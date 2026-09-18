// Package tray 提供系统托盘常驻能力：关窗最小化后，托盘图标负责唤回窗口、
// 开机自启开关与退出。Hooks 由宿主（Wails App）注入，包内不直接依赖 wails。
package tray

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
