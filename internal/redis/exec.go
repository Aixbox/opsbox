package redis

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"opsbox/internal/platform/console"
	"opsbox/internal/platform/opscheck"
	"opsbox/internal/platform/redisx"
	"opsbox/internal/platform/security"
)

// 单次命令执行的超时：Redis 命令通常亚毫秒级，30s 已覆盖大库 KEYS / 慢查询；
// 更长的交互（订阅、阻塞弹出）在分类器里就被拒绝了。
const execTimeout = 30 * time.Second

// maxCommandLength 与 SSH 模块一致：命令文本（含空格引号）的总长度上限。
const maxCommandLength = 8000

// maxArgCount 单条命令的参数个数上限。
const maxArgCount = 32

// ExecLog 是审计日志行（只有元数据，不落 value 内容）。
type ExecLog struct {
	ID             int64      `json:"id"`
	ConnectionID   int64      `json:"connectionId"`
	ConnectionName string     `json:"connectionName"`
	UserID         *int64     `json:"userId"`
	Username       string     `json:"username"`
	Command        string     `json:"command"`
	Status         string     `json:"status"`
	ReplyBytes     int        `json:"replyBytes"`
	Truncated      bool       `json:"truncated"`
	DurationMs     int64      `json:"durationMs"`
	Error          string     `json:"error"`
	Options        LogOptions `json:"options"`
	ApprovedBy     *int64     `json:"approvedBy"`
	ApprovedByName string     `json:"approvedByName"`
	ApprovedAt     *time.Time `json:"approvedAt"`
	CreatedAt      time.Time  `json:"createdAt"`
	FinishedAt     *time.Time `json:"finishedAt"`
}

// LogOptions 随日志行携带的执行参数（批准时据此执行）与分类备注。
type LogOptions struct {
	Args   []string `json:"args"`
	Reason string   `json:"reason,omitempty"`
}

// ExecRequest 是 POST exec 的请求体：一条命令的完整参数（args[0] 是命令名）。
type ExecRequest struct {
	Args []string `json:"args"`
	// ConfirmToken 是审批预检令牌：CLI 提交需审批写命令时必须先取得（check 接口或
	// CHECK_REQUIRED 响应下发），Web 端人工操作不需要。
	ConfirmToken string `json:"confirmToken"`
	// Source 标记来源（cli / web），只用于控制台面板的展示区分，由 handler 按认证方式填充。
	Source string `json:"-"`
}

// ExecOutcome 是 exec / 轮询命令状态的统一响应。
// Reply / Text 在命令成功且回复尚在保留期内时返回（同步执行的响应、或经 GET /redis/commands/{id} 重取）。
type ExecOutcome struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Reply      any    `json:"reply,omitempty"`
	Text       string `json:"text,omitempty"`
	ReplyBytes int    `json:"replyBytes"`
	Truncated  bool   `json:"truncated"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
	Reason     string `json:"reason,omitempty"`
	// ReplyNote 提示回复已超过保留期（或设置为不保存），只剩元数据
	ReplyNote string `json:"replyNote,omitempty"`
}

// normalizeArgs 校验并规整命令参数。
func normalizeArgs(args []string) ([]string, error) {
	if len(args) < 1 {
		return nil, invalid("args 不能为空（第一个元素是命令名，如 HGETALL）")
	}
	if len(args) > maxArgCount {
		return nil, invalid(fmt.Sprintf("命令最多 %d 个参数", maxArgCount))
	}
	total := 0
	for i, arg := range args {
		args[i] = strings.TrimSpace(arg)
		if i == 0 && args[i] == "" {
			return nil, invalid("命令名不能为空")
		}
		total += len(args[i])
	}
	if total > maxCommandLength {
		return nil, invalid(fmt.Sprintf("命令总长度不能超过 %d 个字节", maxCommandLength))
	}
	return args, nil
}

// Exec 执行命令：黑名单 → 会话门禁 → 分类（拒绝 / 策略）→ 执行或入待批队列 → 审计 → 广播。
//
// 会话门禁与 SSH 同一口径：该连接必须有已打开的控制台会话，CLI 才能操作；
// 执行结果广播给会话，正在看面板的人能实时看到 AI 做了什么。
//
// 黑名单排在门禁之前：危险命令即便在没有会话时提交，也要留下 blocked 审计记录。
func (s *Service) Exec(ctx context.Context, userID, connectionID int64, request ExecRequest) (ExecOutcome, error) {
	args, err := normalizeArgs(request.Args)
	if err != nil {
		return ExecOutcome{}, err
	}
	conn, err := s.GetConnection(ctx, connectionID)
	if err != nil {
		return ExecOutcome{}, err
	}
	if !conn.Enabled {
		return ExecOutcome{}, ErrDisabled
	}
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return ExecOutcome{}, err
	}
	display := redisx.FormatCommand(args, maxCommandLength)

	// 1. 黑名单全模式生效
	if entry := redisx.MatchBlocked(args, settings.BlockedCommands); entry != "" {
		reason := fmt.Sprintf("命中黑名单 [%s]：该命令被平台禁用，任何写策略下都不放行", entry)
		id, err := s.insertLog(ctx, connectionID, userID, display, "blocked", LogOptions{Args: args, Reason: reason}, reason, true)
		if err != nil {
			return ExecOutcome{}, err
		}
		outcome := ExecOutcome{ID: id, Status: "blocked", Reason: reason, Error: reason}
		s.broadcastOutcome(connectionID, userID, request.Source, display, outcome)
		return outcome, nil
	}

	// 2. 会话门禁
	if err := s.requireOpenSession(connectionID, userID); err != nil {
		return ExecOutcome{}, err
	}

	// 3. 静态分类；无状态模型不支持的命令直接拒
	verdict := redisx.Classify(args[0], args[1:])
	if verdict.Class == redisx.ClassDenied {
		id, err := s.insertLog(ctx, connectionID, userID, display, "rejected", LogOptions{Args: args, Reason: verdict.Reason}, verdict.Reason, true)
		if err != nil {
			return ExecOutcome{}, err
		}
		outcome := ExecOutcome{ID: id, Status: "rejected", Reason: verdict.Reason, Error: verdict.Reason}
		s.broadcastOutcome(connectionID, userID, request.Source, display, outcome)
		return outcome, nil
	}

	// 4. 静态表不认识时取一次客户端：预热运行时元数据后重新分类，再决定策略
	var handle *redisx.Handle
	if verdict.Unknown {
		if _, handle, err = s.client(ctx, connectionID); err != nil {
			return ExecOutcome{}, err
		}
		verdict = handle.Classifier().Classify(args[0], args[1:])
	}

	// 5. 写命令按连接策略分流
	if verdict.Class == redisx.ClassWrite {
		switch conn.WritePolicy {
		case PolicyReadonly:
			reason := "连接为只读（readonly）策略，写命令一律拒绝"
			if verdict.Reason != "" {
				reason += "；" + verdict.Reason
			}
			id, err := s.insertLog(ctx, connectionID, userID, display, "rejected", LogOptions{Args: args, Reason: verdict.Reason}, reason, true)
			if err != nil {
				return ExecOutcome{}, err
			}
			outcome := ExecOutcome{ID: id, Status: "rejected", Reason: verdict.Reason, Error: reason}
			s.broadcastOutcome(connectionID, userID, request.Source, display, outcome)
			return outcome, nil
		case PolicyConfirm:
			// 审批预检卡口：CLI 来源的需审批写命令必须先取得用户确认（预检令牌）；
			// Web 端是人工提交，用户本来就在场，不做强制。
			if request.Source == "cli" && !opscheck.VerifyToken(s.cipher, checkScope, connectionID, argsKey(args), request.ConfirmToken) {
				return ExecOutcome{}, &opscheck.RequiredError{Check: approvalCheck(connectionID, args, s.cipher)}
			}
			id, err := s.insertLog(ctx, connectionID, userID, display, "pending", LogOptions{Args: args, Reason: verdict.Reason}, "", false)
			if err != nil {
				return ExecOutcome{}, err
			}
			outcome := ExecOutcome{ID: id, Status: "pending", Reason: verdict.Reason}
			s.broadcastOutcome(connectionID, userID, request.Source, display, outcome)
			return outcome, nil
		}
	}

	// 6. 直接执行（读命令，或 allow 策略的写命令）
	id, err := s.insertLog(ctx, connectionID, userID, display, "running", LogOptions{Args: args, Reason: verdict.Reason}, "", false)
	if err != nil {
		return ExecOutcome{}, err
	}
	// 脱离请求上下文的 2s 全局死线，由命令自身超时控制
	outcome := s.runExec(context.WithoutCancel(ctx), connectionID, id, args, settings, handle)
	outcome.Reason = verdict.Reason
	s.broadcastOutcome(connectionID, userID, request.Source, display, outcome)
	return outcome, nil
}

// checkScope 是 redis 模块预检令牌的域隔离前缀。
const checkScope = "redis-check"

// argsKey 是预检令牌绑定的命令表示：规整后的参数用单元分隔符拼接。
// 重新提交时 args 必须与预检时完全一致才能通过校验。
func argsKey(args []string) string { return strings.Join(args, "\x1f") }

// approvalCheck 组装 action=approval 的预检结果（含确定性令牌与指引）。
func approvalCheck(connectionID int64, args []string, cipher *security.TokenCipher) opscheck.Outcome {
	reason := "连接为 confirm 策略，写命令需人工批准"
	return opscheck.Outcome{
		Action:   opscheck.ActionApproval,
		Reason:   reason,
		Token:    opscheck.Token(cipher, checkScope, connectionID, argsKey(args)),
		Guidance: opscheck.GuidanceFor(opscheck.ActionApproval, "redisctl", "Redis → 待批准操作", reason),
	}
}

// CheckExec 预检一条命令是否需要平台审批：无副作用（不建审计记录、不占命令 ID、
// 不碰会话门禁），与 Exec 走同一套黑名单与分类判定；分类器不认识的命令会预热
// 运行时元数据后重新分类（只读连接池操作，无数据副作用）。
func (s *Service) CheckExec(ctx context.Context, connectionID int64, request ExecRequest) (opscheck.Outcome, error) {
	args, err := normalizeArgs(request.Args)
	if err != nil {
		return opscheck.Outcome{}, err
	}
	conn, err := s.GetConnection(ctx, connectionID)
	if err != nil {
		return opscheck.Outcome{}, err
	}
	if !conn.Enabled {
		return opscheck.Outcome{}, ErrDisabled
	}
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return opscheck.Outcome{}, err
	}
	if entry := redisx.MatchBlocked(args, settings.BlockedCommands); entry != "" {
		reason := fmt.Sprintf("命中黑名单 [%s]：该命令被平台禁用，任何写策略下都不放行", entry)
		return opscheck.Outcome{Action: opscheck.ActionBlocked, Reason: reason, Guidance: opscheck.GuidanceFor(opscheck.ActionBlocked, "redisctl", "", reason)}, nil
	}
	verdict := redisx.Classify(args[0], args[1:])
	if verdict.Unknown {
		if _, handle, err := s.client(ctx, connectionID); err != nil {
			return opscheck.Outcome{}, err
		} else {
			verdict = handle.Classifier().Classify(args[0], args[1:])
		}
	}
	if verdict.Class == redisx.ClassDenied {
		reason := verdict.Reason
		if reason == "" {
			reason = "该命令被分类器拒绝"
		}
		return opscheck.Outcome{Action: opscheck.ActionBlocked, Reason: reason, Guidance: opscheck.GuidanceFor(opscheck.ActionBlocked, "redisctl", "", reason)}, nil
	}
	if verdict.Class == redisx.ClassWrite {
		switch conn.WritePolicy {
		case PolicyReadonly:
			reason := "连接为只读（readonly）策略，写命令一律拒绝"
			return opscheck.Outcome{Action: opscheck.ActionBlocked, Reason: reason, Guidance: opscheck.GuidanceFor(opscheck.ActionBlocked, "redisctl", "", reason)}, nil
		case PolicyConfirm:
			return approvalCheck(connectionID, args, s.cipher), nil
		}
	}
	return opscheck.Outcome{Action: opscheck.ActionExecute, Guidance: opscheck.GuidanceFor(opscheck.ActionExecute, "redisctl", "", "")}, nil
}

// broadcastOutcome 把一次执行的结果推给该连接上打开着的控制台面板。
func (s *Service) broadcastOutcome(connectionID, userID int64, source, command string, outcome ExecOutcome) {
	if source == "" {
		source = "cli"
	}
	s.broadcast(connectionID, userID, console.Event{
		Type: "command", Source: source, LogID: outcome.ID, Command: command,
		Status: outcome.Status, Result: outcome.Reply, Text: outcome.Text,
		Error: outcome.Error, Note: outcome.Reason, DurationMs: outcome.DurationMs,
	})
}

// runExec 从池取客户端执行并收尾日志；审批后的异步执行复用同一路径（handle 传 nil）。
func (s *Service) runExec(ctx context.Context, connectionID, logID int64, args []string, settings Settings, handle *redisx.Handle) ExecOutcome {
	outcome := ExecOutcome{ID: logID}
	started := time.Now()
	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	if handle == nil {
		var err error
		if _, handle, err = s.client(execCtx, connectionID); err != nil {
			outcome.Status, outcome.Error, outcome.DurationMs = "failed", err.Error(), time.Since(started).Milliseconds()
			s.finishLog(ctx, logID, outcome)
			return outcome
		}
	}
	run := redisx.RunCommand(execCtx, handle.Client(), args, settings.ValueTruncateBytes, redisx.TotalReplyLimitBytes)
	outcome.DurationMs = time.Since(started).Milliseconds()
	outcome.Reply = run.Reply.JSONValue
	outcome.Text = run.Reply.Text
	outcome.ReplyBytes = run.Reply.Bytes
	outcome.Truncated = run.Reply.Truncated
	switch {
	case run.TimedOut:
		outcome.Status = "timeout"
		outcome.Error = fmt.Sprintf("命令超过 %s 未结束，已终止", execTimeout)
	case run.RedisErr != nil:
		outcome.Status = "failed"
		outcome.Error = run.RedisErr.Error()
	default:
		outcome.Status = "success"
	}
	s.finishLog(ctx, logID, outcome)
	// 回复加密落库：CLI / AI 断线后可经 GET /redis/commands/{id} 重取（保留期由设置控制）
	s.saveReply(ctx, logID, outcome, settings.OutputRetentionDays)
	return outcome
}

// replyPayload 是落库的回复内容（Reply 为 JSON 安全结构，Text 为文本渲染，二者同源）。
type replyPayload struct {
	Reply any    `json:"reply"`
	Text  string `json:"text"`
}

// saveReply 加密保存成功命令的回复（保留天数为 0 或回复为空时跳过）。写失败只记日志，不影响命令结果。
func (s *Service) saveReply(ctx context.Context, logID int64, outcome ExecOutcome, retentionDays int) {
	if retentionDays <= 0 || outcome.Status != "success" || (outcome.Reply == nil && outcome.Text == "") {
		return
	}
	raw, err := json.Marshal(replyPayload{Reply: outcome.Reply, Text: outcome.Text})
	if err != nil {
		s.log.Error("marshal redis reply", "log", logID, "error", err)
		return
	}
	encrypted, err := s.cipher.Encrypt(string(raw))
	if err != nil {
		s.log.Error("encrypt redis reply", "log", logID, "error", err)
		return
	}
	if _, err := s.db.ExecContext(context.WithoutCancel(ctx), `INSERT OR REPLACE INTO redis_exec_outputs(log_id,reply_ciphertext,created_at) VALUES(?,?,?)`,
		logID, encrypted, time.Now().UTC().UnixMilli()); err != nil {
		s.log.Error("save redis reply", "log", logID, "error", err)
	}
}

// loadReply 读回并解密回复；没有存过（或已过保留期 / 保留天数为 0）返回 ok=false。
func (s *Service) loadReply(ctx context.Context, logID int64) (ExecOutcome, bool) {
	var ciphertext []byte
	if err := s.db.QueryRowContext(ctx, `SELECT reply_ciphertext FROM redis_exec_outputs WHERE log_id=?`, logID).Scan(&ciphertext); err != nil {
		return ExecOutcome{}, false
	}
	plain, err := s.cipher.Decrypt(ciphertext)
	if err != nil {
		s.log.Error("decrypt redis reply", "log", logID, "error", err)
		return ExecOutcome{}, false
	}
	var payload replyPayload
	if err := json.Unmarshal([]byte(plain), &payload); err != nil {
		s.log.Error("unmarshal redis reply", "log", logID, "error", err)
		return ExecOutcome{}, false
	}
	return ExecOutcome{Reply: payload.Reply, Text: payload.Text}, true
}

// Approve 批准待批命令：后台异步执行，批准者的请求不等待；回复照常加密落库，CLI 稍后轮询即可重取。
func (s *Service) Approve(ctx context.Context, approverID, logID int64) (ExecLog, error) {
	entry, err := s.GetLog(ctx, logID)
	if err != nil {
		return ExecLog{}, err
	}
	if entry.Status != "pending" {
		return ExecLog{}, fmt.Errorf("%w：该命令当前状态为 %s", ErrConflict, entry.Status)
	}
	if len(entry.Options.Args) == 0 {
		return ExecLog{}, fmt.Errorf("%w：日志缺少执行参数，无法批准", ErrConflict)
	}
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return ExecLog{}, err
	}
	// 黑名单可能在提交后被收紧：批准时刻再拦一次
	if hit := redisx.MatchBlocked(entry.Options.Args, settings.BlockedCommands); hit != "" {
		reason := fmt.Sprintf("批准时命中黑名单 [%s]，已自动拒绝", hit)
		now := time.Now().UTC().UnixMilli()
		if _, err := s.db.ExecContext(ctx, `UPDATE redis_exec_logs SET status='rejected',approved_by=?,approved_at=?,error=?,finished_at=? WHERE id=? AND status='pending'`,
			nullableID(approverID), now, reason, now, logID); err != nil {
			return ExecLog{}, err
		}
		s.broadcast(entry.ConnectionID, approverID, console.Event{
			Type: "command", Source: "approved", LogID: logID, Command: entry.Command,
			Status: "rejected", Error: reason,
		})
		return s.GetLog(ctx, logID)
	}
	now := time.Now().UTC().UnixMilli()
	result, err := s.db.ExecContext(ctx, `UPDATE redis_exec_logs SET status='running',approved_by=?,approved_at=? WHERE id=? AND status='pending'`, nullableID(approverID), now, logID)
	if err != nil {
		return ExecLog{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ExecLog{}, ErrConflict
	}
	args := append([]string(nil), entry.Options.Args...)
	command := entry.Command
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.Error("approved redis exec panicked", "log", logID, "panic", recovered)
			}
		}()
		outcome := s.runExec(context.Background(), entry.ConnectionID, logID, args, settings, nil)
		// 批准后异步执行：结果推给该连接上所有打开的面板（批准者未必是发起人）
		s.sessions.BroadcastConnection(entry.ConnectionID, console.Event{
			Type: "command", Source: "approved", LogID: logID, Command: command,
			Status: outcome.Status, Result: outcome.Reply, Text: outcome.Text,
			Error: outcome.Error, DurationMs: outcome.DurationMs,
		})
	}()
	return s.GetLog(ctx, logID)
}

// Reject 拒绝待批命令。
func (s *Service) Reject(ctx context.Context, approverID, logID int64) (ExecLog, error) {
	entry, err := s.GetLog(ctx, logID)
	if err != nil {
		return ExecLog{}, err
	}
	now := time.Now().UTC().UnixMilli()
	result, err := s.db.ExecContext(ctx, `UPDATE redis_exec_logs SET status='rejected',approved_by=?,approved_at=?,finished_at=?,error='已被拒绝' WHERE id=? AND status='pending'`, nullableID(approverID), now, now, logID)
	if err != nil {
		return ExecLog{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		if _, err := s.GetLog(ctx, logID); err != nil {
			return ExecLog{}, err
		}
		return ExecLog{}, fmt.Errorf("%w：该命令已不在待批状态", ErrConflict)
	}
	s.sessions.BroadcastConnection(entry.ConnectionID, console.Event{
		Type: "command", Source: "approved", LogID: logID, Command: entry.Command,
		Status: "rejected", Error: "已被拒绝",
	})
	return s.GetLog(ctx, logID)
}

// ---- 扫描 ----

// ScanRequest 是 POST scan 的请求体。
type ScanRequest struct {
	Pattern string `json:"pattern"`
	Limit   int    `json:"limit"`
	Count   int    `json:"count"`
}

// ScanResult 是安全 SCAN 的结果。
type ScanResult struct {
	Keys       []string `json:"keys"`
	Truncated  bool     `json:"truncated"`
	Total      int      `json:"total"`
	DurationMs int64    `json:"durationMs"`
}

// Scan 用游标迭代 SCAN 摸 key 分布（替代 KEYS，避免大库阻塞）。
func (s *Service) Scan(ctx context.Context, userID, connectionID int64, request ScanRequest) (ScanResult, error) {
	request.Pattern = strings.TrimSpace(request.Pattern)
	if request.Pattern == "" {
		request.Pattern = "*"
	}
	if len(request.Pattern) > 256 {
		return ScanResult{}, invalid("pattern 不能超过 256 个字符")
	}
	conn, err := s.GetConnection(ctx, connectionID)
	if err != nil {
		return ScanResult{}, err
	}
	if !conn.Enabled {
		return ScanResult{}, ErrDisabled
	}
	// 会话门禁：与 exec 同一口径，扫描同样要求该连接有已打开的控制台会话
	if err := s.requireOpenSession(connectionID, userID); err != nil {
		return ScanResult{}, err
	}
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return ScanResult{}, err
	}
	limit := request.Limit
	if limit <= 0 || limit > settings.ScanKeyLimit {
		limit = settings.ScanKeyLimit
	}
	display := fmt.Sprintf("SCAN MATCH %s LIMIT %d", redisx.FormatCommand([]string{request.Pattern}, 128), limit)
	id, err := s.insertLog(ctx, connectionID, userID, display, "running", LogOptions{Args: []string{"SCAN", request.Pattern}}, "", false)
	if err != nil {
		return ScanResult{}, err
	}
	started := time.Now()
	_, handle, err := s.client(ctx, connectionID)
	if err != nil {
		result := ScanResult{DurationMs: time.Since(started).Milliseconds()}
		s.finishLog(ctx, id, ExecOutcome{Status: "failed", Error: err.Error(), DurationMs: result.DurationMs})
		return result, err
	}
	keys, truncated, err := redisx.ScanKeys(ctx, handle.Client(), request.Pattern, limit, request.Count)
	result := ScanResult{Keys: keys, Truncated: truncated, Total: len(keys), DurationMs: time.Since(started).Milliseconds()}
	// key 列表作为回复落库：断线后 redisctl status <id> 可重取
	keysText := strings.Join(keys, "\n")
	outcome := ExecOutcome{Status: "success", Reply: keys, Text: keysText, ReplyBytes: len(keysText), DurationMs: result.DurationMs}
	if err != nil {
		result.Keys = nil
		result.Total = 0
		outcome.Status, outcome.Error = "failed", err.Error()
	}
	s.finishLog(ctx, id, outcome)
	s.saveReply(ctx, id, outcome, settings.OutputRetentionDays)
	s.broadcast(connectionID, userID, console.Event{
		Type: "command", Source: "cli", LogID: id, Command: display,
		Status: outcome.Status, Result: outcome.Reply, Text: outcome.Text,
		Error: outcome.Error, DurationMs: outcome.DurationMs,
	})
	return result, err
}

// ---- 日志读写 ----

func (s *Service) insertLog(ctx context.Context, connectionID, userID int64, command, status string, options LogOptions, errText string, finished bool) (int64, error) {
	now := time.Now().UTC().UnixMilli()
	raw, _ := json.Marshal(options)
	var finishedAt any
	if finished {
		finishedAt = now
	}
	result, err := s.db.ExecContext(context.WithoutCancel(ctx), `INSERT INTO redis_exec_logs(connection_id,user_id,command,status,error,options_json,created_at,finished_at) VALUES(?,?,?,?,?,?,?,?)`,
		connectionID, nullableID(userID), command, status, errText, string(raw), now, finishedAt)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Service) finishLog(ctx context.Context, logID int64, outcome ExecOutcome) {
	if _, err := s.db.ExecContext(context.WithoutCancel(ctx), `UPDATE redis_exec_logs SET status=?,reply_bytes=?,truncated=?,duration_ms=?,error=?,finished_at=? WHERE id=?`,
		outcome.Status, outcome.ReplyBytes, outcome.Truncated, outcome.DurationMs, outcome.Error, time.Now().UTC().UnixMilli(), logID); err != nil {
		s.log.Error("finish redis exec log", "log", logID, "error", err)
	}
}

// 本地单用户：没有 users 表，username / approvedByName 恒为空串（占位保持列数与 scanLog 一致）。
const logColumns = `l.id,l.connection_id,COALESCE(c.name,''),l.user_id,'',l.command,l.status,l.reply_bytes,l.truncated,l.duration_ms,l.error,l.options_json,l.approved_by,'',l.approved_at,l.created_at,l.finished_at`
const logJoins = ` FROM redis_exec_logs l LEFT JOIN redis_connections c ON c.id=l.connection_id`

func scanLog(row interface{ Scan(...any) error }) (ExecLog, error) {
	var entry ExecLog
	var userID, approvedBy, approvedAt, finishedAt sql.NullInt64
	var options string
	var created int64
	if err := row.Scan(&entry.ID, &entry.ConnectionID, &entry.ConnectionName, &userID, &entry.Username, &entry.Command, &entry.Status, &entry.ReplyBytes, &entry.Truncated, &entry.DurationMs, &entry.Error, &options, &approvedBy, &entry.ApprovedByName, &approvedAt, &created, &finishedAt); err != nil {
		return entry, err
	}
	if userID.Valid {
		entry.UserID = &userID.Int64
	}
	if approvedBy.Valid {
		entry.ApprovedBy = &approvedBy.Int64
	}
	if approvedAt.Valid {
		t := time.UnixMilli(approvedAt.Int64).UTC()
		entry.ApprovedAt = &t
	}
	if finishedAt.Valid {
		t := time.UnixMilli(finishedAt.Int64).UTC()
		entry.FinishedAt = &t
	}
	entry.CreatedAt = time.UnixMilli(created).UTC()
	_ = json.Unmarshal([]byte(options), &entry.Options)
	return entry, nil
}

func (s *Service) GetLog(ctx context.Context, id int64) (ExecLog, error) {
	return scanLog(s.db.QueryRowContext(ctx, `SELECT `+logColumns+logJoins+` WHERE l.id=?`, id))
}

// GetCommand 供 CLI 轮询与审计页查看：返回日志状态 + 保留期内的回复内容。
// 路由已要求 system:redis:exec 权限；能执行任意命令的人读别人的回复不构成提权。
func (s *Service) GetCommand(ctx context.Context, logID int64) (ExecOutcome, error) {
	entry, err := s.GetLog(ctx, logID)
	if err != nil {
		return ExecOutcome{}, err
	}
	outcome := ExecOutcome{ID: entry.ID, Status: entry.Status, ReplyBytes: entry.ReplyBytes, Truncated: entry.Truncated, DurationMs: entry.DurationMs, Error: entry.Error, Reason: entry.Options.Reason}
	if payload, ok := s.loadReply(ctx, logID); ok {
		outcome.Reply, outcome.Text = payload.Reply, payload.Text
	} else if entry.Status == "success" {
		// 成功却读不到落库回复：保留期已过，或当时设置为不保存
		outcome.ReplyNote = "回复已超过保留期（或设置为不保存），只剩元数据；需要数据请重新执行对应的读命令"
	}
	return outcome, nil
}

// LogFilter 是日志列表的过滤条件。
type LogFilter struct {
	ConnectionID int64
	Status       string
	UserID       int64
	From, To     *time.Time
	Page         int
	PageSize     int
}

func (s *Service) ListLogs(ctx context.Context, filter LogFilter) (Page[ExecLog], error) {
	where := []string{"1=1"}
	args := []any{}
	if filter.ConnectionID > 0 {
		where = append(where, "l.connection_id=?")
		args = append(args, filter.ConnectionID)
	}
	if filter.Status != "" {
		where = append(where, "l.status=?")
		args = append(args, filter.Status)
	}
	if filter.UserID > 0 {
		where = append(where, "l.user_id=?")
		args = append(args, filter.UserID)
	}
	if filter.From != nil {
		where = append(where, "l.created_at>=?")
		args = append(args, filter.From.UnixMilli())
	}
	if filter.To != nil {
		where = append(where, "l.created_at<=?")
		args = append(args, filter.To.UnixMilli())
	}
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize < 1 || filter.PageSize > 200 {
		filter.PageSize = 20
	}
	clause := strings.Join(where, " AND ")
	page := Page[ExecLog]{Items: []ExecLog{}, Page: filter.Page, PageSize: filter.PageSize}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redis_exec_logs l WHERE `+clause, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := s.db.QueryContext(ctx, `SELECT `+logColumns+logJoins+` WHERE `+clause+` ORDER BY l.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		entry, err := scanLog(rows)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, entry)
	}
	return page, rows.Err()
}

// ListPending 返回待批队列（按时间正序）。
func (s *Service) ListPending(ctx context.Context) ([]ExecLog, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+logColumns+logJoins+` WHERE l.status='pending' ORDER BY l.id ASC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ExecLog{}
	for rows.Next() {
		entry, err := scanLog(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, entry)
	}
	return items, rows.Err()
}

// Cleanup 删除 90 天前的审计日志、按设置清理过期回复（保留天数为 0 时清空全部）、
// 回收过期待批项（超过 24 小时未批准置为 rejected）与孤儿 running（服务重启遗留）。
func (s *Service) Cleanup(ctx context.Context, retention time.Duration) error {
	now := time.Now().UTC()
	if retention > 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM redis_exec_logs WHERE created_at<? AND status NOT IN ('pending','running')`, now.Add(-retention).UnixMilli()); err != nil {
			return err
		}
	}
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return err
	}
	// 日志留 90 天可追溯「谁执行了什么」；回复内容按设置保留（通常远短于日志）
	if settings.OutputRetentionDays <= 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM redis_exec_outputs`); err != nil {
			return err
		}
	} else if _, err := s.db.ExecContext(ctx, `DELETE FROM redis_exec_outputs WHERE created_at<?`,
		now.AddDate(0, 0, -settings.OutputRetentionDays).UnixMilli()); err != nil {
		return err
	}
	stale := now.Add(-24 * time.Hour).UnixMilli()
	if _, err := s.db.ExecContext(ctx, `UPDATE redis_exec_logs SET status='rejected',error='超过 24 小时未批准，已自动作废',finished_at=? WHERE status='pending' AND created_at<?`, now.UnixMilli(), stale); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE redis_exec_logs SET status='failed',error='执行状态丢失（服务重启）',finished_at=? WHERE status='running' AND created_at<?`, now.UnixMilli(), now.Add(-time.Hour).UnixMilli())
	return err
}
