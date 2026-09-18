package ssh

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"opsbox/internal/platform/sshx"
)

// MaxChunkBytes 是单个分片请求的字节上限。文件总大小不设限——大文件由 CLI 切片顺序上传，
// 中断后按远端已有大小续传；限制单片只是为了给请求一个有界的内存 / 时间预算。
const MaxChunkBytes = 32 << 20

// TransferRequest 是登记一次传输意图的请求体。
type TransferRequest struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	// Size 是文件总字节数（上传时由 CLI 给出）；>0 时服务端据此判断传输完成。
	Size int64 `json:"size"`
}

// RequestTransfer 登记一次 SFTP 传输意图：audit 模式直接放行（status=approved），approve 模式入待批队列。
// 黑名单不适用于路径；但拒绝写入 /dev、/proc、/sys。
func (s *Service) RequestTransfer(ctx context.Context, userID, connectionID int64, request TransferRequest) (ExecLog, error) {
	if request.Kind != "upload" && request.Kind != "download" {
		return ExecLog{}, invalid("传输类型必须是 upload 或 download")
	}
	remotePath := strings.TrimSpace(request.Path)
	if remotePath == "" || len(remotePath) > 4096 || strings.ContainsRune(remotePath, 0) {
		return ExecLog{}, invalid("远端路径无效")
	}
	// 允许绝对路径（/…）、家目录（~…）或相对登录目录的相对路径；相对路径不得越级
	if !strings.HasPrefix(remotePath, "/") && !strings.HasPrefix(remotePath, "~") {
		for _, segment := range strings.Split(remotePath, "/") {
			if segment == ".." {
				return ExecLog{}, invalid("相对路径不能包含 ..，请使用绝对路径")
			}
		}
	}
	if request.Size < 0 {
		return ExecLog{}, invalid("文件大小无效")
	}
	cleaned := path.Clean(remotePath)
	for _, forbidden := range []string{"/dev", "/proc", "/sys"} {
		if request.Kind == "upload" && (cleaned == forbidden || strings.HasPrefix(cleaned, forbidden+"/")) {
			return ExecLog{}, invalid("不允许写入 " + forbidden)
		}
	}
	conn, err := s.GetConnection(ctx, connectionID)
	if err != nil {
		return ExecLog{}, err
	}
	if !conn.Enabled {
		return ExecLog{}, ErrDisabled
	}
	// 会话门禁：与 exec 同一口径——连接必须是「活的」（有人正开着终端），CLI 才能传文件。
	// 传输本身走独立的 SFTP 连接（PTY 传不了文件），会话在这里只作为授权凭据。
	if len(s.sessions.FindByConnection(connectionID, userID)) == 0 {
		return ExecLog{}, ErrNoOpenSession
	}
	status := "approved"
	if conn.ExecPolicy == "approve" {
		status = "pending"
	}
	id, err := s.insertLog(ctx, connectionID, userID, request.Kind, remotePath, status, LogOptions{TotalBytes: request.Size}, "", false)
	if err != nil {
		return ExecLog{}, err
	}
	return s.GetLog(ctx, id)
}

// claimTransfer 校验传输记录归属 / 类型 / 路径，并把 approved 置为 running（分片间保持 running）。
func (s *Service) claimTransfer(ctx context.Context, userID, connectionID, logID int64, kind, remotePath string) (ExecLog, error) {
	entry, err := s.GetLog(ctx, logID)
	if err != nil {
		return ExecLog{}, err
	}
	if entry.ConnectionID != connectionID || entry.Kind != kind || (entry.UserID != nil && *entry.UserID != userID) {
		return ExecLog{}, ErrForbidden
	}
	if entry.Command != strings.TrimSpace(remotePath) {
		return ExecLog{}, invalid("路径与批准的传输记录不一致")
	}
	switch entry.Status {
	case "running":
		return entry, nil // 续传后续分片
	case "approved":
	case "pending":
		return entry, nil // 调用方据此返回 202
	default:
		return ExecLog{}, fmt.Errorf("%w：传输记录状态为 %s", ErrConflict, entry.Status)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE ssh_exec_logs SET status='running' WHERE id=? AND status='approved'`, logID); err != nil {
		return ExecLog{}, err
	}
	entry.Status = "running"
	return entry, nil
}

// TransferChunk 描述一次分片传输。
type TransferChunk struct {
	TransferID int64
	Kind       string
	Path       string
	Offset     int64
	// Final 为 true 表示这是最后一片，写完即把传输记录收尾为 success。
	Final bool
}

// TransferResult 是一次分片传输的结果。
type TransferResult struct {
	LogID    int64  `json:"logId"`
	Status   string `json:"status"`
	Written  int64  `json:"written"`
	Total    int64  `json:"total"`
	Complete bool   `json:"complete"`
}

// UploadChunk 把 body 写到远端 offset 处。transferID 为 0 时先登记（approve 模式返回 pending 记录，不写数据）。
func (s *Service) UploadChunk(ctx context.Context, userID, connectionID int64, chunk TransferChunk, body io.Reader) (TransferResult, ExecLog, error) {
	entry, err := s.prepareChunk(ctx, userID, connectionID, "upload", chunk)
	if err != nil || entry.Status == "pending" {
		return TransferResult{LogID: entry.ID, Status: entry.Status}, entry, err
	}
	started := time.Now()
	transferCtx := context.WithoutCancel(ctx)
	_, client, err := s.dial(transferCtx, connectionID)
	if err != nil {
		s.finishLog(transferCtx, entry.ID, ExecOutcome{Status: "failed", Error: err.Error(), StdoutBytes: int(entry.StdoutBytes), DurationMs: time.Since(started).Milliseconds()})
		return TransferResult{}, entry, err
	}
	defer client.Close()
	written, err := sshx.UploadAt(ctx, client, chunk.Path, chunk.Offset, body)
	total := chunk.Offset + written
	if err != nil {
		s.failTransfer(transferCtx, entry, total, err)
		return TransferResult{}, entry, err
	}
	return s.advanceTransfer(transferCtx, entry, total, chunk.Final), entry, nil
}

// DownloadChunk 从远端 offset 处读到 writer；prepare 在真正开始读之前回调（用于写响应头）。
func (s *Service) DownloadChunk(ctx context.Context, userID, connectionID int64, chunk TransferChunk, prepare func(size int64), writer io.Writer) (TransferResult, ExecLog, error) {
	entry, err := s.prepareChunk(ctx, userID, connectionID, "download", chunk)
	if err != nil || entry.Status == "pending" {
		return TransferResult{LogID: entry.ID, Status: entry.Status}, entry, err
	}
	started := time.Now()
	transferCtx := context.WithoutCancel(ctx)
	_, client, err := s.dial(transferCtx, connectionID)
	if err != nil {
		s.finishLog(transferCtx, entry.ID, ExecOutcome{Status: "failed", Error: err.Error(), DurationMs: time.Since(started).Milliseconds()})
		return TransferResult{}, entry, err
	}
	defer client.Close()
	size, exists, err := sshx.StatSize(client, chunk.Path)
	if err != nil || !exists {
		if err == nil {
			err = fmt.Errorf("远端文件不存在: %s", chunk.Path)
		}
		s.failTransfer(transferCtx, entry, 0, err)
		return TransferResult{}, entry, err
	}
	if chunk.Offset > size {
		err := invalid(fmt.Sprintf("续传偏移 %d 超出远端文件大小 %d", chunk.Offset, size))
		s.failTransfer(transferCtx, entry, 0, err)
		return TransferResult{}, entry, err
	}
	prepare(size)
	read, err := sshx.DownloadAt(ctx, client, chunk.Path, chunk.Offset, writer)
	total := chunk.Offset + read
	if err != nil {
		s.failTransfer(transferCtx, entry, total, err)
		return TransferResult{}, entry, err
	}
	return s.advanceTransfer(transferCtx, entry, total, total >= size), entry, nil
}

// StatRemote 返回远端文件大小，供 CLI 决定从哪里续传。
func (s *Service) StatRemote(ctx context.Context, userID, connectionID int64, remotePath string) (map[string]any, error) {
	remotePath = strings.TrimSpace(remotePath)
	if remotePath == "" {
		return nil, invalid("远端路径无效")
	}
	if len(s.sessions.FindByConnection(connectionID, userID)) == 0 {
		return nil, ErrNoOpenSession
	}
	dialCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_, client, err := s.dial(dialCtx, connectionID)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	size, exists, err := sshx.StatSize(client, remotePath)
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": remotePath, "exists": exists, "size": size}, nil
}

// prepareChunk 登记（或认领）传输记录，返回可写入的记录；status=pending 表示需等待批准。
func (s *Service) prepareChunk(ctx context.Context, userID, connectionID int64, kind string, chunk TransferChunk) (ExecLog, error) {
	if chunk.Offset < 0 {
		return ExecLog{}, invalid("偏移量无效")
	}
	if chunk.TransferID == 0 {
		entry, err := s.RequestTransfer(ctx, userID, connectionID, TransferRequest{Kind: kind, Path: chunk.Path})
		if err != nil {
			return ExecLog{}, err
		}
		if entry.Status == "pending" {
			return entry, nil
		}
		chunk.TransferID = entry.ID
	}
	return s.claimTransfer(ctx, userID, connectionID, chunk.TransferID, kind, chunk.Path)
}

// advanceTransfer 记录累计字节数；final 时收尾为 success，否则保持 running 等下一片。
func (s *Service) advanceTransfer(ctx context.Context, entry ExecLog, total int64, final bool) TransferResult {
	declared := entry.Options.TotalBytes
	complete := final || (declared > 0 && total >= declared)
	if complete {
		s.finishLog(ctx, entry.ID, ExecOutcome{Status: "success", StdoutBytes: int(total), DurationMs: time.Since(entry.CreatedAt).Milliseconds()})
	} else if _, err := s.db.ExecContext(ctx, `UPDATE ssh_exec_logs SET stdout_bytes=?,heartbeat_at=? WHERE id=?`, total, time.Now().UTC().UnixMilli(), entry.ID); err != nil {
		s.log.Error("update ssh transfer progress", "log", entry.ID, "error", err)
	}
	status := "running"
	if complete {
		status = "success"
	}
	return TransferResult{LogID: entry.ID, Status: status, Written: total, Total: declared, Complete: complete}
}

func (s *Service) failTransfer(ctx context.Context, entry ExecLog, total int64, cause error) {
	s.finishLog(ctx, entry.ID, ExecOutcome{Status: "failed", Error: cause.Error(), StdoutBytes: int(total), DurationMs: time.Since(entry.CreatedAt).Milliseconds()})
}
