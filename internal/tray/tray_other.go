//go:build !windows

package tray

// Start 非 Windows 平台暂不实现托盘（opsbox 当前只发 Windows 包）。
func Start(Hooks) error { return nil }

// Stop 非 Windows 平台无托盘可移除。
func Stop() {}

func autostartEnabled() bool { return false }

func setAutostart(bool) error { return nil }
