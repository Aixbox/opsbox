// drivers.go 注册 database/sql 驱动：Target.DSN() 返回的驱动名（mysql / pgx）必须已注册，
// 否则 sql.Open 报 unknown driver。pgx 的 stdlib 包以 "pgx" 名注册 libpq 风格连接串支持。
package sqlx

import _ "github.com/jackc/pgx/v5/stdlib"
