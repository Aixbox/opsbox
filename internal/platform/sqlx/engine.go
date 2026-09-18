// Package sqlx 是 SQL AI 运维的平台侧引擎抽象：MySQL / PostgreSQL 的 DSN 构建、
// 语句切分与分类、结构化黑名单、连接池管理与限幅执行。所有对目标实例的访问都经过这里。
package sqlx

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Engine 是支持的数据库引擎。
type Engine string

const (
	EngineMySQL    Engine = "mysql"
	EnginePostgres Engine = "postgres"
)

func (e Engine) Valid() bool { return e == EngineMySQL || e == EnginePostgres }

// Target 是一次连接所需的全部信息（密码已解密）。
type Target struct {
	Engine   Engine
	Host     string
	Port     int
	Username string
	Password string
	Database string
	// Params 是连接级扩展参数（charset / sslmode 等），键必须是白名单内的。
	Params map[string]string
}

// DialTimeout 是建立连接的死线；查询超时由 Run 控制。
const DialTimeout = 10 * time.Second

// mysqlParams / postgresParams 是允许用户自定义的连接参数白名单，其余一律拒绝，
// 避免借 params 注入 DSN 语义（如 MySQL 的 multiStatements、allowAllFiles）。
var (
	mysqlParams = map[string]bool{
		"charset": true, "collation": true, "tls": true, "loc": true,
		"time_zone": true, "interpolateParams": true, "maxAllowedPacket": true,
		"readTimeout": true, "writeTimeout": true,
	}
	postgresParams = map[string]bool{
		"sslmode": true, "connect_timeout": true, "application_name": true,
		"search_path": true, "timezone": true, "statement_timeout": true, "options": true,
	}
)

// DSN 返回 database/sql 驱动名与数据源字符串。参数白名单校验失败返回错误。
func (t Target) DSN() (driver string, dsn string, err error) {
	for key := range t.Params {
		if t.Engine == EngineMySQL && !mysqlParams[key] {
			return "", "", fmt.Errorf("MySQL 连接不支持参数 %q（可用：%s）", key, paramNames(mysqlParams))
		}
		if t.Engine == EnginePostgres && !postgresParams[key] {
			return "", "", fmt.Errorf("PostgreSQL 连接不支持参数 %q（可用：%s）", key, paramNames(postgresParams))
		}
	}
	switch t.Engine {
	case EngineMySQL:
		return "mysql", t.mysqlDSN(), nil
	case EnginePostgres:
		return "pgx", t.postgresDSN(), nil
	default:
		return "", "", errors.New("不支持的数据库引擎")
	}
}

// mysqlDSN 用 go-sql-driver 的 Config 结构拼 DSN，由库负责转义（密码含 @ / : / 空格等也安全）。
func (t Target) mysqlDSN() string {
	cfg := mysql.NewConfig()
	cfg.User = t.Username
	cfg.Passwd = t.Password
	cfg.Net = "tcp"
	cfg.Addr = fmt.Sprintf("%s:%d", t.Host, t.Port)
	cfg.DBName = t.Database
	cfg.Timeout = DialTimeout
	cfg.Params = map[string]string{}
	// 默认 utf8mb4；多语句不走 DSN 开关——服务端逐条执行，事务用 BEGIN/COMMIT 包裹
	cfg.Params["charset"] = "utf8mb4"
	for key, value := range t.Params {
		cfg.Params[key] = value
	}
	if t.Params["collation"] != "" {
		cfg.Collation = t.Params["collation"]
	}
	if t.Params["tls"] != "" {
		cfg.TLSConfig = t.Params["tls"]
	}
	return cfg.FormatDSN()
}

// postgresDSN 用 net/url 拼 libpq 连接串，UserInfo 编码由标准库负责。
func (t Target) postgresDSN() string {
	endpoint := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(t.Username, t.Password),
		Host:   fmt.Sprintf("%s:%d", t.Host, t.Port),
		Path:   "/" + t.Database,
	}
	query := url.Values{}
	for key, value := range t.Params {
		query.Set(key, value)
	}
	if query.Get("sslmode") == "" {
		// 默认禁用 SSL：自建内网实例大多没有证书；需要加密时在连接参数里显式配置
		query.Set("sslmode", "disable")
	}
	endpoint.RawQuery = query.Encode()
	return endpoint.String()
}

func paramNames(set map[string]bool) string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}
