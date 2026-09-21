package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"opsbox/internal/api"
	"opsbox/internal/climgr"
	"opsbox/internal/platform/security"
	"opsbox/internal/store"
	"opsbox/internal/tray"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// appVersion 由构建注入（wails build -ldflags "-X main.appVersion=…"），与内嵌 CLI 同版本。
var appVersion = "dev"

// 关窗行为取值：点 X 是收到托盘还是直接退出（应用设置页可改）。
const (
	closeActionTray = "tray"
	closeActionExit = "exit"
)

// App 是 Wails 绑定对象：生命周期管理 + 给前端暴露少量元信息。
type App struct {
	ctx      context.Context
	server   *api.Server
	dataDir  string
	quitting bool

	// cfgMu 保护 cfg（含 closeAction）：beforeClose 由 Wails 线程读，
	// SetCloseAction 由前端绑定调用写，并发发生。
	cfgMu   sync.Mutex
	cfg     config
	cfgPath string
}

func NewApp() *App { return &App{} }

// config 是持久化在数据目录里的本地配置：加密密钥 + 应用偏好。
type config struct {
	Secret string `json:"secret"`
	// CloseAction 点 X 关窗行为："tray"（默认，隐藏到托盘）或 "exit"（退出应用）。
	CloseAction string `json:"closeAction,omitempty"`
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	dataDir, err := ensureDataDir()
	if err != nil {
		slog.Default().Error("prepare data dir", "error", err)
		return
	}
	a.dataDir = dataDir
	// 先把 slog 切到文件再继续：打包后的窗口程序没有 stdout，
	// 不落文件的日志等于没有，失败诊断全靠它。
	if err := setupFileLog(dataDir); err != nil {
		slog.Default().Warn("setup file log", "error", err)
	}
	log := slog.Default()
	a.cfgPath = filepath.Join(dataDir, "config.json")

	cfg, err := loadConfig(a.cfgPath)
	if err != nil {
		log.Error("load config", "error", err)
		return
	}
	a.cfg = cfg
	cipher, err := security.NewTokenCipher(cfg.Secret)
	if err != nil {
		log.Error("create cipher", "error", err)
		return
	}
	db, err := store.Open(filepath.Join(dataDir, "opsbox.db"))
	if err != nil {
		log.Error("open database", "error", err)
		return
	}
	server, err := api.New(db, cipher, log, appVersion)
	if err != nil {
		log.Error("create api server", "error", err)
		return
	}
	if err := server.Listen(api.PreferredPort); err != nil {
		log.Error("listen 127.0.0.1", "error", err)
		return
	}
	go func() {
		if err := server.Serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("api server exited", "error", err)
		}
	}()
	// RunBackground 内部是三个永不返回的后台循环（会话回收等），
	// 必须异步启动：同步调用会卡死 startup，导致托盘/发现文件永远不初始化。
	go server.RunBackground(ctx)
	a.server = server
	// 二启进程经 POST /ui/show 唤出窗口（Docker Desktop 行为：再点一次 exe = 弹窗口）
	server.SetShowUI(func() { a.showWindow(ctx) })
	a.writeDiscoveryFile(server.Port())
	// 托盘常驻：关窗最小化后由托盘唤回；开机自启开关也挂在托盘菜单。
	if err := tray.Start(tray.Hooks{
		Show: func() { a.showWindow(ctx) },
		Quit: func() {
			a.quitting = true
			wailsruntime.Quit(ctx)
		},
	}); err != nil {
		log.Warn("start tray", "error", err)
	}
	log.Info("opsbox api ready", "port", server.Port(), "dataDir", dataDir)
}

// showWindow 显示并聚焦主窗口（托盘唤回、二启唤醒共用）。
func (a *App) showWindow(ctx context.Context) {
	wailsruntime.WindowUnminimise(ctx)
	wailsruntime.WindowShow(ctx)
}

// ---- 自定义标题栏的窗口控制绑定（无边框模式下系统标题栏按钮不再存在）----

// WindowMinimise 最小化窗口。
func (a *App) WindowMinimise() { wailsruntime.WindowMinimise(a.ctx) }

// WindowToggleMaximise 最大化/还原窗口。
func (a *App) WindowToggleMaximise() { wailsruntime.WindowToggleMaximise(a.ctx) }

// WindowIsMaximised 报告窗口是否最大化（标题栏据此切换最大化/还原图标）。
func (a *App) WindowIsMaximised() bool { return wailsruntime.WindowIsMaximised(a.ctx) }

// WindowClose 关闭窗口。pkg/runtime 未暴露 WindowClose，而系统 X 按钮的
// onClose 事件同样汇入 Frontend.Quit()，且 OnBeforeClose 正是在 Quit 里检查，
// 因此这里用 Quit 实现：仍按设置页「关闭窗口时」决定隐藏到托盘还是真正退出。
func (a *App) WindowClose() { wailsruntime.Quit(a.ctx) }

// beforeClose 拦截窗口关闭，行为由设置页的「关闭窗口时」决定：
// tray（默认）= 隐藏到托盘，本地服务保持运行（CLI 可用）；exit = 直接退出。
// 托盘「退出」先置 quitting 再 Quit，此时放行真正关闭。
func (a *App) beforeClose(ctx context.Context) bool {
	if a.quitting || a.closeAction() == closeActionExit {
		return false
	}
	wailsruntime.WindowHide(ctx)
	return true
}

// closeAction 读取当前关窗行为（未加载配置时按默认 tray 处理）。
func (a *App) closeAction() string {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if a.cfg.CloseAction == closeActionExit {
		return closeActionExit
	}
	return closeActionTray
}

func (a *App) shutdown(ctx context.Context) {
	tray.Stop()
	if a.server != nil {
		_ = a.server.Shutdown(ctx)
	}
	a.removeDiscoveryFile()
}

// discoveryFile 是 CLI 的快速发现文件：opsbox 实际监听端口写在数据目录里，
// CLI 优先读它（校验 /healthz 身份后使用），避免端口顺延场景下的逐口扫描。
type discoveryFile struct {
	Port      int    `json:"port"`
	PID       int    `json:"pid"`
	Version   string `json:"version"`
	StartedAt string `json:"startedAt"`
}

func (a *App) discoveryPath() string { return filepath.Join(a.dataDir, "server.json") }

func (a *App) writeDiscoveryFile(port int) {
	payload, err := json.Marshal(discoveryFile{
		Port:      port,
		PID:       os.Getpid(),
		Version:   appVersion,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	if err := os.WriteFile(a.discoveryPath(), payload, 0o600); err != nil {
		slog.Default().Warn("write server.json", "error", err)
	}
}

func (a *App) removeDiscoveryFile() {
	if a.dataDir == "" {
		return
	}
	if err := os.Remove(a.discoveryPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Default().Warn("remove server.json", "error", err)
	}
}

// ServerPort 供前端发现 API 端口（默认端口被占用时会同向顺延）。
func (a *App) ServerPort() int {
	if a.server == nil {
		return 0
	}
	return a.server.Port()
}

// DataDir 返回数据目录（数据库、密钥所在），前端「打开数据目录」可用。
func (a *App) DataDir() string { return a.dataDir }

// CLIStatus 返回内嵌运维 CLI 的安装状态（前端「AI CLI」面板展示）。
func (a *App) CLIStatus() climgr.Status { return climgr.Current() }

// InstallCLIs 把内嵌的 sshctl / sqlctl / redisctl 安装到用户目录并写入 PATH（幂等，可作升级）。
func (a *App) InstallCLIs() (climgr.Status, error) { return climgr.Install() }

// UninstallCLIs 移除已安装的 CLI 并清理 PATH 条目。
func (a *App) UninstallCLIs() (climgr.Status, error) { return climgr.Uninstall() }

// TakeOverConflicts 清理用户 PATH 中遮蔽 opsbox 命令的旧版同名 CLI（如旧平台 padmin）。
func (a *App) TakeOverConflicts() (climgr.Status, error) { return climgr.TakeOverConflicts() }

// AppSettings 应用偏好（设置页展示）。
type AppSettings struct {
	// CloseAction 点 X 关窗行为："tray" 隐藏到托盘 / "exit" 退出应用。
	CloseAction string `json:"closeAction"`
	// Autostart 是否已注册开机自启（读注册表实际状态，与托盘菜单同一开关）。
	Autostart bool `json:"autostart"`
}

// GetAppSettings 读取应用偏好。
func (a *App) GetAppSettings() AppSettings {
	return AppSettings{CloseAction: a.closeAction(), Autostart: tray.Autostart()}
}

// SetCloseAction 设置点 X 关窗行为并持久化到 config.json，即时生效（下次关窗即按新值走）。
func (a *App) SetCloseAction(action string) error {
	if action != closeActionTray && action != closeActionExit {
		return fmt.Errorf("未知的关闭行为: %q", action)
	}
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if a.cfgPath == "" {
		return errors.New("配置尚未加载")
	}
	a.cfg.CloseAction = action
	return saveConfig(a.cfgPath, a.cfg)
}

// SetAutostart 注册/注销开机自启（HKCU Run），并同步托盘菜单勾选。
func (a *App) SetAutostart(enable bool) error {
	if err := tray.SetAutostart(enable); err != nil {
		return err
	}
	tray.SyncAutostart(enable)
	return nil
}

// ensureDataDir 返回（并创建）数据目录：<UserConfigDir>/opsbox。
func ensureDataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "opsbox")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// setupFileLog 把 slog 默认输出切到 <dataDir>/logs/opsbox.log（追加写）。
// 超过 8MB 时启动轮转为 .old，只保留一代——本地日志够排查用即可，不做完整轮转体系。
func setupFileLog(dataDir string) error {
	logDir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(logDir, "opsbox.log")
	if info, err := os.Stat(path); err == nil && info.Size() > 8<<20 {
		_ = os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return nil
}

// saveConfig 把配置原子写回数据目录（先写临时文件再替换，避免写坏密钥文件）。
func saveConfig(path string, cfg config) error {
	encoded, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadConfig 读取配置；密钥缺失时生成一次性随机密钥并保存。
// 注意：密钥丢失 = 已存凭证与命令输出无法解密，文件请勿删除。
func loadConfig(path string) (config, error) {
	var cfg config
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return cfg, fmt.Errorf("解析配置文件: %w", err)
		}
	case errors.Is(err, os.ErrNotExist):
		seed := make([]byte, 32)
		if _, err := rand.Read(seed); err != nil {
			return cfg, err
		}
		cfg.Secret = base64.RawURLEncoding.EncodeToString(seed)
		if err := saveConfig(path, cfg); err != nil {
			return cfg, err
		}
	default:
		return cfg, err
	}
	if cfg.Secret == "" {
		return cfg, errors.New("config.json 中 secret 为空")
	}
	return cfg, nil
}
