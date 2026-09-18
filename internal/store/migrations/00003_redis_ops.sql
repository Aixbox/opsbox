-- opsbox Redis 运维模块：由 personal-admin 的 00022 + 00023 合并而来。
-- 本地单用户版去掉 users / menus / permissions / app_meta；user_id、approved_by 仅保留列位（无外键），
-- settings 的 output_retention_days 直接并入建表语句（原 00023 为 ALTER TABLE）。

CREATE TABLE redis_connections (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 100),
    host TEXT NOT NULL,
    port INTEGER NOT NULL DEFAULT 6379 CHECK (port BETWEEN 1 AND 65535),
    -- 空表示无密码（内网 / ACL 由用户名默认账号）
    password_ciphertext BLOB,
    db INTEGER NOT NULL DEFAULT 0 CHECK (db BETWEEN 0 AND 15),
    tls INTEGER NOT NULL DEFAULT 0 CHECK (tls IN (0, 1)),
    -- readonly：写命令一律拒；confirm：读直接执行、写进待批队列（默认）；allow：全放行 + 审计
    write_policy TEXT NOT NULL DEFAULT 'confirm' CHECK (write_policy IN ('readonly', 'confirm', 'allow')),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    remark TEXT NOT NULL DEFAULT '',
    created_by INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_redis_connections_name ON redis_connections(name);

CREATE TABLE redis_exec_logs (
    id INTEGER PRIMARY KEY,
    connection_id INTEGER NOT NULL REFERENCES redis_connections(id) ON DELETE CASCADE,
    user_id INTEGER,
    -- 展示用命令文本（超长截断）；待批命令的原始 args 在 options_json 里，批准时据此执行
    command TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'success', 'failed', 'timeout', 'blocked', 'rejected')),
    reply_bytes INTEGER NOT NULL DEFAULT 0,
    truncated INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0, 1)),
    duration_ms INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    -- {args: [...], reason?: string}：args 为执行参数；reason 为分类说明（未知命令按写等）
    options_json TEXT NOT NULL DEFAULT '{}',
    approved_by INTEGER,
    approved_at INTEGER,
    created_at INTEGER NOT NULL,
    finished_at INTEGER
);
CREATE INDEX idx_redis_exec_logs_created ON redis_exec_logs(created_at DESC);
CREATE INDEX idx_redis_exec_logs_status ON redis_exec_logs(status, created_at DESC);
CREATE INDEX idx_redis_exec_logs_connection ON redis_exec_logs(connection_id, created_at DESC);

CREATE TABLE redis_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    -- scan 助手单次最多返回的 key 数（游标迭代上限）
    scan_key_limit INTEGER NOT NULL DEFAULT 1000 CHECK (scan_key_limit BETWEEN 1 AND 100000),
    -- 单个 bulk string 超过该字节数截断并标记；单次回复总量上限 256KB 固定在代码里
    value_truncate_bytes INTEGER NOT NULL DEFAULT 4096 CHECK (value_truncate_bytes BETWEEN 16 AND 1048576),
    -- 全模式生效的黑名单命令，元素形如 "FLUSHALL" 或 "CONFIG SET"（多 token 前缀匹配）
    blocked_commands TEXT NOT NULL DEFAULT '[]',
    -- 命令回复保留天数；0 = 只留元数据
    output_retention_days INTEGER NOT NULL DEFAULT 7 CHECK (output_retention_days BETWEEN 0 AND 90),
    updated_at INTEGER NOT NULL
);
INSERT INTO redis_settings (id, scan_key_limit, value_truncate_bytes, blocked_commands, updated_at)
VALUES (
    1,
    1000,
    4096,
    '["FLUSHALL","FLUSHDB","SHUTDOWN","DEBUG","MODULE","REPLICAOF","SLAVEOF","SWAPDB","CONFIG SET"]',
    CAST(strftime('%s','now') AS INTEGER) * 1000
);

-- 命令回复加密落库（与 SQL/SSH 输出同一方案）：断线 / 审批异步执行后可经 GET /redis/commands/{id} 重取。
CREATE TABLE redis_exec_outputs (
    log_id INTEGER PRIMARY KEY REFERENCES redis_exec_logs(id) ON DELETE CASCADE,
    reply_ciphertext BLOB NOT NULL,
    created_at INTEGER NOT NULL
);
