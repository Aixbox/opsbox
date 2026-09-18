-- opsbox 初始 schema：由 personal-admin 的 00018 + 00019 两个迁移中的 SSH 部分合并而来。
-- 本地单用户版去掉 users / menus / permissions / api_tokens / cli_login_requests，
-- user_id、approved_by 仅保留列位（无外键），created_by 同理。

CREATE TABLE ssh_connections (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 100),
    host TEXT NOT NULL,
    port INTEGER NOT NULL DEFAULT 22 CHECK (port BETWEEN 1 AND 65535),
    username TEXT NOT NULL,
    auth_type TEXT NOT NULL CHECK (auth_type IN ('password', 'key')),
    password_ciphertext BLOB,
    private_key_ciphertext BLOB,
    key_passphrase_ciphertext BLOB,
    host_key TEXT,
    exec_policy TEXT NOT NULL DEFAULT 'audit' CHECK (exec_policy IN ('audit', 'approve')),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    remark TEXT NOT NULL DEFAULT '',
    created_by INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_ssh_connections_name ON ssh_connections(name);

CREATE TABLE ssh_exec_logs (
    id INTEGER PRIMARY KEY,
    connection_id INTEGER NOT NULL REFERENCES ssh_connections(id) ON DELETE CASCADE,
    user_id INTEGER,
    kind TEXT NOT NULL CHECK (kind IN ('exec', 'upload', 'download', 'session')),
    command TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'running', 'success', 'failed', 'timeout', 'blocked', 'rejected')),
    exit_code INTEGER,
    stdout_bytes INTEGER NOT NULL DEFAULT 0,
    stderr_bytes INTEGER NOT NULL DEFAULT 0,
    truncated INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0, 1)),
    duration_ms INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    -- approve 模式的执行参数（timeout/pty 等）随日志行携带，批准时据此执行
    options_json TEXT NOT NULL DEFAULT '{}',
    approved_by INTEGER,
    approved_at INTEGER,
    created_at INTEGER NOT NULL,
    finished_at INTEGER,
    -- 分片传输的心跳：大文件可能传很久，孤儿 running 的回收要看最后活动时间而不是创建时间
    heartbeat_at INTEGER
);
CREATE INDEX idx_ssh_exec_logs_created ON ssh_exec_logs(created_at DESC);
CREATE INDEX idx_ssh_exec_logs_status ON ssh_exec_logs(status, created_at DESC);
CREATE INDEX idx_ssh_exec_logs_connection ON ssh_exec_logs(connection_id, created_at DESC);

-- 命令输出持久化：AES-256-GCM 加密落库（与凭证同一把密钥），按天数保留，可设 0 关闭。
CREATE TABLE ssh_exec_outputs (
    log_id INTEGER PRIMARY KEY REFERENCES ssh_exec_logs(id) ON DELETE CASCADE,
    stdout_ciphertext BLOB,
    stderr_ciphertext BLOB,
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_ssh_exec_outputs_created ON ssh_exec_outputs(created_at);

CREATE TABLE ssh_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    blacklist_patterns TEXT NOT NULL DEFAULT '[]',
    exec_timeout_seconds INTEGER NOT NULL DEFAULT 60 CHECK (exec_timeout_seconds BETWEEN 1 AND 600),
    output_limit_bytes INTEGER NOT NULL DEFAULT 262144 CHECK (output_limit_bytes BETWEEN 1024 AND 8388608),
    -- eval / sh -c / 管道进 shell 等静态无法判定的命令，在 audit 模式下是否也强制人工审批
    dynamic_requires_approval INTEGER NOT NULL DEFAULT 1 CHECK (dynamic_requires_approval IN (0, 1)),
    -- 0 = 不保存输出（只留元数据）
    output_retention_days INTEGER NOT NULL DEFAULT 7,
    updated_at INTEGER NOT NULL
);
INSERT INTO ssh_settings (id, blacklist_patterns, exec_timeout_seconds, output_limit_bytes, dynamic_requires_approval, output_retention_days, updated_at)
VALUES (
    1,
    '["\\bcrontab\\s+-r\\b"]',
    60,
    262144,
    1,
    7,
    CAST(strftime('%s','now') AS INTEGER) * 1000
);
