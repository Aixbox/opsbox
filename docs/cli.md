# opsbox CLI 使用指南（AI Agent 必读）

sshctl / sqlctl / redisctl 是 opsbox 内置的三个运维 CLI，供 AI（及人）在命令行操控 SSH、数据库、Redis。执行、审计、审批全部发生在本地 opsbox 服务里，CLI 本身无状态、无登录。

## 安装

**推荐：应用内一键安装。** 打开 opsbox 窗口 → 右上角「AI CLI」→ 点击「一键安装」。CLI 由应用内嵌，安装到 `%LOCALAPPDATA%\Programs\opsbox\bin` 并写入用户 PATH，装完**重开一个终端**即可在任意目录使用。应用升级后重点一次「一键安装」即可覆盖更新；同一面板可卸载。

开发者备选（仓库内构建，日常用户无需关心）：

```powershell
# 在 opsbox 仓库根目录执行（构建 + 复制到 %LOCALAPPDATA%\Programs\opsbox\bin + 加入用户 PATH）
powershell -ExecutionPolicy Bypass -File scripts\install-cli.ps1
# 卸载
powershell -ExecutionPolicy Bypass -File scripts\uninstall-cli.ps1
```

> 若本机还装有旧平台（personal-admin）的同名 CLI（`...\Programs\padmin`），PATH 靠前的那个生效——AI CLI 面板会列出冲突目录。不再用旧平台的话先卸载它（旧版自带 `xxxctl uninstall`，或删除 padmin 目录并从 PATH 移除），避免敲 `sshctl` 连到旧平台。

## 前置条件

1. **opsbox 桌面应用必须正在运行**（应用单实例运行、端口稳定；CLI 先读发现文件 `%APPDATA%\opsbox\server.json`，再探测 `127.0.0.1:37421..37445` 的 `/healthz` 并校验服务身份为 opsbox。应用未启动时 CLI 报错退出码 4）。
2. **会话即授权**：只有用户在 opsbox 窗口里打开的会话（SSH 终端 / SQL 控制台 / Redis 控制台）能被 CLI 操作。连接没有打开的会话时一律拒绝；用户关掉会话即刻失权。CLI 不能自己新建会话。
3. 地址优先级：`--url` 参数 → `OPSBOX_URL` 环境变量 → 自动探测。一般无需指定。

## 退出码（三个 CLI 统一，脚本据此判断）

| 码 | 含义 | AI 应对 |
|---|------|---------|
| 0 | 成功 | 继续 |
| 1 | 远端失败（命令非零退出 / SQL 报错 / Redis 错误） | 读 stderr 里的原始错误 |
| 2 | 被黑名单拦截 / 被拒绝 / 等待批准超时 / 无可用会话 | **停止，不要重试**；把原因告诉用户 |
| 3 | 超时 | 检查超时设置或远端状态 |
| 4 | 本地服务 API 错误（opsbox 未运行等） | 提示用户启动 opsbox |
| 5 | **需要审批且尚未取得用户确认** | 走下方审批协议 |

## 审批协议（核心，AI 必读）

confirm 策略下的写操作（以及命中黑名单动态规则的命令）需要用户批准：

1. **直接执行**会返回退出码 5 + `*_CHECK_REQUIRED`（HTTP 409），错误输出里已包含判定原因、**预检令牌**与指引——这次被拒的响应本身就是预检结果。
2. **向用户完整展示这条命令 / SQL 与其影响，取得明确确认。** 未获确认前不得重试，不得改写命令绕过。
3. 确认后**原样重跑同一条命令并加 `--confirm-token <令牌>`**。操作进入待批队列，用户在 opsbox 窗口对应模块的「待批准」面板里批准后才会真正执行。CLI 等待期间用 `--wait` 控制（默认 5 分钟）。
4. `--check` 只预检不执行（无副作用），可在提交前先拿判定结果与令牌。
5. CLI 不能调用 approve/reject——批准权只在 opsbox 窗口的用户手里（服务端强制）。

## sshctl — SSH 运维

```
sshctl connections list                     # 列出可用连接
sshctl sessions list                        # 列出已打开的终端（只有这些连接能操作）
sshctl exec prod -- df -h                   # 执行命令（退出码透传远端退出码）
sshctl exec prod --check -- "rm -rf /tmp/x" # 预检
sshctl exec prod --confirm-token T -- "rm -rf /tmp/x"   # 确认后重提
sshctl exec prod --timeout 120s -- "tail -n 200 /var/log/nginx/error.log"
sshctl exec prod --pty -- sudo systemctl restart nginx  # 需要 TTY 时
sshctl upload prod ./a.tgz /tmp/a.tgz       # SFTP 上传（分片 + 断点续传）
sshctl download prod /var/log/big.log ./big.log --resume
sshctl status 42 --wait 60s                 # 查看 / 继续等待某条命令
sshctl shell prod                           # 接入已打开终端（人用；AI 用 exec）
```

注意：
- 长任务不要加大 `--timeout`（上限 600s），改为 `nohup` 后台运行 + 轮询日志。
- 输出超限会被截断；需要完整输出先重定向到远端文件再分段取。

## sqlctl — MySQL / PostgreSQL

```
sqlctl connections list
sqlctl sessions list                        # 已打开的控制台
sqlctl tables prod                          # 列表
sqlctl schema prod users                    # 表结构（列 / 索引 / DDL）
sqlctl query prod -- SELECT id, name FROM users LIMIT 20
sqlctl query prod --json -- SELECT * FROM orders LIMIT 100
sqlctl query prod --check -- "DELETE FROM logs WHERE id=1"
sqlctl query prod --confirm-token T -- "DELETE FROM logs WHERE id=1"
sqlctl query prod --tx -- "UPDATE a SET x=1 WHERE id=1" "INSERT INTO log VALUES (1)"
sqlctl status 7
```

注意：探索性查询带 `LIMIT` 并用 `--json` / `--csv`；黑名单（无 WHERE 的 UPDATE/DELETE、DROP DATABASE 等）任何策略下都拦截（退出码 2）。

## redisctl — Redis / Valkey

```
redisctl connections list
redisctl sessions list
redisctl exec prod -- HGETALL user:1
redisctl exec prod --arg "HSET" --arg "user:1" --arg "有 空格 的值"
redisctl exec prod --check -- FLUSHDB
redisctl exec prod --confirm-token T -- FLUSHDB
redisctl scan prod "user:*"                 # 安全 SCAN，禁止用 KEYS
redisctl status 3
```

注意：`--` 后按空格分词，含空格参数用 `--arg`；MOVED 错误表示集群 key 在其他节点（v1 不支持集群路由）。

## 排错

- 「未发现本地 opsbox 服务」→ 启动 opsbox 桌面应用；或用 `--url http://127.0.0.1:<端口>` 显式指定。
- 「该连接没有已打开的终端/控制台会话」→ 请用户在 opsbox 窗口打开对应连接的会话后再试。
- 命令卡在 pending → 让用户在 opsbox 窗口的待批准面板处理；`sshctl status <ID>` 可继续等待。
