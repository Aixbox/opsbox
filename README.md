# opsbox

本地单机运维工具箱：把 personal-admin 里「运维工具」（SSH AI 运维）的能力抽成独立桌面应用，
**不需要部署服务器**，一个 exe 在用户本机运行。

技术栈：Wails v2（Go + WebView）· Gin · SQLite（modernc 纯 Go 驱动）· React 19 + HeroUI 3 + Tailwind 4。

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
| `internal/ssh` | backend/internal/modules/ssh | 业务模块，去掉了权限码与 users 表关联 |
| `internal/api/response` | backend/internal/http/response | 统一信封 |
| `frontend/src/features/*` | app/features/{messaging,console,ssh} | UI 组件与页面，去掉登录/RBAC/CLI 安装引导 |

## 开发

```bash
# 前端（frontend/）
pnpm install && pnpm dev        # 走 vite 代理连 127.0.0.1:37421
# 整体（根目录）
wails dev                       # Go + 前端热更新
wails build                     # 出包 build/bin/opsbox.exe
```

## 路线

- [x] 单 exe 本地运行（Wails）+ SSH 全功能（连接 / 终端 / exec / 审计 / 审批）
- [ ] 执行日志页面（后端 API 已就绪：GET /api/v1/ssh/exec-logs）
- [ ] SQL / Redis 运维模块按同样模式移植（sqlx / redisx / console 包）
- [ ] 本机 CLI（opsbox ssh exec ...）供 AI 通过 Bash 调用，skills 同步更新
- [ ] 系统托盘 / 开机自启 / goreleaser 交叉编译
