//go:build !windows && !darwin

package tray

// 非 Windows/macOS 平台（仅作 CLI 开发环境，不发布桌面包）暂不实现托盘。
// systray 消息循环与菜单（startLoop/serveMenu）在 tray_menu.go 中，仅
// windows/darwin 编译；本文件的 Start 不走托盘循环。

// Start 无托盘可创建，静默成功。
func Start(Hooks) error { return nil }

// Stop 无托盘可移除。
func Stop() {}

func autostartEnabled() bool { return false }

func setAutostart(bool) error { return nil }
