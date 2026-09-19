// Package tray 提供系统托盘常驻能力：关窗最小化后，托盘图标负责唤回窗口、
// 开机自启开关与退出。Hooks 由宿主（Wails App）注入，包内不直接依赖 wails。
//
// 本文件是全平台共享部分；systray 消息循环（startLoop/serveMenu）只在
// windows/darwin 实现（tray_menu.go），stub 平台（tray_other.go）无托盘。
package tray

import (
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
