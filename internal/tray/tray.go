// Package tray 提供系统托盘常驻能力：关窗最小化后，托盘图标负责唤回窗口、
// 开机自启开关与退出。Hooks 由宿主（Wails App）注入，包内不直接依赖 wails。
package tray

import (
	"log/slog"
	"runtime"
	"sync"

	"fyne.io/systray"
)

// Hooks 是托盘回调宿主。三个函数都可能从托盘内部 goroutine 调用，需并发安全。
type Hooks struct {
	// Show 显示并聚焦主窗口（从托盘唤回）。
	Show func()
	// Quit 请求退出应用（托盘「退出」）。
	Quit func()
}

// Autostart 报告当前是否已注册开机自启。
func Autostart() bool { return autostartEnabled() }

// SetAutostart 注册/注销开机自启（登录后自动运行 opsbox）。
func SetAutostart(enable bool) error { return setAutostart(enable) }

// SyncAutostart 同步托盘菜单「开机自启」的勾选状态。
// 平台状态源（Windows 注册表 / macOS LaunchAgent plist）是唯一事实，
// 宿主（设置页）改完状态后调用，托盘菜单据此刷新。
func SyncAutostart(enabled bool) { syncAutostart(enabled) }

// autoItem 是「开机自启」菜单项的包级引用：设置页改完状态后据此同步勾选。
// nil 表示托盘尚未就绪（SyncAutostart 此时静默跳过）。
var (
	autoMu   sync.Mutex
	autoItem *systray.MenuItem
)

// startLoop 在专用 OS 线程上运行 systray 消息循环（windows/darwin 共用）。
// systray 的窗口创建与消息泵必须固定在同一个 OS 线程上（库的 init() 只锁
// 主线程，而主线程归 Wails 所有），所以这里显式 LockOSThread 后再 Run，
// 避免调度器把 goroutine 搬到别的线程导致托盘消息/行为异常。
func startLoop(iconSetup func() error, autoHint string) {
	go func() {
		defer func() {
			// systray 内部出错可能 panic（如托盘注册失败），托盘静默消失最难排查，
			// 这里把 panic 落到 stderr 保留现场。
			if r := recover(); r != nil {
				slog.Error("tray: systray goroutine panic", "panic", r)
			}
		}()
		runtime.LockOSThread()
		slog.Info("tray: systray thread starting")
		systray.Run(func() { serveMenu(iconSetup, autoHint) }, func() {})
		slog.Info("tray: systray loop exited")
	}()
}

// serveMenu 构建托盘菜单并服务菜单事件（在 systray.Run 的 onReady 回调里执行）。
// iconSetup 注册平台图标，失败只记日志不阻断菜单；autoHint 是「开机自启」的提示文案。
// 菜单项行为跨平台一致：打开主窗口 / 开机自启勾选（状态源在平台文件）/ 退出。
func serveMenu(iconSetup func() error, autoHint string) {
	slog.Info("tray: onReady, registering icon")
	if err := iconSetup(); err != nil {
		slog.Error("tray: set icon", "error", err)
	}
	systray.SetTooltip("opsbox — 本地运维工具箱")
	// 左键单击托盘 = 唤回主窗口（Docker Desktop 惯例）。
	systray.SetOnTapped(func() {
		if hooks.Show != nil {
			hooks.Show()
		}
	})

	mShow := systray.AddMenuItem("打开 opsbox", "显示主窗口")
	mAuto := systray.AddMenuItemCheckbox("开机自启", autoHint, autostartEnabled())
	autoMu.Lock()
	autoItem = mAuto
	autoMu.Unlock()
	mQuit := systray.AddMenuItem("退出", "退出 opsbox（本地服务与 CLI 随之停止）")

	// 开启自启但可执行文件位置变了（升级/移动安装位置）时静默修正状态源。
	if mAuto.Checked() {
		_ = setAutostart(true)
	}

	go func() {
		for {
			select {
			case <-mShow.ClickedCh:
				if hooks.Show != nil {
					hooks.Show()
				}
			case <-mAuto.ClickedCh:
				// 平台状态源是唯一事实：不依赖菜单勾选缓存，设置页改过后这里仍翻转正确。
				enable := !autostartEnabled()
				if err := setAutostart(enable); err != nil {
					slog.Warn("toggle autostart", "error", err)
					continue
				}
				if enable {
					mAuto.Check()
				} else {
					mAuto.Uncheck()
				}
			case <-mQuit.ClickedCh:
				if hooks.Quit != nil {
					hooks.Quit()
				}
				return
			}
		}
	}()
}

// syncAutostart 同步托盘菜单「开机自启」的勾选（设置页改状态后调用）。
// systray 菜单项方法从任意 goroutine 调用是库支持的用法（与 serveMenu 内响应
// ClickedCh 的 goroutine 一致）。
func syncAutostart(enabled bool) {
	autoMu.Lock()
	item := autoItem
	autoMu.Unlock()
	if item == nil {
		return
	}
	if enabled {
		item.Check()
	} else {
		item.Uncheck()
	}
}
