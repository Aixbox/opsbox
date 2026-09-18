// Package sql 是 SQL AI 运维模块：平台托管 MySQL / PostgreSQL 连接、三档写策略
// （readonly / confirm / allow）、待批写操作队列、自省与查询审计。
// 凭证 AES-256-GCM 加密落库，永不下发；所有对目标实例的访问都在服务端完成。
package sql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"opsbox/internal/platform/console"
	"opsbox/internal/platform/security"
	"opsbox/internal/platform/sqlx"
)

var (
	ErrNotFound  = sql.ErrNoRows
	ErrInvalid   = errors.New("无效的数据库请求")
	ErrDisabled  = errors.New("连接已停用")
	ErrConflict  = errors.New("操作状态冲突")
	ErrForbidden = errors.New("无权访问该记录")
	// ErrNoOpenSession 表示该连接没有已打开的控制台会话。与 SSH / Redis 同一口径：
	// 会话打开即授权、关闭即失权，「配置过」本身不构成操作许可。
	ErrNoOpenSession = errors.New("该连接没有已打开的控制台会话")
)

func invalid(message string) error { return fmt.Errorf("%w：%s", ErrInvalid, message) }

// Connection 是连接的对外视图，不含密码明文。
type Connection struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Engine      string    `json:"engine"`
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	Username    string    `json:"username"`
	HasPassword bool      `json:"hasPassword"`
	Database    string    `json:"database"`
	Params      string    `json:"params"`
	WritePolicy string    `json:"writePolicy"`
	Enabled     bool      `json:"enabled"`
	Remark      string    `json:"remark"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ConnectionInput 是创建 / 更新连接的请求体。密码为 nil 表示「更新时保持不变」，空串表示清空。
type ConnectionInput struct {
	Name        string  `json:"name"`
	Engine      string  `json:"engine"`
	Host        string  `json:"host"`
	Port        int     `json:"port"`
	Username    string  `json:"username"`
	Password    *string `json:"password"`
	Database    string  `json:"database"`
	Params      string  `json:"params"`
	WritePolicy string  `json:"writePolicy"`
	Enabled     *bool   `json:"enabled"`
	Remark      string  `json:"remark"`
}

// Settings 是全局设置（单例行）。
type Settings struct {
	MaxRows             int      `json:"maxRows"`
	QueryTimeoutSeconds int      `json:"queryTimeoutSeconds"`
	BlockedPatterns     []string `json:"blockedPatterns"`
	// OutputRetentionDays 为查询结果（行数据）的加密保存天数；0 时不保存结果（只留元数据）。
	OutputRetentionDays int       `json:"outputRetentionDays"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

// Page 是分页结果。
type Page[T any] struct {
	Items    []T   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"pageSize"`
}

// PoolManager 是执行层抽象（按连接解析目标并执行语句），可注入假实现做单测。
type PoolManager interface {
	DB(ctx context.Context, id int64, target sqlx.Target) (*sql.DB, error)
	Run(ctx context.Context, id int64, target sqlx.Target, statements []sqlx.Statement, opts sqlx.Options) (sqlx.Result, error)
	Close(id int64)
}

// poolManager 是 PoolManager 的默认实现，封装 sqlx.Pool。
type poolManager struct{ pools *sqlx.Pool }

func (m poolManager) DB(ctx context.Context, id int64, target sqlx.Target) (*sql.DB, error) {
	return m.pools.DB(ctx, id, target)
}

func (m poolManager) Run(ctx context.Context, id int64, target sqlx.Target, statements []sqlx.Statement, opts sqlx.Options) (sqlx.Result, error) {
	db, err := m.pools.DB(ctx, id, target)
	if err != nil {
		return sqlx.Result{}, err
	}
	return sqlx.Run(ctx, db, statements, opts)
}

func (m poolManager) Close(id int64) { m.pools.Invalidate(id) }

// Service 承载全部业务逻辑。
type Service struct {
	db       *sql.DB
	cipher   *security.TokenCipher
	pools    PoolManager
	sessions *console.Manager
	tickets  *console.TicketStore
	log      *slog.Logger
}

// sessionIdle 是控制台会话的回收阈值：无人接入且没有任何操作超过这么久才收掉。
// 会话是持久的（关掉面板不结束），阈值给得宽一些；CLI 仍在用的会话会被操作广播续命。
const sessionIdle = time.Hour

func NewService(db *sql.DB, cipher *security.TokenCipher, pools PoolManager) *Service {
	if pools == nil {
		pools = &poolManager{pools: sqlx.NewPool()}
	}
	return &Service{
		db: db, cipher: cipher, pools: pools,
		sessions: console.NewManager("sql", sessionIdle),
		tickets:  console.NewTicketStore(60 * time.Second),
		log:      slog.Default(),
	}
}

// SetLogger 注入日志器。
func (s *Service) SetLogger(log *slog.Logger) {
	if log != nil {
		s.log = log
	}
}

// Run 运行空闲连接池回收、控制台会话回收与票据清理协程直到 ctx 结束
// （默认实现带回收；注入的假实现没有就不跑）。
func (s *Service) Run(ctx context.Context) {
	go s.tickets.Run(ctx)
	go s.sessions.Run(ctx)
	if manager, ok := s.pools.(*poolManager); ok {
		manager.pools.Run(ctx)
	}
}

// ---- 连接 ----

const connectionColumns = `id,name,engine,host,port,username,password_ciphertext IS NOT NULL,database,params,write_policy,enabled,remark,created_at,updated_at`

func scanConnection(row interface{ Scan(...any) error }) (Connection, error) {
	var c Connection
	var created, updated int64
	if err := row.Scan(&c.ID, &c.Name, &c.Engine, &c.Host, &c.Port, &c.Username, &c.HasPassword, &c.Database, &c.Params, &c.WritePolicy, &c.Enabled, &c.Remark, &created, &updated); err != nil {
		return c, err
	}
	c.CreatedAt, c.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	return c, nil
}

func (s *Service) ListConnections(ctx context.Context) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+connectionColumns+` FROM sql_connections ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Connection{}
	for rows.Next() {
		item, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) GetConnection(ctx context.Context, id int64) (Connection, error) {
	return scanConnection(s.db.QueryRowContext(ctx, `SELECT `+connectionColumns+` FROM sql_connections WHERE id=?`, id))
}

func validateConnection(input *ConnectionInput, creating bool) error {
	input.Name = strings.TrimSpace(input.Name)
	input.Host = strings.TrimSpace(input.Host)
	input.Username = strings.TrimSpace(input.Username)
	input.Database = strings.TrimSpace(input.Database)
	input.Remark = strings.TrimSpace(input.Remark)
	input.Params = strings.TrimSpace(input.Params)
	if input.Name == "" || len([]rune(input.Name)) > 100 {
		return invalid("名称不能为空且不能超过 100 个字符")
	}
	if strings.ContainsAny(input.Name, " \t/\\") {
		return invalid("名称不能包含空格或斜杠（CLI 用名称引用连接）")
	}
	engine := sqlx.Engine(input.Engine)
	if !engine.Valid() {
		return invalid("引擎必须是 mysql 或 postgres")
	}
	if input.Host == "" {
		return invalid("主机不能为空")
	}
	if strings.ContainsAny(input.Host, " /") || net.ParseIP(input.Host) == nil && !hostnamePattern(input.Host) {
		return invalid("主机格式无效")
	}
	defaultPort := 3306
	if engine == sqlx.EnginePostgres {
		defaultPort = 5432
	}
	if input.Port == 0 {
		input.Port = defaultPort
	}
	if input.Port < 1 || input.Port > 65535 {
		return invalid("端口必须在 1-65535 之间")
	}
	if input.Username == "" || len(input.Username) > 64 {
		return invalid("用户名不能为空且不能超过 64 个字符")
	}
	if input.Database == "" || len(input.Database) > 64 {
		return invalid("数据库名不能为空且不能超过 64 个字符")
	}
	if input.WritePolicy == "" {
		input.WritePolicy = "confirm"
	}
	if input.WritePolicy != "readonly" && input.WritePolicy != "confirm" && input.WritePolicy != "allow" {
		return invalid("写策略必须是 readonly、confirm 或 allow")
	}
	if creating && input.Password == nil || creating && *input.Password == "" && engine == sqlx.EngineMySQL {
		return invalid("MySQL 连接需要填写密码（PostgreSQL 留空表示免密认证）")
	}
	if input.Params != "" {
		var decoded map[string]string
		if err := json.Unmarshal([]byte(input.Params), &decoded); err != nil {
			return invalid("扩展参数必须是 JSON 对象，如 {\"sslmode\":\"require\"}")
		}
		normalized, _ := json.Marshal(decoded)
		input.Params = string(normalized)
	}
	if len([]rune(input.Remark)) > 500 {
		return invalid("备注不能超过 500 个字符")
	}
	return nil
}

func hostnamePattern(host string) bool {
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return false
			}
		}
	}
	return true
}

func (s *Service) CreateConnection(ctx context.Context, userID int64, input ConnectionInput) (Connection, error) {
	if err := validateConnection(&input, true); err != nil {
		return Connection{}, err
	}
	password, err := s.encryptOptional(input.Password)
	if err != nil {
		return Connection{}, err
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := time.Now().UTC().UnixMilli()
	result, err := s.db.ExecContext(ctx, `INSERT INTO sql_connections(name,engine,host,port,username,password_ciphertext,database,params,write_policy,enabled,remark,created_by,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		input.Name, input.Engine, input.Host, input.Port, input.Username, password, input.Database, input.Params, input.WritePolicy, enabled, input.Remark, nullableID(userID), now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Connection{}, invalid("名称已存在")
		}
		return Connection{}, err
	}
	id, _ := result.LastInsertId()
	return s.GetConnection(ctx, id)
}

func (s *Service) UpdateConnection(ctx context.Context, id int64, input ConnectionInput) (Connection, error) {
	if err := validateConnection(&input, false); err != nil {
		return Connection{}, err
	}
	if _, err := s.GetConnection(ctx, id); err != nil {
		return Connection{}, err
	}
	sets := []string{"name=?", "engine=?", "host=?", "port=?", "username=?", "database=?", "params=?", "write_policy=?", "remark=?", "updated_at=?"}
	args := []any{input.Name, input.Engine, input.Host, input.Port, input.Username, input.Database, input.Params, input.WritePolicy, input.Remark, time.Now().UTC().UnixMilli()}
	if input.Password != nil {
		encrypted, err := s.encryptOptional(input.Password)
		if err != nil {
			return Connection{}, err
		}
		sets = append(sets, "password_ciphertext=?")
		args = append(args, encrypted)
	}
	if input.Enabled != nil {
		sets = append(sets, "enabled=?")
		args = append(args, *input.Enabled)
	}
	args = append(args, id)
	if _, err := s.db.ExecContext(ctx, `UPDATE sql_connections SET `+strings.Join(sets, ",")+` WHERE id=?`, args...); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Connection{}, invalid("名称已存在")
		}
		return Connection{}, err
	}
	// 配置可能已变化：关闭旧连接池，下次执行按新配置重建
	s.pools.Close(id)
	updated, err := s.GetConnection(ctx, id)
	if err != nil {
		return Connection{}, err
	}
	if updated.Engine == string(sqlx.EngineMySQL) && !updated.HasPassword {
		return Connection{}, invalid("MySQL 连接需要填写密码")
	}
	return updated, nil
}

func (s *Service) DeleteConnection(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sql_connections WHERE id=?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	s.pools.Close(id)
	return nil
}

// TestConnection 用 SELECT 1（MySQL）/ 同语义常量查询（PG）验证凭证与网络可达性。
func (s *Service) TestConnection(ctx context.Context, id int64) (map[string]any, error) {
	conn, target, err := s.target(ctx, id)
	if err != nil {
		return nil, err
	}
	testCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	db, err := s.pools.DB(testCtx, id, target)
	if err != nil {
		return nil, err
	}
	var one int
	if err := db.QueryRowContext(testCtx, `SELECT 1`).Scan(&one); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "engine": conn.Engine, "database": conn.Database}, nil
}

// target 解密密码并组装连接目标。
func (s *Service) target(ctx context.Context, id int64) (Connection, sqlx.Target, error) {
	conn, err := s.GetConnection(ctx, id)
	if err != nil {
		return Connection{}, sqlx.Target{}, err
	}
	var password []byte
	if err := s.db.QueryRowContext(ctx, `SELECT password_ciphertext FROM sql_connections WHERE id=?`, id).Scan(&password); err != nil {
		return Connection{}, sqlx.Target{}, err
	}
	var passwordPlain string
	if len(password) > 0 {
		if passwordPlain, err = s.cipher.Decrypt(password); err != nil {
			return Connection{}, sqlx.Target{}, err
		}
	}
	params := map[string]string{}
	if conn.Params != "" {
		_ = json.Unmarshal([]byte(conn.Params), &params)
	}
	target := sqlx.Target{
		Engine: sqlx.Engine(conn.Engine), Host: conn.Host, Port: conn.Port,
		Username: conn.Username, Password: passwordPlain, Database: conn.Database, Params: params,
	}
	return conn, target, nil
}

// resolve 加载连接并校验启用状态（执行入口共用）。
func (s *Service) resolve(ctx context.Context, id int64) (Connection, sqlx.Target, error) {
	conn, target, err := s.target(ctx, id)
	if err != nil {
		return Connection{}, sqlx.Target{}, err
	}
	if !conn.Enabled {
		return conn, target, ErrDisabled
	}
	return conn, target, nil
}

func (s *Service) encryptOptional(value *string) (any, error) {
	if value == nil || *value == "" {
		return nil, nil
	}
	encrypted, err := s.cipher.Encrypt(*value)
	if err != nil {
		return nil, err
	}
	return encrypted, nil
}

// ---- 设置 ----

func (s *Service) GetSettings(ctx context.Context) (Settings, error) {
	var settings Settings
	var patterns string
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT max_rows,query_timeout_seconds,blocked_patterns,output_retention_days,updated_at FROM sql_settings WHERE id=1`).
		Scan(&settings.MaxRows, &settings.QueryTimeoutSeconds, &patterns, &settings.OutputRetentionDays, &updated)
	if err != nil {
		return Settings{}, err
	}
	if err := json.Unmarshal([]byte(patterns), &settings.BlockedPatterns); err != nil || settings.BlockedPatterns == nil {
		settings.BlockedPatterns = []string{}
	}
	settings.UpdatedAt = time.UnixMilli(updated).UTC()
	return settings, nil
}

func (s *Service) UpdateSettings(ctx context.Context, input Settings) (Settings, error) {
	cleaned := make([]string, 0, len(input.BlockedPatterns))
	for _, pattern := range input.BlockedPatterns {
		if pattern = strings.TrimSpace(pattern); pattern == "" {
			continue
		}
		if !sqlx.ValidRule(pattern) {
			return Settings{}, invalid("未知黑名单规则 " + pattern)
		}
		cleaned = append(cleaned, pattern)
	}
	if input.MaxRows < 1 || input.MaxRows > 10000 {
		return Settings{}, invalid("行数上限必须在 1-10000 之间")
	}
	if input.QueryTimeoutSeconds < 1 || input.QueryTimeoutSeconds > 300 {
		return Settings{}, invalid("查询超时必须在 1-300 秒之间")
	}
	if input.OutputRetentionDays < 0 || input.OutputRetentionDays > 90 {
		return Settings{}, invalid("结果保留天数必须在 0-90 之间（0 表示不保存结果）")
	}
	raw, _ := json.Marshal(cleaned)
	if _, err := s.db.ExecContext(ctx, `UPDATE sql_settings SET max_rows=?,query_timeout_seconds=?,blocked_patterns=?,output_retention_days=?,updated_at=? WHERE id=1`,
		input.MaxRows, input.QueryTimeoutSeconds, string(raw), input.OutputRetentionDays, time.Now().UTC().UnixMilli()); err != nil {
		return Settings{}, err
	}
	return s.GetSettings(ctx)
}

func nullableID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}
