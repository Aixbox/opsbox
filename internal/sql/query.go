package sql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"opsbox/internal/platform/console"
	"opsbox/internal/platform/opscheck"
	"opsbox/internal/platform/security"
	"opsbox/internal/platform/sqlx"
)

// maxQueryLength 与 SSH 命令上限一致；待批语句要按原文执行，库里存全文（列表页展示时截断）。
const maxQueryLength = 8000

// QueryOutcome 是 query / 轮询查询状态的统一响应。同步执行的读语句带 Columns/Rows；
// 执行完成后结果加密落库（保留天数见设置），CLI 断线后轮询这里可重取，过期只剩元数据（outputExpired）。
type QueryOutcome struct {
	ID            int64    `json:"id"`
	Status        string   `json:"status"`
	Kind          string   `json:"kind"`
	Columns       []string `json:"columns,omitempty"`
	Rows          [][]any  `json:"rows,omitempty"`
	RowsReturned  int      `json:"rowsReturned"`
	RowsAffected  int64    `json:"rowsAffected"`
	Truncated     bool     `json:"truncated"`
	DurationMs    int64    `json:"durationMs"`
	Error         string   `json:"error,omitempty"`
	BlockedRule   string   `json:"blockedRule,omitempty"`
	BlockedReason string   `json:"blockedReason,omitempty"`
	// OutputExpired 表示查询已完成但结果已过保存期（或设置为不保存），只剩元数据
	OutputExpired bool `json:"outputExpired,omitempty"`
}

// QueryRequest 是 POST query 的请求体。
type QueryRequest struct {
	SQL            string `json:"sql"`
	Tx             bool   `json:"tx"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
	// ConfirmToken 是审批预检令牌：CLI 提交需审批写操作时必须先取得（check 接口或
	// CHECK_REQUIRED 响应下发），Web 端人工操作不需要。
	ConfirmToken string `json:"confirmToken"`
	// Source 标记来源（cli / web），只用于控制台面板的展示区分，由 handler 按认证方式填充。
	Source string `json:"-"`
}

// LogOptions 是待批写操作随日志行携带的执行参数（批准时据此执行）。
type LogOptions struct {
	Tx             bool `json:"tx,omitempty"`
	TimeoutSeconds int  `json:"timeoutSeconds,omitempty"`
	MaxRows        int  `json:"maxRows,omitempty"`
}

// QueryLog 是审计日志行（只有元数据：语句文本、类别、状态、行数、耗时；不落结果数据）。
type QueryLog struct {
	ID             int64      `json:"id"`
	ConnectionID   int64      `json:"connectionId"`
	ConnectionName string     `json:"connectionName"`
	UserID         *int64     `json:"userId"`
	Username       string     `json:"username"`
	Kind           string     `json:"kind"`
	Statements     string     `json:"statements"`
	Status         string     `json:"status"`
	RowsReturned   int        `json:"rowsReturned"`
	RowsAffected   int64      `json:"rowsAffected"`
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

// Query 执行 SQL：切分分类 → 黑名单 → 会话门禁 → 写策略路由（读直执行；写按 readonly 拒 / confirm 入队 / allow 放行）
// → 审计 → 广播到控制台面板。
//
// 会话门禁与 SSH / Redis 同一口径：该连接必须有已打开的控制台会话，CLI 才能操作；
// 结果广播给会话，正在看面板的人能实时看到 AI 跑了什么 SQL、返回了什么。
//
// 黑名单排在门禁之前：危险语句即便在没有会话时提交，也要留下 blocked 审计记录。
func (s *Service) Query(ctx context.Context, userID, connectionID int64, request QueryRequest) (QueryOutcome, error) {
	request.SQL = strings.TrimSpace(request.SQL)
	if request.SQL == "" || len(request.SQL) > maxQueryLength {
		return QueryOutcome{}, invalid(fmt.Sprintf("SQL 不能为空且不能超过 %d 个字符", maxQueryLength))
	}
	conn, target, err := s.resolve(ctx, connectionID)
	if err != nil {
		return QueryOutcome{}, err
	}
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return QueryOutcome{}, err
	}
	engine := sqlx.Engine(conn.Engine)
	statements := sqlx.Split(engine, request.SQL)
	if len(statements) == 0 {
		return QueryOutcome{}, invalid("没有可执行的语句")
	}
	kind := sqlx.Classify(statements)
	// 黑名单全策略生效
	if rule, hit := sqlx.NewBlacklist(settings.BlockedPatterns).Check(statements); hit {
		reason := fmt.Sprintf("命中黑名单 [%s]：%s", rule, sqlx.Reason(rule))
		id, err := s.insertLog(ctx, connectionID, userID, string(kind), request.SQL, "blocked", LogOptions{}, reason, true)
		if err != nil {
			return QueryOutcome{}, err
		}
		outcome := QueryOutcome{ID: id, Status: "blocked", Kind: string(kind), BlockedRule: rule, BlockedReason: sqlx.Reason(rule), Error: reason}
		s.broadcastOutcome(connectionID, userID, request.Source, request.SQL, outcome)
		return outcome, nil
	}
	// 会话门禁：没有已打开的控制台会话就拒绝——不留任何绕过路径
	if err := s.requireOpenSession(connectionID, userID); err != nil {
		return QueryOutcome{}, err
	}
	options := LogOptions{Tx: request.Tx, TimeoutSeconds: request.TimeoutSeconds}
	if options.TimeoutSeconds <= 0 {
		options.TimeoutSeconds = settings.QueryTimeoutSeconds
	}
	if options.TimeoutSeconds > 300 {
		return QueryOutcome{}, invalid("超时不能超过 300 秒")
	}
	options.MaxRows = settings.MaxRows
	// 写操作按连接的三档策略路由；读操作恒直执行
	if kind == sqlx.KindWrite {
		switch conn.WritePolicy {
		case "readonly":
			id, err := s.insertLog(ctx, connectionID, userID, string(kind), request.SQL, "rejected", options, "连接为只读策略，拒绝执行写操作", true)
			if err != nil {
				return QueryOutcome{}, err
			}
			outcome := QueryOutcome{ID: id, Status: "rejected", Kind: string(kind), Error: "连接为只读策略，拒绝执行写操作"}
			s.broadcastOutcome(connectionID, userID, request.Source, request.SQL, outcome)
			return outcome, nil
		case "confirm":
			// 审批预检卡口：CLI 来源的需审批写操作必须先取得用户确认（预检令牌）；
			// Web 端是人工提交，用户本来就在场，不做强制。
			if request.Source == "cli" && !opscheck.VerifyToken(s.cipher, checkScope, connectionID, request.SQL, request.ConfirmToken) {
				return QueryOutcome{}, &opscheck.RequiredError{Check: approvalCheck(connectionID, request.SQL, s.cipher)}
			}
			id, err := s.insertLog(ctx, connectionID, userID, string(kind), request.SQL, "pending", options, "", false)
			if err != nil {
				return QueryOutcome{}, err
			}
			outcome := QueryOutcome{ID: id, Status: "pending", Kind: string(kind)}
			s.broadcastOutcome(connectionID, userID, request.Source, request.SQL, outcome)
			return outcome, nil
		}
	}
	id, err := s.insertLog(ctx, connectionID, userID, string(kind), request.SQL, "running", options, "", false)
	if err != nil {
		return QueryOutcome{}, err
	}
	// 脱离请求上下文的 2s 全局死线，由查询自身超时控制
	execCtx := context.WithoutCancel(ctx)
	outcome := s.runQuery(execCtx, connectionID, id, target, statements, options, settings.OutputRetentionDays)
	outcome.Kind = string(kind)
	s.broadcastOutcome(connectionID, userID, request.Source, request.SQL, outcome)
	return outcome, nil
}

// checkScope 是 sql 模块预检令牌的域隔离前缀。
const checkScope = "sql-check"

// approvalCheck 组装 action=approval 的预检结果（含确定性令牌与指引）。
func approvalCheck(connectionID int64, statement string, cipher *security.TokenCipher) opscheck.Outcome {
	reason := "连接为 confirm 策略，写操作需人工批准"
	return opscheck.Outcome{
		Action:   opscheck.ActionApproval,
		Reason:   reason,
		Token:    opscheck.Token(cipher, checkScope, connectionID, statement),
		Guidance: opscheck.GuidanceFor(opscheck.ActionApproval, "sqlctl", "数据库 → 待批准写操作", reason),
	}
}

// CheckQuery 预检一段 SQL 是否需要平台审批：无副作用（不建审计记录、不占 SQL ID、
// 不碰会话门禁、不连目标库），与 Query 走同一套黑名单与写策略判定。
func (s *Service) CheckQuery(ctx context.Context, connectionID int64, request QueryRequest) (opscheck.Outcome, error) {
	request.SQL = strings.TrimSpace(request.SQL)
	if request.SQL == "" || len(request.SQL) > maxQueryLength {
		return opscheck.Outcome{}, invalid(fmt.Sprintf("SQL 不能为空且不能超过 %d 个字符", maxQueryLength))
	}
	conn, _, err := s.resolve(ctx, connectionID)
	if err != nil {
		return opscheck.Outcome{}, err
	}
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return opscheck.Outcome{}, err
	}
	engine := sqlx.Engine(conn.Engine)
	statements := sqlx.Split(engine, request.SQL)
	if len(statements) == 0 {
		return opscheck.Outcome{}, invalid("没有可执行的语句")
	}
	kind := sqlx.Classify(statements)
	if rule, hit := sqlx.NewBlacklist(settings.BlockedPatterns).Check(statements); hit {
		reason := fmt.Sprintf("命中黑名单 [%s]：%s", rule, sqlx.Reason(rule))
		return opscheck.Outcome{Action: opscheck.ActionBlocked, Reason: reason, Guidance: opscheck.GuidanceFor(opscheck.ActionBlocked, "sqlctl", "", reason)}, nil
	}
	if kind == sqlx.KindWrite {
		switch conn.WritePolicy {
		case "readonly":
			reason := "连接为只读策略，写操作一律拒绝"
			return opscheck.Outcome{Action: opscheck.ActionBlocked, Reason: reason, Guidance: opscheck.GuidanceFor(opscheck.ActionBlocked, "sqlctl", "", reason)}, nil
		case "confirm":
			return approvalCheck(connectionID, request.SQL, s.cipher), nil
		}
	}
	return opscheck.Outcome{Action: opscheck.ActionExecute, Guidance: opscheck.GuidanceFor(opscheck.ActionExecute, "sqlctl", "", "")}, nil
}

// broadcastOutcome 把一次查询的结果推给该连接上打开着的控制台面板。
func (s *Service) broadcastOutcome(connectionID, userID int64, source, statement string, outcome QueryOutcome) {
	if source == "" {
		source = "cli"
	}
	s.broadcast(connectionID, userID, console.Event{
		Type: "query", Source: source, LogID: outcome.ID, Command: statement,
		Status: outcome.Status, Columns: outcome.Columns, Rows: outcome.Rows,
		Error: outcome.Error, DurationMs: outcome.DurationMs, Kind: outcome.Kind,
	})
}

// runQuery 执行并收尾日志；有行数据时按保留天数加密落库，供 CLI 断线后重取与审计回溯。
func (s *Service) runQuery(ctx context.Context, connectionID, logID int64, target sqlx.Target, statements []sqlx.Statement, options LogOptions, retentionDays int) QueryOutcome {
	outcome := QueryOutcome{ID: logID}
	started := time.Now()
	result, err := s.pools.Run(ctx, connectionID, target, statements, sqlx.Options{
		Timeout: time.Duration(options.TimeoutSeconds) * time.Second,
		MaxRows: options.MaxRows,
		Tx:      options.Tx,
	})
	outcome.DurationMs = time.Since(started).Milliseconds()
	outcome.Columns, outcome.Rows = result.Columns, result.Rows
	outcome.RowsReturned, outcome.RowsAffected, outcome.Truncated = result.RowsReturned, result.RowsAffected, result.Truncated
	switch {
	case errors.Is(err, sqlx.ErrTimeout):
		outcome.Status = "timeout"
		outcome.Error = fmt.Sprintf("查询超过 %d 秒未结束，已终止", options.TimeoutSeconds)
	case err != nil:
		outcome.Status, outcome.Error = "failed", err.Error()
	default:
		outcome.Status = "success"
	}
	s.finishLog(ctx, logID, outcome)
	s.saveOutput(ctx, logID, outcome, retentionDays)
	return outcome
}

// saveOutput 加密保存查询结果（保留天数为 0 或没有行数据时跳过）。写失败只记日志，不影响查询结果。
func (s *Service) saveOutput(ctx context.Context, logID int64, outcome QueryOutcome, retentionDays int) {
	if retentionDays <= 0 || len(outcome.Rows) == 0 {
		return
	}
	payload, err := json.Marshal(savedResult{Columns: outcome.Columns, Rows: outcome.Rows, Truncated: outcome.Truncated})
	if err != nil {
		s.log.Error("marshal sql result", "log", logID, "error", err)
		return
	}
	ciphertext, err := s.cipher.Encrypt(string(payload))
	if err != nil {
		s.log.Error("encrypt sql result", "log", logID, "error", err)
		return
	}
	if _, err := s.db.ExecContext(context.WithoutCancel(ctx), `INSERT OR REPLACE INTO sql_query_outputs(log_id,result_ciphertext,created_at) VALUES(?,?,?)`,
		logID, ciphertext, time.Now().UTC().UnixMilli()); err != nil {
		s.log.Error("save sql query output", "log", logID, "error", err)
	}
}

// savedResult 是落库的结果载荷（JSON 后整体加密）。
type savedResult struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	Truncated bool     `json:"truncated"`
}

// loadOutput 读回并解密结果；没有存过（或已过保留期 / 保留天数为 0）返回 ok=false。
// UseNumber 让数字保持原文（json.Number），避免大整数经 JSON 往返后以科学计数法展示。
func (s *Service) loadOutput(ctx context.Context, logID int64) (savedResult, bool) {
	var ciphertext []byte
	if err := s.db.QueryRowContext(ctx, `SELECT result_ciphertext FROM sql_query_outputs WHERE log_id=?`, logID).Scan(&ciphertext); err != nil {
		return savedResult{}, false
	}
	payload, err := s.cipher.Decrypt(ciphertext)
	if err != nil {
		s.log.Error("decrypt sql result", "log", logID, "error", err)
		return savedResult{}, false
	}
	var saved savedResult
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&saved); err != nil {
		s.log.Error("unmarshal sql result", "log", logID, "error", err)
		return savedResult{}, false
	}
	return saved, true
}

// GetQuery 供 CLI 轮询与审计页查看：返回查询状态；已完成且结果尚在保存期时附带 Columns/Rows，
// 发起方断线后轮询这里即可重取完整结果。路由已要求 system:sql:exec 权限。
func (s *Service) GetQuery(ctx context.Context, logID int64) (QueryOutcome, error) {
	entry, err := s.GetLog(ctx, logID)
	if err != nil {
		return QueryOutcome{}, err
	}
	outcome := QueryOutcome{
		ID: entry.ID, Status: entry.Status, Kind: entry.Kind,
		RowsReturned: entry.RowsReturned, RowsAffected: entry.RowsAffected, Truncated: entry.Truncated,
		DurationMs: entry.DurationMs, Error: entry.Error,
	}
	if entry.Status == "blocked" {
		outcome.BlockedReason = entry.Error
	}
	saved, ok := s.loadOutput(ctx, logID)
	switch {
	case ok:
		outcome.Columns, outcome.Rows, outcome.Truncated = saved.Columns, saved.Rows, saved.Truncated
		outcome.RowsReturned = len(saved.Rows)
	case entry.Kind == "read" && (entry.RowsReturned > 0 || entry.Truncated):
		// 有行数却读不到结果：保存期已过，或当时设置为不保存
		outcome.OutputExpired = true
	}
	return outcome, nil
}

// Approve 批准待批写操作：状态置 running 后在后台异步执行（批准者的请求不等待）。
func (s *Service) Approve(ctx context.Context, approverID, logID int64) (QueryLog, error) {
	entry, err := s.GetLog(ctx, logID)
	if err != nil {
		return QueryLog{}, err
	}
	if entry.Status != "pending" {
		return QueryLog{}, fmt.Errorf("%w：该操作当前状态为 %s", ErrConflict, entry.Status)
	}
	if entry.Kind != "write" {
		return QueryLog{}, fmt.Errorf("%w：只有写操作需要批准", ErrConflict)
	}
	now := time.Now().UTC().UnixMilli()
	result, err := s.db.ExecContext(ctx, `UPDATE sql_exec_logs SET status='running',approved_by=?,approved_at=? WHERE id=? AND status='pending'`, approverID, now, logID)
	if err != nil {
		return QueryLog{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return QueryLog{}, ErrConflict
	}
	_, target, err := s.resolve(ctx, entry.ConnectionID)
	if err != nil {
		return QueryLog{}, err
	}
	engine, err := s.connectionEngine(ctx, entry.ConnectionID)
	if err != nil {
		return QueryLog{}, err
	}
	statements := sqlx.Split(engine, entry.Statements)
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return QueryLog{}, err
	}
	if entry.Options.TimeoutSeconds <= 0 {
		entry.Options.TimeoutSeconds = settings.QueryTimeoutSeconds
	}
	if entry.Options.MaxRows <= 0 {
		entry.Options.MaxRows = settings.MaxRows
	}
	statement := entry.Statements
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.Error("approved sql query panicked", "log", logID, "panic", recovered)
			}
		}()
		outcome := s.runQuery(context.Background(), entry.ConnectionID, logID, target, statements, entry.Options, settings.OutputRetentionDays)
		outcome.Kind = entry.Kind
		// 批准后异步执行：结果推给该连接上所有打开的面板（批准者未必是发起人）
		s.sessions.BroadcastConnection(entry.ConnectionID, console.Event{
			Type: "query", Source: "approved", LogID: logID, Command: statement,
			Status: outcome.Status, Columns: outcome.Columns, Rows: outcome.Rows,
			Error: outcome.Error, DurationMs: outcome.DurationMs, Kind: outcome.Kind,
		})
	}()
	return s.GetLog(ctx, logID)
}

// connectionEngine 读取连接的引擎类型。
func (s *Service) connectionEngine(ctx context.Context, connectionID int64) (sqlx.Engine, error) {
	var engine string
	if err := s.db.QueryRowContext(ctx, `SELECT engine FROM sql_connections WHERE id=?`, connectionID).Scan(&engine); err != nil {
		return "", err
	}
	return sqlx.Engine(engine), nil
}

// Reject 拒绝待批操作。
func (s *Service) Reject(ctx context.Context, approverID, logID int64) (QueryLog, error) {
	entry, err := s.GetLog(ctx, logID)
	if err != nil {
		return QueryLog{}, err
	}
	now := time.Now().UTC().UnixMilli()
	result, err := s.db.ExecContext(ctx, `UPDATE sql_exec_logs SET status='rejected',approved_by=?,approved_at=?,finished_at=?,error='已被拒绝' WHERE id=? AND status='pending'`, approverID, now, now, logID)
	if err != nil {
		return QueryLog{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		if _, err := s.GetLog(ctx, logID); err != nil {
			return QueryLog{}, err
		}
		return QueryLog{}, fmt.Errorf("%w：该操作已不在待批状态", ErrConflict)
	}
	s.sessions.BroadcastConnection(entry.ConnectionID, console.Event{
		Type: "query", Source: "approved", LogID: logID, Command: entry.Statements,
		Status: "rejected", Error: "已被拒绝", Kind: entry.Kind,
	})
	return s.GetLog(ctx, logID)
}

// ---- 日志读写 ----

func (s *Service) insertLog(ctx context.Context, connectionID, userID int64, kind, statements, status string, options LogOptions, errText string, finished bool) (int64, error) {
	now := time.Now().UTC().UnixMilli()
	raw, _ := json.Marshal(options)
	var finishedAt any
	if finished {
		finishedAt = now
	}
	result, err := s.db.ExecContext(context.WithoutCancel(ctx), `INSERT INTO sql_exec_logs(connection_id,user_id,kind,statements,status,error,options_json,created_at,finished_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		connectionID, nullableID(userID), kind, statements, status, errText, string(raw), now, finishedAt)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Service) finishLog(ctx context.Context, logID int64, outcome QueryOutcome) {
	if _, err := s.db.ExecContext(context.WithoutCancel(ctx), `UPDATE sql_exec_logs SET status=?,rows_returned=?,rows_affected=?,truncated=?,duration_ms=?,error=?,finished_at=? WHERE id=?`,
		outcome.Status, outcome.RowsReturned, outcome.RowsAffected, outcome.Truncated, outcome.DurationMs, outcome.Error, time.Now().UTC().UnixMilli(), logID); err != nil {
		s.log.Error("finish sql query log", "log", logID, "error", err)
	}
}

// 本地单用户：没有 users 表，username / approvedByName 恒为空串（占位保持列数与 scanLog 一致）。
const logColumns = `l.id,l.connection_id,COALESCE(c.name,''),l.user_id,'',l.kind,l.statements,l.status,l.rows_returned,l.rows_affected,l.truncated,l.duration_ms,l.error,l.options_json,l.approved_by,'',l.approved_at,l.created_at,l.finished_at`
const logJoins = ` FROM sql_exec_logs l LEFT JOIN sql_connections c ON c.id=l.connection_id`

func scanLog(row interface{ Scan(...any) error }) (QueryLog, error) {
	var entry QueryLog
	var userID, approvedBy, approvedAt, finishedAt sql.NullInt64
	var options string
	var created int64
	if err := row.Scan(&entry.ID, &entry.ConnectionID, &entry.ConnectionName, &userID, &entry.Username, &entry.Kind, &entry.Statements, &entry.Status, &entry.RowsReturned, &entry.RowsAffected, &entry.Truncated, &entry.DurationMs, &entry.Error, &options, &approvedBy, &entry.ApprovedByName, &approvedAt, &created, &finishedAt); err != nil {
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

func (s *Service) GetLog(ctx context.Context, id int64) (QueryLog, error) {
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

func (s *Service) ListLogs(ctx context.Context, filter LogFilter) (Page[QueryLog], error) {
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
	page := Page[QueryLog]{Items: []QueryLog{}, Page: filter.Page, PageSize: filter.PageSize}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sql_exec_logs l WHERE `+clause, args...).Scan(&page.Total); err != nil {
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

// ListPending 返回待批写操作队列（按时间正序）。
func (s *Service) ListPending(ctx context.Context) ([]QueryLog, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+logColumns+logJoins+` WHERE l.status='pending' ORDER BY l.id ASC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []QueryLog{}
	for rows.Next() {
		entry, err := scanLog(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, entry)
	}
	return items, rows.Err()
}

// Cleanup 删除 retention 之前的日志、按设置清理过期结果、回收过期待批项（超过 24 小时未处理的
// pending 置为 rejected）与服务重启遗留的孤儿 running。
func (s *Service) Cleanup(ctx context.Context, retention time.Duration) error {
	now := time.Now().UTC()
	if retention > 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM sql_exec_logs WHERE created_at<? AND status NOT IN ('pending','running')`, now.Add(-retention).UnixMilli()); err != nil {
			return err
		}
	}
	// 结果保存期通常远短于日志保留期：日志留 90 天可追溯「谁执行了什么」，结果只留几天
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return err
	}
	if settings.OutputRetentionDays <= 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM sql_query_outputs`); err != nil {
			return err
		}
	} else {
		cutoff := now.AddDate(0, 0, -settings.OutputRetentionDays).UnixMilli()
		if _, err := s.db.ExecContext(ctx, `DELETE FROM sql_query_outputs WHERE created_at<?`, cutoff); err != nil {
			return err
		}
	}
	stale := now.Add(-24 * time.Hour).UnixMilli()
	if _, err := s.db.ExecContext(ctx, `UPDATE sql_exec_logs SET status='rejected',error='超过 24 小时未批准，已自动作废',finished_at=? WHERE status='pending' AND created_at<?`, now.UnixMilli(), stale); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE sql_exec_logs SET status='failed',error='执行状态丢失（服务重启或客户端中断）',finished_at=? WHERE status='running' AND created_at<?`, now.UnixMilli(), now.Add(-time.Hour).UnixMilli())
	return err
}
