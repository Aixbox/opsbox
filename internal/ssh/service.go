// Package ssh 是 SSH AI 运维模块：连接管理、命令执行（审计 / 审批双模式）、SFTP 传输、PTY 会话与执行审计。
// 凭证 AES-256-GCM 加密落库，永不下发；所有远端操作都在服务端完成。
package ssh

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
	"opsbox/internal/platform/sshx"
)

var (
	ErrNotFound  = sql.ErrNoRows
	ErrInvalid   = errors.New("无效的 SSH 请求")
	ErrDisabled  = errors.New("连接已停用")
	ErrConflict  = errors.New("操作状态冲突")
	ErrForbidden = errors.New("无权访问该记录")
	// ErrNoOpenSession 表示该连接没有已打开的终端会话。CLI 的操作一律要求会话存在：
	// 会话打开即授权、关闭即失权，「配置过」本身不构成操作许可。
	ErrNoOpenSession = errors.New("该连接没有已打开的终端会话")
)

func invalid(message string) error { return fmt.Errorf("%w：%s", ErrInvalid, message) }

// Connection 是连接的对外视图，不含任何凭证明文。
type Connection struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Host          string    `json:"host"`
	Port          int       `json:"port"`
	Username      string    `json:"username"`
	AuthType      string    `json:"authType"`
	HasPassword   bool      `json:"hasPassword"`
	HasPrivateKey bool      `json:"hasPrivateKey"`
	HasPassphrase bool      `json:"hasPassphrase"`
	HostKey       string    `json:"hostKey"`
	ExecPolicy    string    `json:"execPolicy"`
	Enabled       bool      `json:"enabled"`
	Remark        string    `json:"remark"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// ConnectionInput 是创建 / 更新连接的请求体。凭证字段为 nil 表示「更新时保持不变」，空串表示清空。
type ConnectionInput struct {
	Name       string  `json:"name"`
	Host       string  `json:"host"`
	Port       int     `json:"port"`
	Username   string  `json:"username"`
	AuthType   string  `json:"authType"`
	Password   *string `json:"password"`
	PrivateKey *string `json:"privateKey"`
	Passphrase *string `json:"passphrase"`
	// ResetHostKey 为 true 时清空已记录指纹（服务器重装后重新 TOFU）
	ResetHostKey bool   `json:"resetHostKey"`
	ExecPolicy   string `json:"execPolicy"`
	Enabled      *bool  `json:"enabled"`
	Remark       string `json:"remark"`
}

// Settings 是全局设置（单例行）。
type Settings struct {
	BlacklistPatterns       []string `json:"blacklistPatterns"`
	ExecTimeoutSeconds      int      `json:"execTimeoutSeconds"`
	OutputLimitBytes        int      `json:"outputLimitBytes"`
	DynamicRequiresApproval bool     `json:"dynamicRequiresApproval"`
	// OutputRetentionDays 为 0 时不保存命令输出（只留元数据）。
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

// Service 承载全部业务逻辑；dialer 可注入假实现做单测。
type Service struct {
	db       *sql.DB
	cipher   *security.TokenCipher
	dialer   sshx.Dialer
	sessions *sshx.SessionManager
	tickets  *console.TicketStore
	log      *slog.Logger
}

// sessionIdle 是终端会话的回收阈值：无人接入且没有任何 IO 超过这么久才收掉。
// 会话是持久的（关掉面板不结束），阈值给得宽一些，用户离开一会儿回来还能接上；
// CLI 仍在用的会话会被 IO 续命，不受影响。
const sessionIdle = time.Hour

func NewService(db *sql.DB, cipher *security.TokenCipher, dialer sshx.Dialer) *Service {
	if dialer == nil {
		dialer = sshx.NetDialer{}
	}
	s := &Service{
		db: db, cipher: cipher, dialer: dialer,
		tickets: console.NewTicketStore(60 * time.Second),
		log:     slog.Default(),
	}
	s.sessions = sshx.NewSessionManager(sessionIdle, s.onSessionClosed)
	return s
}

// SetLogger 注入日志器。
func (s *Service) SetLogger(log *slog.Logger) {
	if log != nil {
		s.log = log
	}
}

// Run 运行后台协程（会话闲置回收、票据清理）直到 ctx 结束。
func (s *Service) Run(ctx context.Context) {
	go s.tickets.Run(ctx)
	s.sessions.Run(ctx)
}

// ---- 连接 ----

const connectionColumns = `id,name,host,port,username,auth_type,password_ciphertext IS NOT NULL,private_key_ciphertext IS NOT NULL,key_passphrase_ciphertext IS NOT NULL,COALESCE(host_key,''),exec_policy,enabled,remark,created_at,updated_at`

func scanConnection(row interface{ Scan(...any) error }) (Connection, error) {
	var c Connection
	var created, updated int64
	if err := row.Scan(&c.ID, &c.Name, &c.Host, &c.Port, &c.Username, &c.AuthType, &c.HasPassword, &c.HasPrivateKey, &c.HasPassphrase, &c.HostKey, &c.ExecPolicy, &c.Enabled, &c.Remark, &created, &updated); err != nil {
		return c, err
	}
	c.CreatedAt, c.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	return c, nil
}

func (s *Service) ListConnections(ctx context.Context) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+connectionColumns+` FROM ssh_connections ORDER BY name`)
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
	return scanConnection(s.db.QueryRowContext(ctx, `SELECT `+connectionColumns+` FROM ssh_connections WHERE id=?`, id))
}

func validateConnection(input *ConnectionInput, creating bool) error {
	input.Name = strings.TrimSpace(input.Name)
	input.Host = strings.TrimSpace(input.Host)
	input.Username = strings.TrimSpace(input.Username)
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
		input.Port = 22
	}
	if input.Port < 1 || input.Port > 65535 {
		return invalid("端口必须在 1-65535 之间")
	}
	if input.Username == "" || len(input.Username) > 64 {
		return invalid("用户名不能为空且不能超过 64 个字符")
	}
	if input.AuthType != "password" && input.AuthType != "key" {
		return invalid("认证方式必须是 password 或 key")
	}
	if input.ExecPolicy == "" {
		input.ExecPolicy = "audit"
	}
	if input.ExecPolicy != "audit" && input.ExecPolicy != "approve" {
		return invalid("执行策略必须是 audit 或 approve")
	}
	if creating {
		if input.AuthType == "password" && (input.Password == nil || *input.Password == "") {
			return invalid("密码认证需要填写密码")
		}
		if input.AuthType == "key" && (input.PrivateKey == nil || strings.TrimSpace(*input.PrivateKey) == "") {
			return invalid("私钥认证需要填写私钥")
		}
	}
	if input.PrivateKey != nil && len(*input.PrivateKey) > 16*1024 {
		return invalid("私钥过长")
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

func (s *Service) CreateConnection(ctx context.Context, userID int64, input ConnectionInput) (Connection, error) {
	if err := validateConnection(&input, true); err != nil {
		return Connection{}, err
	}
	password, err := s.encryptOptional(input.Password)
	if err != nil {
		return Connection{}, err
	}
	privateKey, err := s.encryptOptional(input.PrivateKey)
	if err != nil {
		return Connection{}, err
	}
	passphrase, err := s.encryptOptional(input.Passphrase)
	if err != nil {
		return Connection{}, err
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := time.Now().UTC().UnixMilli()
	result, err := s.db.ExecContext(ctx, `INSERT INTO ssh_connections(name,host,port,username,auth_type,password_ciphertext,private_key_ciphertext,key_passphrase_ciphertext,exec_policy,enabled,remark,created_by,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		input.Name, input.Host, input.Port, input.Username, input.AuthType, password, privateKey, passphrase, input.ExecPolicy, enabled, input.Remark, nullableID(userID), now, now)
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
	existing, err := s.GetConnection(ctx, id)
	if err != nil {
		return Connection{}, err
	}
	// 凭证：nil 保持不变，"" 清空，其余覆盖
	sets := []string{"name=?", "host=?", "port=?", "username=?", "auth_type=?", "exec_policy=?", "remark=?", "updated_at=?"}
	args := []any{input.Name, input.Host, input.Port, input.Username, input.AuthType, input.ExecPolicy, input.Remark, time.Now().UTC().UnixMilli()}
	for _, secret := range []struct {
		column string
		value  *string
	}{{"password_ciphertext", input.Password}, {"private_key_ciphertext", input.PrivateKey}, {"key_passphrase_ciphertext", input.Passphrase}} {
		if secret.value == nil {
			continue
		}
		encrypted, err := s.encryptOptional(secret.value)
		if err != nil {
			return Connection{}, err
		}
		sets = append(sets, secret.column+"=?")
		args = append(args, encrypted)
	}
	if input.Enabled != nil {
		sets = append(sets, "enabled=?")
		args = append(args, *input.Enabled)
	}
	// 主机 / 端口变了或显式要求重记时清空指纹
	if input.ResetHostKey || existing.Host != input.Host || existing.Port != input.Port {
		sets = append(sets, "host_key=NULL")
	}
	args = append(args, id)
	if _, err := s.db.ExecContext(ctx, `UPDATE ssh_connections SET `+strings.Join(sets, ",")+` WHERE id=?`, args...); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Connection{}, invalid("名称已存在")
		}
		return Connection{}, err
	}
	// 凭证 / 认证方式完整性：更新后必须仍有可用凭证
	updated, err := s.GetConnection(ctx, id)
	if err != nil {
		return Connection{}, err
	}
	if updated.AuthType == "password" && !updated.HasPassword {
		return Connection{}, invalid("密码认证需要填写密码")
	}
	if updated.AuthType == "key" && !updated.HasPrivateKey {
		return Connection{}, invalid("私钥认证需要填写私钥")
	}
	return updated, nil
}

func (s *Service) DeleteConnection(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM ssh_connections WHERE id=?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// target 解密凭证、组装拨号参数。
func (s *Service) target(ctx context.Context, id int64) (Connection, sshx.Target, error) {
	conn, err := s.GetConnection(ctx, id)
	if err != nil {
		return Connection{}, sshx.Target{}, err
	}
	var password, privateKey, passphrase []byte
	if err := s.db.QueryRowContext(ctx, `SELECT password_ciphertext,private_key_ciphertext,key_passphrase_ciphertext FROM ssh_connections WHERE id=?`, id).Scan(&password, &privateKey, &passphrase); err != nil {
		return Connection{}, sshx.Target{}, err
	}
	target := sshx.Target{Host: conn.Host, Port: conn.Port, Username: conn.Username, AuthType: conn.AuthType, HostKey: conn.HostKey, Timeout: 10 * time.Second}
	decrypt := func(ciphertext []byte) (string, error) {
		if len(ciphertext) == 0 {
			return "", nil
		}
		return s.cipher.Decrypt(ciphertext)
	}
	if target.Password, err = decrypt(password); err != nil {
		return Connection{}, sshx.Target{}, err
	}
	if target.PrivateKey, err = decrypt(privateKey); err != nil {
		return Connection{}, sshx.Target{}, err
	}
	if target.Passphrase, err = decrypt(passphrase); err != nil {
		return Connection{}, sshx.Target{}, err
	}
	return conn, target, nil
}

// dial 拨号并在首连时记录 host key（TOFU）。
func (s *Service) dial(ctx context.Context, id int64) (Connection, *sshx.Client, error) {
	conn, target, err := s.target(ctx, id)
	if err != nil {
		return Connection{}, nil, err
	}
	if !conn.Enabled {
		return conn, nil, ErrDisabled
	}
	client, err := s.dialer.Dial(ctx, target)
	if err != nil {
		return conn, nil, err
	}
	if conn.HostKey == "" && client.HostKey != "" {
		if _, err := s.db.ExecContext(context.WithoutCancel(ctx), `UPDATE ssh_connections SET host_key=? WHERE id=? AND host_key IS NULL`, client.HostKey, id); err != nil {
			s.log.Warn("record ssh host key", "connection", id, "error", err)
		}
		conn.HostKey = client.HostKey
	}
	return conn, client, nil
}

// TestConnection 拨号验证凭证并记录 host key；返回远端 `uname -a` 的首行便于确认身份。
func (s *Service) TestConnection(ctx context.Context, id int64) (map[string]any, error) {
	conn, target, err := s.target(ctx, id)
	if err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	client, err := s.dialer.Dial(dialCtx, target)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	if conn.HostKey == "" && client.HostKey != "" {
		if _, err := s.db.ExecContext(dialCtx, `UPDATE ssh_connections SET host_key=?,updated_at=? WHERE id=? AND host_key IS NULL`, client.HostKey, time.Now().UTC().UnixMilli(), id); err != nil {
			return nil, err
		}
	}
	result, err := sshx.RunExec(dialCtx, client, "uname -a", sshx.ExecOptions{OutputLimit: 4096})
	uname := ""
	if err == nil {
		uname = strings.TrimSpace(string(result.Stdout))
	}
	return map[string]any{"ok": true, "hostKey": client.HostKey, "uname": uname}, nil
}

// TestTarget 用表单当前参数拨号测试（添加 / 编辑抽屉的「测试连接」按钮）。
// fromID>0 表示编辑场景：凭证字段未填（nil）时逐字段回退到已保存凭证，其余字段一律以表单为准。
// 与 TestConnection 不同：只在内存里拨号验证，不落库、不记录 host key——测试不应产生副作用。
func (s *Service) TestTarget(ctx context.Context, fromID int64, input ConnectionInput) (map[string]any, error) {
	var saved [3]string // password / privateKey / passphrase 的已保存明文（惰性解密）
	if fromID > 0 && (input.Password == nil || input.PrivateKey == nil || input.Passphrase == nil) {
		var password, privateKey, passphrase []byte
		if err := s.db.QueryRowContext(ctx, `SELECT password_ciphertext,private_key_ciphertext,key_passphrase_ciphertext FROM ssh_connections WHERE id=?`, fromID).Scan(&password, &privateKey, &passphrase); err != nil {
			return nil, err
		}
		decrypt := func(ciphertext []byte) (string, error) {
			if len(ciphertext) == 0 {
				return "", nil
			}
			return s.cipher.Decrypt(ciphertext)
		}
		var err error
		if saved[0], err = decrypt(password); err != nil {
			return nil, err
		}
		if saved[1], err = decrypt(privateKey); err != nil {
			return nil, err
		}
		if saved[2], err = decrypt(passphrase); err != nil {
			return nil, err
		}
	}
	// nil = 表单里留空（编辑时沿用已保存的），空串 = 显式清空
	effective := func(value *string, index int) string {
		if value != nil {
			return *value
		}
		return saved[index]
	}
	target := sshx.Target{
		Host: input.Host, Port: input.Port, Username: input.Username, AuthType: input.AuthType,
		Password:   effective(input.Password, 0),
		PrivateKey: effective(input.PrivateKey, 1),
		Passphrase: effective(input.Passphrase, 2),
		Timeout:    10 * time.Second,
	}
	// 凭证缺失时提前给出可操作的原因（authMethods 的裸错误会被 fail() 归为笼统的 500）
	switch target.AuthType {
	case "password":
		if target.Password == "" {
			return nil, invalid("密码认证需要填写密码")
		}
	case "key":
		if target.PrivateKey == "" {
			return nil, invalid("私钥认证需要粘贴私钥")
		}
	default:
		return nil, invalid("不支持的认证方式 " + target.AuthType)
	}
	dialCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	client, err := s.dialer.Dial(dialCtx, target)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	result, err := sshx.RunExec(dialCtx, client, "uname -a", sshx.ExecOptions{OutputLimit: 4096})
	uname := ""
	if err == nil {
		uname = strings.TrimSpace(string(result.Stdout))
	}
	return map[string]any{"ok": true, "hostKey": client.HostKey, "uname": uname}, nil
}

// ---- 设置 ----

func (s *Service) GetSettings(ctx context.Context) (Settings, error) {
	var settings Settings
	var patterns string
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT blacklist_patterns,exec_timeout_seconds,output_limit_bytes,dynamic_requires_approval,output_retention_days,updated_at FROM ssh_settings WHERE id=1`).
		Scan(&patterns, &settings.ExecTimeoutSeconds, &settings.OutputLimitBytes, &settings.DynamicRequiresApproval, &settings.OutputRetentionDays, &updated)
	if err != nil {
		return Settings{}, err
	}
	if err := json.Unmarshal([]byte(patterns), &settings.BlacklistPatterns); err != nil || settings.BlacklistPatterns == nil {
		settings.BlacklistPatterns = []string{}
	}
	settings.UpdatedAt = time.UnixMilli(updated).UTC()
	return settings, nil
}

func (s *Service) UpdateSettings(ctx context.Context, input Settings) (Settings, error) {
	cleaned := make([]string, 0, len(input.BlacklistPatterns))
	for _, pattern := range input.BlacklistPatterns {
		if pattern = strings.TrimSpace(pattern); pattern != "" {
			cleaned = append(cleaned, pattern)
		}
	}
	if len(cleaned) > 200 {
		return Settings{}, invalid("黑名单规则不能超过 200 条")
	}
	if _, err := sshx.CompileBlacklist(cleaned); err != nil {
		return Settings{}, invalid(err.Error())
	}
	if input.ExecTimeoutSeconds < 1 || input.ExecTimeoutSeconds > 600 {
		return Settings{}, invalid("默认超时必须在 1-600 秒之间")
	}
	if input.OutputLimitBytes < 1024 || input.OutputLimitBytes > 8<<20 {
		return Settings{}, invalid("输出上限必须在 1KB-8MB 之间")
	}
	if input.OutputRetentionDays < 0 || input.OutputRetentionDays > 90 {
		return Settings{}, invalid("输出保留天数必须在 0-90 之间（0 表示不保存输出）")
	}
	raw, _ := json.Marshal(cleaned)
	if _, err := s.db.ExecContext(ctx, `UPDATE ssh_settings SET blacklist_patterns=?,exec_timeout_seconds=?,output_limit_bytes=?,dynamic_requires_approval=?,output_retention_days=?,updated_at=? WHERE id=1`,
		string(raw), input.ExecTimeoutSeconds, input.OutputLimitBytes, input.DynamicRequiresApproval, input.OutputRetentionDays, time.Now().UTC().UnixMilli()); err != nil {
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
