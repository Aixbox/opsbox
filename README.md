# opsbox

AI Agent 的本地运维闸门：Windows / macOS 桌面应用，**无需部署服务器**，单进程跑在你自己的电脑上。
你在窗口里保管服务器连接，AI Agent（Claude Code 等）通过内置 CLI 操控服务器——
每一条命令都要过 **黑名单拦截 → 审批放行 → 全量审计** 三道闸，Agent 干活，你掌闸。

- **连接由你保管**：SSH / MySQL / Redis 连接存本机 SQLite，凭证 AES-256-GCM 加密，永不下发给 Agent
- **会话即授权**：Agent 只能操作你打开了终端 / 控制台的连接，关掉会话即刻失权；CLI 自己开不了会话
- **批准权只在窗口里**：写操作先拿预检令牌、进「待批准」面板由你逐条放行，CLI 无法自批自审
- **审计可回溯**：命令、状态、耗时、退出码全量落库，输出加密存储、按天保留

技术栈：Wails v2（Go + WebView）· Gin · SQLite（modernc 纯 Go 驱动）· React 19 + HeroUI 3 + Tailwind 4。

## 使用说明

```
你 ── opsbox 窗口：添加连接 / 打开终端与控制台 / 在「待批准」面板放行或拒绝
                        │ 应用托盘常驻，本地服务 127.0.0.1:37421
AI Agent（Claude Code 等）
   │ Bash 调用 sshctl / sqlctl / redisctl（无状态、无登录、自动发现服务端口）
   ▼
opsbox 本地服务 ── 黑名单（shell 语法树 + 自定义正则）→ 审批队列 → 审计落库
   ▼
SSH / MySQL / Redis 服务器
```

**五步接入一个 Agent：**

1. 启动 opsbox，在「连接」页添加 SSH / 数据库 / Redis 连接
2. 打开该连接的终端或控制台会话（**不开会话 = Agent 无权操作该连接**）
3. 右上角「AI CLI」→ 一键安装（CLI 写入 PATH，装完重开终端生效）
4. 告诉你的 Agent：**先读一遍 [docs/cli.md](docs/cli.md) 再干活**——CLI 安装、退出码约定、审批协议、三个命令的完整用法全在里面
5. Agent 提出写操作时会带预检令牌请求确认，你在「待批准」面板放行；每条命令都能在「日志」页回溯

一次典型协作（SSH 为例）：

```bash
sshctl exec prod -- df -h
# Agent 日常巡检：只读命令直接执行，退出码 0

sshctl exec prod -- "sudo systemctl restart nginx"
# 确认策略下的写操作：被拒（退出码 5）+ 返回预检令牌
# Agent 必须向你完整展示这条命令并取得确认，不得改写绕过

sshctl exec prod --confirm-token <令牌> -- "sudo systemctl restart nginx"
# 确认后原样重提 → 进入待批准队列 → 你在 opsbox 窗口点「批准」→ 命令才真正执行
```

## 架构

```
opsbox.exe（单二进制）
├─ Wails 窗口：加载 frontend/dist（go:embed 内嵌）
├─ 本地 API：127.0.0.1:37421（被占用自动顺延），前端通过 Go 绑定发现端口
└─ 数据：os.UserConfigDir()/opsbox/（Windows 为 %AppData%\opsbox）
    ├─ opsbox.db      SQLite（连接、审计日志、设置）
    └─ config.json    AES-256-GCM 密钥（丢失 = 已存凭证无法解密，勿删）
```

安全模型与原平台一致：凭证加密落库永不下发、黑名单拦截（shell 语法树 + 自定义正则）、
audit / approve 双模式、一次性 WebSocket 票据、命令输出加密落库按天保留。
本地单用户：无登录、无 RBAC，安全边界是「本机进程 + 操作级闸门」。

## 从 personal-admin 搬运的代码

| 目录 | 来源 | 说明 |
| --- | --- | --- |
| `internal/platform/sshx` | backend/internal/platform/sshx | SSH 引擎：拨号/TOFU、exec、SFTP、PTY 会话、黑名单、命令静态分析 |
| `internal/platform/console` | backend/internal/platform/console | WS 票据、会话命名规则 |
| `internal/platform/security` | backend/internal/platform/security/token.go | AES-256-GCM、HMAC（审批预检令牌） |
| `internal/platform/opscheck` | backend/internal/platform/opscheck | CLI 审批预检协议 |
| `internal/cliapp` | backend/internal/cliapp | CLI 共享骨架（本地版：无登录，端口自动发现） |
| `cmd/sshctl` `cmd/sqlctl` `cmd/redisctl` | backend/cmd/* | 三个运维 CLI，AI 操控入口，命令面与退出码与原版一致 |
| `internal/ssh` | backend/internal/modules/ssh | 业务模块，去掉了权限码与 users 表关联 |
| `internal/api/response` | backend/internal/http/response | 统一信封 |
| `frontend/src/features/*` | app/features/{messaging,console,ssh} | UI 组件与页面，去掉登录/RBAC/CLI 安装引导 |

## 开发

```bash
# 前端（frontend/）
pnpm install && pnpm dev        # 走 vite 代理连 127.0.0.1:37421
# 整体（根目录）
wails dev                       # Go + 前端热更新
wails build                     # 出包 build/bin/opsbox.exe（Windows）
```

## 打包

| 平台 | 命令 | 产物 |
| --- | --- | --- |
| Windows | `powershell -File scripts\build-all.ps1 [-Version v0.2.0] [-NSIS]` | `build/bin/opsbox.exe`；release 模式（`-NSIS`）另出安装版 `opsbox-<版本>-windows-installer.exe` + 免安装版 `opsbox-<版本>-windows-portable.zip` |
| macOS | `scripts/build-all.sh [v0.2.0]` | 安装版 `build/dmg/opsbox-<版本>-macos.dmg` + 免安装版 `build/dmg/opsbox-<版本>-macos-portable.zip`（universal：Apple Silicon + Intel） |

两个脚本都会先构建三个 CLI 内嵌进应用（macOS 版 CLI 为 arm64+amd64 universal）。macOS 构建必须在 mac 上进行（Wails 依赖 macOS SDK，无法从 Windows 交叉编译）；Windows 上拿不到 mac 时，推送 `v*` 标签即可由 GitHub Actions（`.github/workflows/release.yml`）在 windows + macos 双平台上自动出包并发布 Release。

## 路线

- [x] 单 exe 本地运行（Wails）+ SSH 全功能（连接 / 终端 / exec / 审计 / 审批）
- [x] 执行日志页面（SSH / SQL / Redis 三模块日志审计）
- [x] SQL / Redis 运维模块（连接 / 控制台 / 日志 / 待批队列）
- [x] 本机 CLI 三件套（sshctl / sqlctl / redisctl，见 docs/cli.md）——AI 通过 Bash 调用，审批预检协议与退出码同原版
- [x] 自定义无边框标题栏（拖拽 / 双击最大化 / Aero Snap，最小化、最大化还原、关闭按钮；关闭仍按设置页「关闭窗口时」走托盘或退出）+ 系统托盘常驻（服务保持运行；托盘单击唤回窗口）+ 开机自启（托盘菜单 / 设置页开关，Windows HKCU Run / macOS LaunchAgent）+ 设置页（关窗行为：托盘 / 退出，config.json 持久化）
- [x] Windows + macOS 双平台出包，Release 同时含免安装版与安装版（Windows portable.zip / NSIS 安装包，macOS universal DMG / .app portable.zip；tag 推送由 GitHub Actions 自动发布）
- [ ] goreleaser 交叉编译

## CLI

三个运维 CLI 供 AI（及人）在命令行操控本机 opsbox：执行、审计、审批都在本地服务里，CLI 无状态无登录，自动探测服务端口。审批预检协议（`--check` / `--confirm-token`，退出码 0-5）与 AI 使用指南见 [docs/cli.md](docs/cli.md)。

**安装：打开 opsbox 窗口 → 右上角「AI CLI」→ 一键安装**。CLI 内嵌在应用里：Windows 装到 `%LOCALAPPDATA%\Programs\opsbox\bin` 并写入用户 PATH（注册表）；macOS 装到 `~/.local/bin` 并在 `~/.zshenv` 写入 PATH（bash 用户需自行配置）。重开终端生效；同面板可查看状态、冲突与卸载。

开发者出包：Windows `scripts\build-all.ps1`，macOS `scripts/build-all.sh`（构建 CLI → 内嵌 → wails build → 打安装包）；`scripts\install-cli.ps1` / `uninstall-cli.ps1` 为 Windows 仓库内直接安装/卸载的备选方式。
