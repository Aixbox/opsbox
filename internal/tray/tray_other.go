//go:build !windows && !darwin

package tray

// 非 Windows/macOS 平台（仅作 CLI 开发环境，不发布桌面包）暂不实现托盘。
// 菜单/勾选同步（startLoop、serveMenu、syncAutostart）在 tray.go 中跨平台共享，
// 这些共享函数引用 hooks 变量（windows/darwin 由各自平台文件定义）；stub 平台
// 的 Start 不走共享循环，hooks 恒为零值，仅为满足编译。

var hooks Hooks

// Start 无托盘可创建，静默成功。
func Start(Hooks) error { return nil }

// Stop 无托盘可移除。
func Stop() {}

func autostartEnabled() bool { return false }

func setAutostart(bool) error { return nil }
