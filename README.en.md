<div align="center">

# opsbox

**A local ops gate for AI agents — the agent does the work, you hold the reins.**

[![Release](https://img.shields.io/github/v/release/Aixbox/opsbox?logo=github)](https://github.com/Aixbox/opsbox/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
![Platform](https://img.shields.io/badge/platform-Windows%20%7C%20macOS-lightgrey)

[简体中文](README.md) · English

</div>

opsbox is a Windows / macOS desktop app that runs entirely on your own machine — **no server deployment needed**. You keep your server connections in the app; AI agents (Claude Code, etc.) operate your servers through the bundled CLIs — and every single command passes three gates: **blocklist interception → approval → full audit**.

- **Connections stay yours**: SSH / MySQL / Redis connections live in a local SQLite database, credentials encrypted with AES-256-GCM and never exposed to the agent
- **A session is the authorization**: agents can only touch connections whose terminal / console session you have opened; close the session and access is gone — CLIs cannot open sessions on their own
- **Approval lives only in the window**: write operations first obtain a pre-check token, then wait in the "Pending approvals" panel for your one-by-one decision; CLIs cannot self-approve
- **Full audit trail**: command, status, duration and exit code are all recorded; command output is encrypted at rest and retained per-day

Tech stack: Wails v2 (Go + WebView) · Gin · SQLite (pure-Go modernc driver) · React 19 + HeroUI 3 + Tailwind 4.

<!-- TODO: screenshots of the main UI (connections page, pending-approvals panel, logs page) go here -->

## How It Works

```
You ── opsbox window: add connections / open terminals & consoles / approve or reject in "Pending approvals"
                        │ tray-resident app, local service on 127.0.0.1:37421
AI Agent (Claude Code, etc.)
   │ invokes sshctl / sqlctl / redisctl via Bash (stateless, no login, auto-discovers the service port)
   ▼
opsbox local service ── blocklist (shell syntax tree + custom regex) → approval queue → audit log
   ▼
SSH / MySQL / Redis servers
```

**Five steps to hook up an agent:**

1. Start opsbox and add an SSH / database / Redis connection on the "Connections" page
2. Open that connection's terminal or console session (**no session opened = the agent has no access to that connection**)
3. Top-right "AI CLI" → one-click install (the CLIs are added to your PATH; reopen your terminal afterwards)
4. Open the connection page of the module, find **"Prompt for your AI agent"** at the bottom of the usage card, copy it with one click and paste it to your agent — the agent will first probe available connections with `sessions list` and learn the rest via `--help`; for the full protocol details see [docs/cli.md](docs/cli.md)
5. When the agent proposes a write operation it will attach a pre-check token and ask for confirmation; you approve it in the "Pending approvals" panel. Every command can be traced on the "Logs" page

A typical collaboration (SSH example):

```bash
sshctl exec prod -- df -h
# Routine inspection: read-only commands run directly, exit code 0

sshctl exec prod -- "sudo systemctl restart nginx"
# Write operation under confirm policy: rejected (exit code 5) with a pre-check token returned
# The agent must show you the full command and get your explicit consent — no rewrites, no bypassing

sshctl exec prod --confirm-token <token> -- "sudo systemctl restart nginx"
# Re-submit the exact command with the token → it enters the approval queue → you click "Approve" in the opsbox window → only then is it executed
```

## Download

Grab the artifact for your platform from [Releases](https://github.com/Aixbox/opsbox/releases) — no need to build from source:

| Platform | Portable | Installer |
| --- | --- | --- |
| Windows | `opsbox-<version>-windows-portable.zip` (unzip and run) | `opsbox-<version>-windows-installer.exe` (NSIS wizard) |
| macOS | `opsbox-<version>-macos-portable.zip` (unzip to get opsbox.app) | `opsbox-<version>-macos.dmg` (drag to install) |

- macOS builds are universal binaries (both Apple Silicon and Intel).
- The app is not code-signed yet: on macOS, if Gatekeeper blocks the first launch, right-click → "Open", or run `xattr -cr /Applications/opsbox.app`; on Windows, click "Run anyway" if SmartScreen warns.
- The three ops CLIs are embedded in the app; see [CLI](#cli) for installation.

## Architecture

```
opsbox.exe (single binary)
├─ Wails window: loads frontend/dist (embedded via go:embed)
├─ Local API: 127.0.0.1:37421 (auto-increments if taken); the frontend discovers the port via Go bindings
└─ Data: os.UserConfigDir()/opsbox/ (%AppData%\opsbox on Windows)
    ├─ opsbox.db      SQLite (connections, audit logs, settings)
    └─ config.json    AES-256-GCM key (if lost, stored credentials can no longer be decrypted — do not delete)
```

Security model: credentials are encrypted at rest and never handed to agents; blocklist interception (shell syntax tree + custom regex); audit / approve dual modes; one-time WebSocket tickets; command output encrypted at rest with per-day retention.
Single-user local app: no login, no RBAC — the security boundary is "local process + per-operation gates".

## CLI

Three ops CLIs let AI (and humans) drive the local opsbox from the command line: execution, auditing and approval all live in the local service; the CLIs are stateless, login-free, and auto-discover the service port. The approval pre-check protocol (`--check` / `--confirm-token`, exit codes 0–5) and the AI usage guide are in [docs/cli.md](docs/cli.md).

**Install: open the opsbox window → top-right "AI CLI" → one-click install**. The CLIs ship inside the app: on Windows they are installed to `%LOCALAPPDATA%\Programs\opsbox\bin` and added to your user PATH (registry); on macOS to `~/.local/bin` with a PATH block written to `~/.zshenv` (bash users need to configure it themselves). Reopen your terminal for it to take effect; the same panel shows status, conflicts and uninstall.

## Building from Source

### Development

```bash
# Frontend (frontend/)
pnpm install && pnpm dev        # vite proxy to 127.0.0.1:37421
# Root
wails dev                       # Go + frontend hot reload
wails build                     # build to build/bin/opsbox.exe (Windows)
```

### Packaging

| Platform | Command | Artifacts |
| --- | --- | --- |
| Windows | `powershell -File scripts\build-all.ps1 [-Version v0.2.0] [-NSIS]` | `build/bin/opsbox.exe`; in release mode (`-NSIS`) also `opsbox-<version>-windows-installer.exe` + `opsbox-<version>-windows-portable.zip` |
| macOS | `scripts/build-all.sh [v0.2.0]` | `build/dmg/opsbox-<version>-macos.dmg` + `build/dmg/opsbox-<version>-macos-portable.zip` (universal: Apple Silicon + Intel) |

Both scripts first build the three CLIs and embed them into the app. macOS builds must run on a mac (Wails needs the macOS SDK and cannot cross-compile from Windows); without a mac, push a `v*` tag and GitHub Actions (`.github/workflows/release.yml`) will build on both windows + macos runners and publish the Release automatically.

## Roadmap

- [x] Single-binary local app (Wails) + full SSH features (connect / terminal / exec / audit / approvals)
- [x] Exec-log pages (audit for SSH / SQL / Redis)
- [x] SQL / Redis modules (connections / console / logs / pending queue)
- [x] Local CLI trio (sshctl / sqlctl / redisctl, see docs/cli.md) — invoked by agents via Bash, with the approval pre-check protocol and exit-code contract
- [x] Custom frameless title bar (drag / double-click maximize / Aero Snap, minimize, maximize-restore, close; close still honors the "when closing the window" setting) + tray-resident app (service keeps running; clicking the tray icon brings the window back) + launch at login (tray menu / settings toggle, Windows HKCU Run / macOS LaunchAgent) + settings page (close behavior: tray / quit, persisted in config.json)
- [x] Windows + macOS releases with both portable and installer artifacts (Windows portable.zip / NSIS installer, macOS universal DMG / .app portable.zip; auto-published by GitHub Actions on tag push)
- [ ] goreleaser cross-compilation

## Contributing

Issues and PRs are welcome at [Issues](https://github.com/Aixbox/opsbox/issues):

1. Fork and branch off `main`
2. Follow Conventional Commits (`feat:` / `fix:` / `refactor:` …)
3. Before opening a PR, make sure `go build ./... && go vet ./...` and the frontend `npm run build` both pass

## Disclaimer

opsbox **actually executes** the commands generated by AI agents against your servers and databases. Make sure you understand the blocklist and approval mechanisms and your session scope before hooking up an agent; any data loss or damage caused by using this software is at your own risk.

## License

[MIT](LICENSE) © 2026 Aixbox
