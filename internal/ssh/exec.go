package ssh

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"opsbox/internal/platform/opscheck"
	"opsbox/internal/platform/security"
	"opsbox/internal/platform/sshx"
)

// ExecLog 是审计日志行（只有元数据，不含输出内容）。
type ExecLog struct {
	ID             int64      `json:"id"`
	ConnectionID   int64      `json:"connectionId"`
	ConnectionName string     `json:"connectionName"`
	UserID         *int64     `json:"userId"`
	Username       string     `json:"username"`
	Kind           string     `json:"kind"`
	Command        string     `json:"command"`
	Status         string     `json:"status"`
	ExitCode       *int       `json:"exitCode"`
	StdoutBytes    int        `json:"stdoutBytes"`
	StderrBytes    int        `json:"stderrBytes"`
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

// LogOptions 随日志行携带的执行参数（approve 模式批准时据此执行）与静态分析备注。
type LogOptions struct {
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
	PTY            bool   `json:"pty,omitempty"`
	DynamicReason  string `json:"dynamicReason,omitempty"`
	Rows           int    `json:"rows,omitempty"`
	Cols           int    `json:"cols,omitempty"`
	// TotalBytes 是传输类记录声明的文件总字节数，用于判断分片是否传完。
	TotalBytes int64 `json:"totalBytes,omitempty"`
	// SessionID 是命令要在其连接上执行的终端会话。登记时确定，批准后据此找回目标会话。
	SessionID string `json:"sessionId,omitempty"`
}

// ExecRequest 是 POST exec 的请求体。
type ExecRequest struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
	PTY            bool   `json:"pty"`
	// SessionID 指定在哪个终端会话的连接上执行；留空则取该连接最近活跃的会话。
	SessionID string `json:"sessionId"`
	// ConfirmToken 是审批预检令牌：CLI 提交需审批命令时必须先取得（check 接口或
	// CHECK_REQUIRED 响应下发），Web 端人工操作不需要。json:"-" 是因为 Source 同理由 handler 按来源填充。
	ConfirmToken string `json:"confirmToken"`
	// Source 标记来源（cli / web）：预检令牌只强制 CLI 来源，浏览器人工操作不需要。
	Source string `json:"-"`
}

// ExecOutcome 是 exec / 轮询命令状态的统一响应。Stdout/Stderr 只在执行完成且结果尚在缓存时返回。
type ExecOutcome struct {
	ID            int64  `json:"id"`
	Status        string `json:"status"`
	ExitCode      *int   `json:"exitCode,omitempty"`
	Stdout        string `json:"stdout,omitempty"`
	Stderr        string `json:"stderr,omitempty"`
	StdoutBytes   int    `json:"stdoutBytes"`
	StderrBytes   int    `json:"stderrBytes"`
	Truncated     bool   `json:"truncated"`
	DurationMs    int64  `json:"durationMs"`
	Error         string `json:"error,omitempty"`
	BlockedRule   string `json:"blockedRule,omitempty"`
	BlockedReason string `json:"blockedReason,omitempty"`
	Dynamic       bool   `json:"dynamic,omitempty"`
	DynamicReason string `json:"dynamicReason,omitempty"`
	// OutputExpired 表示命令已完成但输出缓存已过期（只剩元数据）
	OutputExpired bool `json:"outputExpired,omitempty"`
}

const (
	maxCommandLength = 8000
	maxExecTimeout   = 600
)

// Exec 执行命令：黑名单 → 会话门禁 → 策略（audit 直接执行 / approve 或动态命令入待批队列）→ 审计。
//
// 命令不再新建连接，而是在用户已打开的终端会话所属的 SSH 连接上另开 exec 通道执行，
// 命令与输出实时回显进那个终端：用户能看到 AI 在做什么，会话关闭即失去操作许可。
//
// 黑名单排在门禁之前：危险命令即便在没有会话时提交，也要留下一条 blocked 审计记录，
// 否则「谁试过 rm -rf /」这类信息会因为没开终端而丢失。
func (s *Service) Exec(ctx context.Context, userID, connectionID int64, request ExecRequest) (ExecOutcome, error) {
	request.Command = strings.TrimSpace(request.Command)
	if request.Command == "" || len(request.Command) > maxCommandLength {
		return ExecOutcome{}, invalid(fmt.Sprintf("命令不能为空且不能超过 %d 个字符", maxCommandLength))
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
	if request.TimeoutSeconds <= 0 {
		request.TimeoutSeconds = settings.ExecTimeoutSeconds
	}
	if request.TimeoutSeconds > maxExecTimeout {
		return ExecOutcome{}, invalid(fmt.Sprintf("超时不能超过 %d 秒；长任务请在服务器上 nohup 后台运行再轮询日志", maxExecTimeout))
	}
	blacklist, _ := sshx.CompileBlacklist(settings.BlacklistPatterns)
	verdict := sshx.Analyze(request.Command, blacklist)
	options := LogOptions{TimeoutSeconds: request.TimeoutSeconds, PTY: request.PTY, DynamicReason: verdict.DynamicReason}
	if verdict.Blocked {
		id, err := s.insertLog(ctx, connectionID, userID, "exec", request.Command, "blocked", options, fmt.Sprintf("命中黑名单 [%s]：%s", verdict.Rule, verdict.Reason), true)
		if err != nil {
			return ExecOutcome{}, err
		}
		return ExecOutcome{ID: id, Status: "blocked", BlockedRule: verdict.Rule, BlockedReason: verdict.Reason, Error: verdict.Reason}, nil
	}
	// 会话门禁：确定目标会话，没有已打开的终端就拒绝——不留任何绕过路径
	session, err := s.resolveSession(connectionID, userID, request.SessionID)
	if err != nil {
		return ExecOutcome{}, err
	}
	options.SessionID = session.ID
	needsApproval := conn.ExecPolicy == "approve" || (verdict.Dynamic && settings.DynamicRequiresApproval)
	if needsApproval {
		// 审批预检卡口：CLI 来源的需审批命令必须先取得用户确认（预检令牌）；
		// Web 端是人工提交，用户本来就在场，不做强制。
		if request.Source == "cli" && !opscheck.VerifyToken(s.cipher, checkScope, connectionID, request.Command, request.ConfirmToken) {
			return ExecOutcome{}, &opscheck.RequiredError{Check: approvalCheck(approvalReason(conn.ExecPolicy, verdict, settings), connectionID, request.Command, s.cipher)}
		}
		id, err := s.insertLog(ctx, connectionID, userID, "exec", request.Command, "pending", options, "", false)
		if err != nil {
			return ExecOutcome{}, err
		}
		return ExecOutcome{ID: id, Status: "pending", Dynamic: verdict.Dynamic, DynamicReason: verdict.DynamicReason}, nil
	}
	id, err := s.insertLog(ctx, connectionID, userID, "exec", request.Command, "running", options, "", false)
	if err != nil {
		return ExecOutcome{}, err
	}
	// 脱离请求上下文的 2s 全局死线，由命令自身超时控制
	execCtx := context.WithoutCancel(ctx)
	outcome, err := s.runExec(execCtx, connectionID, id, request.Command, options, settings)
	if err != nil {
		return outcome, err
	}
	outcome.Dynamic, outcome.DynamicReason = verdict.Dynamic, verdict.DynamicReason
	return outcome, nil
}

// checkScope 是 ssh 模块预检令牌的域隔离前缀，避免与其他模块的同参数令牌互认。
const checkScope = "ssh-check"

// approvalReason 返回该命令需要审批的原因描述。
func approvalReason(policy string, verdict sshx.Verdict, settings Settings) string {
	if verdict.Dynamic {
		return "动态命令强制审批：" + verdict.DynamicReason
	}
	return "连接为 approve（逐条审批）策略"
}

// approvalCheck 组装 action=approval 的预检结果（含确定性令牌与指引）。
func approvalCheck(reason string, connectionID int64, command string, cipher *security.TokenCipher) opscheck.Outcome {
	return opscheck.Outcome{
		Action:   opscheck.ActionApproval,
		Reason:   reason,
		Token:    opscheck.Token(cipher, checkScope, connectionID, command),
		Guidance: opscheck.GuidanceFor(opscheck.ActionApproval, "sshctl", "SSH → 待批准", reason),
	}
}

// CheckExec 预检一条命令是否需要平台审批：无副作用（不建审计记录、不占命令 ID、不碰会话门禁），
// 与 Exec 走同一套黑名单与策略判定。CLI（AI agent）据此决定是否先向用户确认。
func (s *Service) CheckExec(ctx context.Context, connectionID int64, request ExecRequest) (opscheck.Outcome, error) {
	request.Command = strings.TrimSpace(request.Command)
	if request.Command == "" || len(request.Command) > maxCommandLength {
		return opscheck.Outcome{}, invalid(fmt.Sprintf("命令不能为空且不能超过 %d 个字符", maxCommandLength))
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
	blacklist, _ := sshx.CompileBlacklist(settings.BlacklistPatterns)
	verdict := sshx.Analyze(request.Command, blacklist)
	if verdict.Blocked {
		reason := fmt.Sprintf("命中黑名单 [%s]：%s", verdict.Rule, verdict.Reason)
		return opscheck.Outcome{Action: opscheck.ActionBlocked, Reason: reason, Guidance: opscheck.GuidanceFor(opscheck.ActionBlocked, "sshctl", "", reason)}, nil
	}
	if conn.ExecPolicy == "approve" || (verdict.Dynamic && settings.DynamicRequiresApproval) {
		return approvalCheck(approvalReason(conn.ExecPolicy, verdict, settings), connectionID, request.Command, s.cipher), nil
	}
	return opscheck.Outcome{Action: opscheck.ActionExecute, Guidance: opscheck.GuidanceFor(opscheck.ActionExecute, "sshctl", "", "")}, nil
}

// resolveSession 选定目标会话：指定了 sessionID 就用它（并校验归属），否则取该连接最近活跃的那个。
func (s *Service) resolveSession(connectionID, userID int64, sessionID string) (*sshx.Session, error) {
	if sessionID != "" {
		session, err := s.ownedSession(userID, sessionID)
		if err != nil {
			return nil, err
		}
		if session.ConnectionID != connectionID {
			return nil, fmt.Errorf("%w：会话 %s 不属于该连接", ErrForbidden, sessionID)
		}
		return session, nil
	}
	open := s.sessions.FindByConnection(connectionID, userID)
	if len(open) == 0 {
		return nil, ErrNoOpenSession
	}
	return open[0], nil
}

// runExec 在目标会话的连接上执行命令并收尾日志；
// 输出按设置加密落库，供 CLI 断线后重取与审计回溯。
func (s *Service) runExec(ctx context.Context, connectionID, logID int64, command string, options LogOptions, settings Settings) (ExecOutcome, error) {
	outcome := ExecOutcome{ID: logID}
	started := time.Now()
	timeout := time.Duration(options.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	session, err := s.sessions.Get(options.SessionID)
	if err != nil {
		// 等待审批期间会话被关掉是常见情况，错误要说得清楚
		outcome.Status = "failed"
		outcome.Error = "目标终端会话已关闭，命令未执行；请重新打开终端后再试"
		outcome.DurationMs = time.Since(started).Milliseconds()
		s.finishLog(ctx, logID, outcome)
		return outcome, nil
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := session.RunCommand(execCtx, command, sshx.ExecOptions{PTY: options.PTY, OutputLimit: settings.OutputLimitBytes})
	// 正忙（排队等不到执行权）/ 会话在等待期间关闭属于「现在不能执行」，不是「执行失败」：
	// 记终态日志后把原因抛给调用方，CLI 拿到的是可解释的 409，而不是一条语焉不详的 failed。
	if errors.Is(err, sshx.ErrExecBusy) || errors.Is(err, sshx.ErrSessionNotFound) {
		outcome.Status, outcome.Error = "failed", err.Error()
		outcome.DurationMs = time.Since(started).Milliseconds()
		s.finishLog(ctx, logID, outcome)
		return outcome, err
	}
	outcome.DurationMs = time.Since(started).Milliseconds()
	outcome.Stdout, outcome.Stderr = string(result.Stdout), string(result.Stderr)
	outcome.StdoutBytes, outcome.StderrBytes, outcome.Truncated = result.StdoutBytes, result.StderrBytes, result.Truncated
	switch {
	case result.TimedOut:
		outcome.Status = "timeout"
		outcome.Error = fmt.Sprintf("命令超过 %d 秒未结束，已终止", options.TimeoutSeconds)
	case err != nil:
		outcome.Status, outcome.Error = "failed", err.Error()
	default:
		code := result.ExitCode
		outcome.ExitCode = &code
		if code == 0 {
			outcome.Status = "success"
		} else {
			outcome.Status = "failed"
		}
	}
	s.finishLog(ctx, logID, outcome)
	s.saveOutput(ctx, logID, outcome, settings.OutputRetentionDays)
	return outcome, nil
}

// saveOutput 加密保存 stdout/stderr（保留天数为 0 时跳过）。写失败只记日志，不影响命令结果。
func (s *Service) saveOutput(ctx context.Context, logID int64, outcome ExecOutcome, retentionDays int) {
	if retentionDays <= 0 || (outcome.Stdout == "" && outcome.Stderr == "") {
		return
	}
	encrypt := func(text string) any {
		if text == "" {
			return nil
		}
		encrypted, err := s.cipher.Encrypt(text)
		if err != nil {
			s.log.Error("encrypt ssh output", "log", logID, "error", err)
			return nil
		}
		return encrypted
	}
	if _, err := s.db.ExecContext(context.WithoutCancel(ctx), `INSERT OR REPLACE INTO ssh_exec_outputs(log_id,stdout_ciphertext,stderr_ciphertext,created_at) VALUES(?,?,?,?)`,
		logID, encrypt(outcome.Stdout), encrypt(outcome.Stderr), time.Now().UTC().UnixMilli()); err != nil {
		s.log.Error("save ssh exec output", "log", logID, "error", err)
	}
}

// loadOutput 读回并解密输出；没有存过（或已过保留期 / 保留天数为 0）返回 ok=false。
func (s *Service) loadOutput(ctx context.Context, logID int64) (stdout, stderr string, ok bool) {
	var stdoutCipher, stderrCipher []byte
	err := s.db.QueryRowContext(ctx, `SELECT stdout_ciphertext,stderr_ciphertext FROM ssh_exec_outputs WHERE log_id=?`, logID).Scan(&stdoutCipher, &stderrCipher)
	if err != nil {
		return "", "", false
	}
	decrypt := func(ciphertext []byte) string {
		if len(ciphertext) == 0 {
			return ""
		}
		text, err := s.cipher.Decrypt(ciphertext)
		if err != nil {
			s.log.Error("decrypt ssh output", "log", logID, "error", err)
			return ""
		}
		return text
	}
	return decrypt(stdoutCipher), decrypt(stderrCipher), true
}

// GetCommand 供 CLI 轮询与审计页查看：返回日志状态 + 已保存的输出。
// 路由已要求 system:ssh:exec 权限；能执行任意命令的人读别人的输出不构成提权，故不再按发起人隔离。
func (s *Service) GetCommand(ctx context.Context, logID int64) (ExecOutcome, error) {
	entry, err := s.GetLog(ctx, logID)
	if err != nil {
		return ExecOutcome{}, err
	}
	outcome := ExecOutcome{ID: entry.ID, Status: entry.Status, ExitCode: entry.ExitCode, StdoutBytes: entry.StdoutBytes, StderrBytes: entry.StderrBytes, Truncated: entry.Truncated, DurationMs: entry.DurationMs, Error: entry.Error, DynamicReason: entry.Options.DynamicReason, Dynamic: entry.Options.DynamicReason != ""}
	if entry.Status == "blocked" {
		outcome.BlockedReason = entry.Error
	}
	stdout, stderr, ok := s.loadOutput(ctx, logID)
	switch {
	case ok:
		outcome.Stdout, outcome.Stderr = stdout, stderr
	case entry.Kind == "exec" && (entry.StdoutBytes > 0 || entry.StderrBytes > 0):
		// 有输出字节数却读不到内容：保留期已过，或当时设置为不保存
		outcome.OutputExpired = true
	}
	return outcome, nil
}

// Approve 批准待批操作。exec 类在后台异步执行（批准者的请求不等待）；传输 / 会话类只改状态，由发起方再次调用完成。
func (s *Service) Approve(ctx context.Context, approverID, logID int64) (ExecLog, error) {
	entry, err := s.GetLog(ctx, logID)
	if err != nil {
		return ExecLog{}, err
	}
	if entry.Status != "pending" {
		return ExecLog{}, fmt.Errorf("%w：该操作当前状态为 %s", ErrConflict, entry.Status)
	}
	now := time.Now().UTC().UnixMilli()
	nextStatus := "approved"
	if entry.Kind == "exec" {
		nextStatus = "running"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE ssh_exec_logs SET status=?,approved_by=?,approved_at=? WHERE id=? AND status='pending'`, nextStatus, approverID, now, logID)
	if err != nil {
		return ExecLog{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ExecLog{}, ErrConflict
	}
	if entry.Kind == "exec" {
		settings, err := s.GetSettings(ctx)
		if err != nil {
			return ExecLog{}, err
		}
		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					s.log.Error("approved ssh exec panicked", "log", logID, "panic", recovered)
				}
			}()
			if _, err := s.runExec(context.Background(), entry.ConnectionID, logID, entry.Command, entry.Options, settings); err != nil {
				// 批准时会话已被关闭 / 正忙：审计行已记 failed，这里只留日志
				s.log.Warn("approved ssh exec not delivered", "log", logID, "error", err)
			}
		}()
	}
	return s.GetLog(ctx, logID)
}

// Reject 拒绝待批操作。
func (s *Service) Reject(ctx context.Context, approverID, logID int64) (ExecLog, error) {
	now := time.Now().UTC().UnixMilli()
	result, err := s.db.ExecContext(ctx, `UPDATE ssh_exec_logs SET status='rejected',approved_by=?,approved_at=?,finished_at=?,error='已被拒绝' WHERE id=? AND status='pending'`, approverID, now, now, logID)
	if err != nil {
		return ExecLog{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		if _, err := s.GetLog(ctx, logID); err != nil {
			return ExecLog{}, err
		}
		return ExecLog{}, fmt.Errorf("%w：该操作已不在待批状态", ErrConflict)
	}
	return s.GetLog(ctx, logID)
}

// ---- 日志读写 ----

func (s *Service) insertLog(ctx context.Context, connectionID, userID int64, kind, command, status string, options LogOptions, errText string, finished bool) (int64, error) {
	now := time.Now().UTC().UnixMilli()
	raw, _ := json.Marshal(options)
	var finishedAt any
	if finished {
		finishedAt = now
	}
	result, err := s.db.ExecContext(context.WithoutCancel(ctx), `INSERT INTO ssh_exec_logs(connection_id,user_id,kind,command,status,error,options_json,created_at,finished_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		connectionID, nullableID(userID), kind, command, status, errText, string(raw), now, finishedAt)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Service) finishLog(ctx context.Context, logID int64, outcome ExecOutcome) {
	var exitCode any
	if outcome.ExitCode != nil {
		exitCode = *outcome.ExitCode
	}
	if _, err := s.db.ExecContext(context.WithoutCancel(ctx), `UPDATE ssh_exec_logs SET status=?,exit_code=?,stdout_bytes=?,stderr_bytes=?,truncated=?,duration_ms=?,error=?,finished_at=? WHERE id=?`,
		outcome.Status, exitCode, outcome.StdoutBytes, outcome.StderrBytes, outcome.Truncated, outcome.DurationMs, outcome.Error, time.Now().UTC().UnixMilli(), logID); err != nil {
		s.log.Error("finish ssh exec log", "log", logID, "error", err)
	}
}

// 本地单用户：没有 users 表，username / approvedByName 恒为空串（占位保持列数与 scanLog 一致）。
const logColumns = `l.id,l.connection_id,COALESCE(c.name,''),l.user_id,'',l.kind,l.command,l.status,l.exit_code,l.stdout_bytes,l.stderr_bytes,l.truncated,l.duration_ms,l.error,l.options_json,l.approved_by,'',l.approved_at,l.created_at,l.finished_at`
const logJoins = ` FROM ssh_exec_logs l LEFT JOIN ssh_connections c ON c.id=l.connection_id`

func scanLog(row interface{ Scan(...any) error }) (ExecLog, error) {
	var entry ExecLog
	var userID, exitCode, approvedBy, approvedAt, finishedAt sql.NullInt64
	var options string
	var created int64
	if err := row.Scan(&entry.ID, &entry.ConnectionID, &entry.ConnectionName, &userID, &entry.Username, &entry.Kind, &entry.Command, &entry.Status, &exitCode, &entry.StdoutBytes, &entry.StderrBytes, &entry.Truncated, &entry.DurationMs, &entry.Error, &options, &approvedBy, &entry.ApprovedByName, &approvedAt, &created, &finishedAt); err != nil {
		return entry, err
	}
	if userID.Valid {
		entry.UserID = &userID.Int64
	}
	if exitCode.Valid {
		code := int(exitCode.Int64)
		entry.ExitCode = &code
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

// LogFilter 是日志列表的过滤条件。
type LogFilter struct {
	ConnectionID int64
	Status       string
	Kind         string
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
	if filter.Kind != "" {
		where = append(where, "l.kind=?")
		args = append(args, filter.Kind)
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
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ssh_exec_logs l WHERE `+clause, args...).Scan(&page.Total); err != nil {
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

// Cleanup 删除 retention 之前的日志、按设置清理过期输出、回收过期待批项（超过 24 小时未处理的 pending 置为 rejected）。
func (s *Service) Cleanup(ctx context.Context, retention time.Duration) error {
	now := time.Now().UTC()
	if retention > 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM ssh_exec_logs WHERE created_at<? AND status NOT IN ('pending','running')`, now.Add(-retention).UnixMilli()); err != nil {
			return err
		}
	}
	// 输出保留期通常远短于日志保留期：日志留 90 天可追溯「谁执行了什么」，输出只留几天
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return err
	}
	if settings.OutputRetentionDays <= 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM ssh_exec_outputs`); err != nil {
			return err
		}
	} else {
		cutoff := now.AddDate(0, 0, -settings.OutputRetentionDays).UnixMilli()
		if _, err := s.db.ExecContext(ctx, `DELETE FROM ssh_exec_outputs WHERE created_at<?`, cutoff); err != nil {
			return err
		}
	}
	stale := now.Add(-24 * time.Hour).UnixMilli()
	if _, err := s.db.ExecContext(ctx, `UPDATE ssh_exec_logs SET status='rejected',error='超过 24 小时未批准，已自动作废',finished_at=? WHERE status IN ('pending','approved') AND created_at<?`, now.UnixMilli(), stale); err != nil {
		return err
	}
	// 服务重启 / 客户端消失导致的孤儿 running：按最后活动时间（分片心跳）判断，避免误伤仍在推进的大文件传输
	_, err = s.db.ExecContext(ctx, `UPDATE ssh_exec_logs SET status='failed',error='执行状态丢失（服务重启或客户端中断）',finished_at=? WHERE status='running' AND COALESCE(heartbeat_at, created_at)<?`, now.UnixMilli(), now.Add(-time.Hour).UnixMilli())
	return err
}

// IsNotFound 判断错误是否为记录不存在。
func IsNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }
