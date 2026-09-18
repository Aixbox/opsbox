-- opsbox SQL 运维模块：由 personal-admin 的 00020 + 00021 合并而来。
-- 本地单用户版去掉 users / menus / permissions / app_meta；user_id、approved_by 仅保留列位（无外键），
-- settings 的 output_retention_days 直接并入建表语句（原 00021 为 ALTER TABLE）。

CREATE TABLE sql_connections (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 100),
    engine TEXT NOT NULL CHECK (engine IN ('mysql', 'postgres')),
    host TEXT NOT NULL,
    port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    username TEXT NOT NULL,
    password_ciphertext BLOB,
    database TEXT NOT NULL,
    -- 引擎扩展参数（charset / sslmode 等），JSON 对象
    params TEXT NOT NULL DEFAULT '{}',
    write_policy TEXT NOT NULL DEFAULT 'confirm' CHECK (write_policy IN ('readonly', 'confirm', 'allow')),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    remark TEXT NOT NULL DEFAULT '',
    created_by INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_sql_connections_name ON sql_connections(name);

CREATE TABLE sql_exec_logs (
    id INTEGER PRIMARY KEY,
    connection_id INTEGER NOT NULL REFERENCES sql_connections(id) ON DELETE CASCADE,
    user_id INTEGER,
    kind TEXT NOT NULL CHECK (kind IN ('read', 'write', 'schema')),
    statements TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'success', 'failed', 'timeout', 'blocked', 'rejected')),
    rows_returned INTEGER NOT NULL DEFAULT 0,
    rows_affected INTEGER NOT NULL DEFAULT 0,
    truncated INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0, 1)),
    duration_ms INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    -- 待批写操作的执行参数（tx / timeout / maxRows），批准时据此执行
    options_json TEXT NOT NULL DEFAULT '{}',
    approved_by INTEGER,
    approved_at INTEGER,
    created_at INTEGER NOT NULL,
    finished_at INTEGER
);
CREATE INDEX idx_sql_exec_logs_created ON sql_exec_logs(created_at DESC);
CREATE INDEX idx_sql_exec_logs_status ON sql_exec_logs(status, created_at DESC);
CREATE INDEX idx_sql_exec_logs_connection ON sql_exec_logs(connection_id, created_at DESC);

CREATE TABLE sql_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    max_rows INTEGER NOT NULL DEFAULT 500 CHECK (max_rows BETWEEN 1 AND 10000),
    query_timeout_seconds INTEGER NOT NULL DEFAULT 30 CHECK (query_timeout_seconds BETWEEN 1 AND 300),
    -- 结构化黑名单规则名数组：drop_database / drop_schema / full_table_write / truncate / drop_table
    blocked_patterns TEXT NOT NULL DEFAULT '[]',
    -- 查询结果保留天数；0 = 不保存结果（只留元数据）
    output_retention_days INTEGER NOT NULL DEFAULT 7 CHECK (output_retention_days BETWEEN 0 AND 90),
    updated_at INTEGER NOT NULL
);
INSERT INTO sql_settings (id, blocked_patterns, updated_at)
VALUES (
    1,
    '["drop_database","drop_schema","full_table_write"]',
    CAST(strftime('%s','now') AS INTEGER) * 1000
);

-- 查询结果持久化：confirm 模式批准后的结果、断线后的重取、审计回溯都拿不到行数据，
-- 结果（columns + rows 的 JSON）AES-256-GCM 加密落库（与凭证同一把密钥），按天数保留。
CREATE TABLE sql_query_outputs (
    log_id INTEGER PRIMARY KEY REFERENCES sql_exec_logs(id) ON DELETE CASCADE,
    result_ciphertext BLOB NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_sql_query_outputs_created ON sql_query_outputs(created_at);
