// Package redis 是 Redis AI 运维模块：连接管理、命令执行（三档写策略 + 审批队列）、安全 SCAN 与命令审计。
// 密码 AES-256-GCM 加密落库，永不下发；命令分类、策略判定与审计全部在服务端完成。
package redis

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

	"github.com/redis/go-redis/v9"

	"opsbox/internal/platform/console"
	"opsbox/internal/platform/redisx"
	"opsbox/internal/platform/security"
)

var (
	ErrNotFound  = sql.ErrNoRows
	ErrInvalid   = errors.New("无效的 Redis 请求")
	ErrDisabled  = errors.New("连接已停用")
	ErrConflict  = errors.New("操作状态冲突")
	ErrForbidden = errors.New("无权访问该记录")
	// ErrNoOpenSession 表示该连接没有已打开的控制台会话。与 SSH 同一口径：
	// 会话打开即授权、关闭即失权，「配置过」本身不构成操作许可。
	ErrNoOpenSession = errors.New("该连接没有已打开的控制台会话")
)

func invalid(message string) error { return fmt.Errorf("%w：%s", ErrInvalid, message) }

// 写策略：readonly 拒写、confirm 写进待批队列（默认）、allow 全放行。
const (
	PolicyReadonly = "readonly"
	PolicyConfirm  = "confirm"
	PolicyAllow    = "allow"
)

// Connection 是连接的对外视图，不含密码明文。
type Connection struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	HasPassword bool      `json:"hasPassword"`
	DB          int       `json:"db"`
	TLS         bool      `json:"tls"`
	WritePolicy string    `json:"writePolicy"`
	Enabled     bool      `json:"enabled"`
	Remark      string    `json:"remark"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ConnectionInput 是创建 / 更新连接的请求体。Password 为 nil 表示「更新时保持不变」，空串表示清空。
type ConnectionInput struct {
	Name        string  `json:"name"`
	Host        string  `json:"host"`
	Port        int     `json:"port"`
	Password    *string `json:"password"`
	DB          int     `json:"db"`
	TLS         bool    `json:"tls"`
	WritePolicy string  `json:"writePolicy"`
	Enabled     *bool   `json:"enabled"`
	Remark      string  `json:"remark"`
}

// Settings 是全局设置（单例行）。
type Settings struct {
	ScanKeyLimit       int      `json:"scanKeyLimit"`
	ValueTruncateBytes int      `json:"valueTruncateBytes"`
	BlockedCommands    []string `json:"blockedCommands"`
	// OutputRetentionDays 为命令回复加密落库的保留天数，0 表示不保存（只留元数据）。
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

// Service 承载全部业务逻辑。
type Service struct {
	db       *sql.DB
	cipher   *security.TokenCipher
	pool     *redisx.Pool
	sessions *console.Manager
	tickets  *console.TicketStore
	log      *slog.Logger
}

// sessionIdle 是控制台会话的回收阈值：无人接入且没有任何操作超过这么久才收掉。
// 会话是持久的（关掉面板不结束），阈值给得宽一些；CLI 仍在用的会话会被操作广播续命。
const sessionIdle = time.Hour

func NewService(db *sql.DB, cipher *security.TokenCipher) *Service {
	return &Service{
		db: db, cipher: cipher, pool: redisx.NewPool(),
		sessions: console.NewManager("redis", sessionIdle),
		tickets:  console.NewTicketStore(60 * time.Second),
		log:      slog.Default(),
	}
}

// SetLogger 注入日志器（同时配置给底层连接池）。
func (s *Service) SetLogger(log *slog.Logger) {
	if log != nil {
		s.log = log
		s.pool.SetLogger(log)
	}
}

// Run 运行连接池空闲回收、控制台会话回收与票据清理协程，直到 ctx 结束。
func (s *Service) Run(ctx context.Context) {
	go s.tickets.Run(ctx)
	go s.sessions.Run(ctx)
	s.pool.Run(ctx)
}

// Close 立即关闭全部客户端（连接删除后残留连接的兜底）。
func (s *Service) Close() {
	s.pool.CloseAll()
}

// ---- 连接 ----

const connectionColumns = `id,name,host,port,password_ciphertext IS NOT NULL,db,tls,write_policy,enabled,remark,created_at,updated_at`

func scanConnection(row interface{ Scan(...any) error }) (Connection, error) {
	var c Connection
	var created, updated int64
	if err := row.Scan(&c.ID, &c.Name, &c.Host, &c.Port, &c.HasPassword, &c.DB, &c.TLS, &c.WritePolicy, &c.Enabled, &c.Remark, &created, &updated); err != nil {
		return c, err
	}
	c.CreatedAt, c.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	return c, nil
}

func (s *Service) ListConnections(ctx context.Context) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+connectionColumns+` FROM redis_connections ORDER BY name`)
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
	return scanConnection(s.db.QueryRowContext(ctx, `SELECT `+connectionColumns+` FROM redis_connections WHERE id=?`, id))
}

func validateConnection(input *ConnectionInput, creating bool) error {
	input.Name = strings.TrimSpace(input.Name)
	input.Host = strings.TrimSpace(input.Host)
	input.Remark = strings.TrimSpace(input.Remark)
	if input.Name == "" || len([]rune(input.Name)) > 100 {
		return invalid("名称不能为空且不能超过 100 个字符")
	}
	if strings.ContainsAny(input.Name, " \t/\\") {
		return invalid("名称不能包含空格或斜杠（CLI 用名称引用连接）")
	}
	if input.Host == "" {
		return invalid("主机不能为空")
	}
	if strings.ContainsAny(input.Host, " /") || net.ParseIP(input.Host) == nil && !hostnamePattern(input.Host) {
		return invalid("主机格式无效")
	}
	if input.Port == 0 {
		input.Port = 6379
	}
	if input.Port < 1 || input.Port > 65535 {
		return invalid("端口必须在 1-65535 之间")
	}
	if input.DB < 0 || input.DB > 15 {
		return invalid("db 必须在 0-15 之间")
	}
	if input.WritePolicy == "" {
		input.WritePolicy = PolicyConfirm
	}
	if input.WritePolicy != PolicyReadonly && input.WritePolicy != PolicyConfirm && input.WritePolicy != PolicyAllow {
		return invalid("写策略必须是 readonly、confirm 或 allow")
	}
	if creating && input.Password != nil && len(*input.Password) > 1024 {
		return invalid("密码过长")
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
	result, err := s.db.ExecContext(ctx, `INSERT INTO redis_connections(name,host,port,password_ciphertext,db,tls,write_policy,enabled,remark,created_by,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		input.Name, input.Host, input.Port, password, input.DB, input.TLS, input.WritePolicy, enabled, input.Remark, nullableID(userID), now, now)
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
	// 密码：nil 保持不变，"" 清空，其余覆盖；变更后需要让池里的旧客户端失效
	sets := []string{"name=?", "host=?", "port=?", "db=?", "tls=?", "write_policy=?", "remark=?", "updated_at=?"}
	args := []any{input.Name, input.Host, input.Port, input.DB, input.TLS, input.WritePolicy, input.Remark, time.Now().UTC().UnixMilli()}
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
	if _, err := s.db.ExecContext(ctx, `UPDATE redis_connections SET `+strings.Join(sets, ",")+` WHERE id=?`, args...); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Connection{}, invalid("名称已存在")
		}
		return Connection{}, err
	}
	return s.GetConnection(ctx, id)
}

func (s *Service) DeleteConnection(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM redis_connections WHERE id=?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	// 日志按外键级联删除；连接没了，池里对应的客户端一并回收
	s.pool.Close(id)
	return nil
}

// target 读取密码密文并解密，组装客户端配置。
func (s *Service) target(ctx context.Context, id int64) (Connection, redisx.ClientConfig, error) {
	conn, err := s.GetConnection(ctx, id)
	if err != nil {
		return Connection{}, redisx.ClientConfig{}, err
	}
	var password []byte
	if err := s.db.QueryRowContext(ctx, `SELECT password_ciphertext FROM redis_connections WHERE id=?`, id).Scan(&password); err != nil {
		return Connection{}, redisx.ClientConfig{}, err
	}
	config := redisx.ClientConfig{Host: conn.Host, Port: conn.Port, DB: conn.DB, TLS: conn.TLS}
	if len(password) > 0 {
		if config.Password, err = s.cipher.Decrypt(password); err != nil {
			return Connection{}, redisx.ClientConfig{}, err
		}
	}
	return conn, config, nil
}

// client 取连接对应的客户端（池懒建，配置变更自动失效）。
func (s *Service) client(ctx context.Context, id int64) (Connection, *redisx.Handle, error) {
	conn, config, err := s.target(ctx, id)
	if err != nil {
		return Connection{}, nil, err
	}
	if !conn.Enabled {
		return conn, nil, ErrDisabled
	}
	handle, err := s.pool.Acquire(ctx, id, config, true)
	if err != nil {
		return conn, nil, err
	}
	return conn, handle, nil
}

// TestConnection 用 PING 验证连通性，顺带返回实例版本便于确认身份。
func (s *Service) TestConnection(ctx context.Context, id int64) (map[string]any, error) {
	conn, config, err := s.target(ctx, id)
	if err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	handle, err := s.pool.Acquire(dialCtx, id, config, false)
	if err != nil {
		return nil, err
	}
	if err := handle.Client().Ping(dialCtx).Err(); err != nil {
		return nil, err
	}
	version := ""
	if info, err := handle.Client().Do(dialCtx, "INFO", "server").Result(); err == nil {
		version = parseServerVersion(fmt.Sprint(info))
	}
	return map[string]any{"ok": true, "version": version, "db": conn.DB}, nil
}

// TestTarget 用表单当前参数 PING 测试（添加 / 编辑抽屉的「测试连接」按钮）。
// fromID>0 表示编辑场景：密码未填（nil）时回退到已保存密码，其余字段一律以表单为准；
// 密码显式传空串（前端「清除已保存的密码」）表示按无密码实例测试。
func (s *Service) TestTarget(ctx context.Context, fromID int64, input ConnectionInput) (map[string]any, error) {
	password := ""
	if input.Password != nil {
		password = *input.Password
	} else if fromID > 0 {
		var ciphertext []byte
		if err := s.db.QueryRowContext(ctx, `SELECT password_ciphertext FROM redis_connections WHERE id=?`, fromID).Scan(&ciphertext); err != nil {
			return nil, err
		}
		if len(ciphertext) > 0 {
			var err error
			if password, err = s.cipher.Decrypt(ciphertext); err != nil {
				return nil, err
			}
		}
	}
	config := redisx.ClientConfig{Host: input.Host, Port: input.Port, DB: input.DB, TLS: input.TLS, Password: password}
	// 一次性客户端，不进连接池：未保存的配置不能污染按连接 id 缓存的池
	client := redis.NewClient(config.Options())
	defer client.Close()
	dialCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := client.Ping(dialCtx).Err(); err != nil {
		return nil, err
	}
	version := ""
	if info, err := client.Do(dialCtx, "INFO", "server").Result(); err == nil {
		version = parseServerVersion(fmt.Sprint(info))
	}
	return map[string]any{"ok": true, "version": version, "db": input.DB}, nil
}

// parseServerVersion 从 INFO server 输出里取 redis_version / valkey_version。
func parseServerVersion(info string) string {
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(line, "redis_version:"); ok {
			return strings.TrimSpace(value)
		}
		if value, ok := strings.CutPrefix(line, "valkey_version:"); ok {
			return "valkey " + strings.TrimSpace(value)
		}
	}
	return ""
}

// ---- 设置 ----

// defaultBlockedCommands 与 migration 里的种子一致（设置被清空到非法值时的兜底）。
var defaultBlockedCommands = []string{"FLUSHALL", "FLUSHDB", "SHUTDOWN", "DEBUG", "MODULE", "REPLICAOF", "SLAVEOF", "SWAPDB", "CONFIG SET"}

func (s *Service) GetSettings(ctx context.Context) (Settings, error) {
	var settings Settings
	var blocked string
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT scan_key_limit,value_truncate_bytes,blocked_commands,output_retention_days,updated_at FROM redis_settings WHERE id=1`).
		Scan(&settings.ScanKeyLimit, &settings.ValueTruncateBytes, &blocked, &settings.OutputRetentionDays, &updated)
	if err != nil {
		return Settings{}, err
	}
	if err := json.Unmarshal([]byte(blocked), &settings.BlockedCommands); err != nil || settings.BlockedCommands == nil {
		settings.BlockedCommands = defaultBlockedCommands
	}
	settings.UpdatedAt = time.UnixMilli(updated).UTC()
	return settings, nil
}

func (s *Service) UpdateSettings(ctx context.Context, input Settings) (Settings, error) {
	if input.ScanKeyLimit < 1 || input.ScanKeyLimit > 100000 {
		return Settings{}, invalid("scan 上限必须在 1-100000 之间")
	}
	if input.ValueTruncateBytes < 16 || input.ValueTruncateBytes > 1<<20 {
		return Settings{}, invalid("单值截断阈值必须在 16B-1MB 之间")
	}
	if input.OutputRetentionDays < 0 || input.OutputRetentionDays > 90 {
		return Settings{}, invalid("回复保留天数必须在 0-90 之间（0 表示不保存回复）")
	}
	cleaned := make([]string, 0, len(input.BlockedCommands))
	for _, command := range input.BlockedCommands {
		command = strings.ToUpper(strings.TrimSpace(command))
		if command == "" {
			continue
		}
		if len(command) > 64 {
			return Settings{}, invalid("黑名单条目不能超过 64 个字符")
		}
		cleaned = append(cleaned, command)
	}
	if len(cleaned) > 200 {
		return Settings{}, invalid("黑名单条目不能超过 200 条")
	}
	raw, _ := json.Marshal(cleaned)
	if _, err := s.db.ExecContext(ctx, `UPDATE redis_settings SET scan_key_limit=?,value_truncate_bytes=?,blocked_commands=?,output_retention_days=?,updated_at=? WHERE id=1`,
		input.ScanKeyLimit, input.ValueTruncateBytes, string(raw), input.OutputRetentionDays, time.Now().UTC().UnixMilli()); err != nil {
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

// encryptOptional 密码可选：nil / 空串落 NULL，否则加密。
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
