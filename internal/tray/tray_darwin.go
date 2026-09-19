//go:build darwin

package tray

import (
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"fyne.io/systray"
)

//go:embed icon.png
var iconPNG []byte

var hooks Hooks

// Start 创建 macOS 菜单栏（托盘）图标并开始服务菜单事件。非阻塞（专用线程见 startLoop）。
// 注意：fyne.io/systray 的 darwin 实现内部把 Cocoa 调用派发到主线程；若实际运行中发现
// 与 Wails 的 NSApplication 主循环冲突（表现为菜单栏图标不出现），可改用
// systray.Register 把托盘挂到 Wails 已运行的 NSApplication 上（菜单逻辑不变）。
func Start(h Hooks) error {
	hooks = h
	startLoop(func() error {
		// darwin 下 SetIcon 直接接收内存图像数据（PNG 即可，无需落盘）。无返回值。
		systray.SetIcon(iconPNG)
		slog.Info("tray: icon registered")
		return nil
	}, "登录 macOS 后自动运行 opsbox")
	return nil
}

// Stop 移除托盘图标（应用退出前调用）。
func Stop() { systray.Quit() }

// ---- 开机自启：LaunchAgent ----

// launchAgentLabel 是 LaunchAgent 的唯一标识（同时是 plist 文件名）。
// 放在 ~/Library/LaunchAgents 下的 plist 由 launchd 在用户登录后自动拉起。
const launchAgentLabel = "com.opsbox.app"

// launchAgentPath 返回 LaunchAgent plist 路径（~/Library/LaunchAgents/<label>.plist）。
func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist"), nil
}

// autostartEnabled：plist 存在即视为已启用（内容记录 opsbox 的 Label）。
// 安装位置移动后的路径修正由 serveMenu 的「已勾选则重写」逻辑兜底。
func autostartEnabled() bool {
	path, err := launchAgentPath()
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), launchAgentLabel)
}

// setAutostart 写入/删除 LaunchAgent plist。幂等：重复写入即升级修正路径。
// 说明：plist 指向 .app 包内的可执行文件（os.Executable 在 .app 里返回
// /Applications/opsbox.app/Contents/MacOS/opsbox），登录后由 launchd 拉起；
// 变更对下一次登录生效（与 Windows HKCU Run 的生效时机一致）。
func setAutostart(enable bool) error {
	path, err := launchAgentPath()
	if err != nil {
		return err
	}
	if !enable {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(launchAgentPlist(exe)), 0o644)
}

// launchAgentPlist 生成 RunAtLoad 的 LaunchAgent 配置。exe 路径经 XML 转义。
func launchAgentPlist(exe string) string {
	escape := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>
`, launchAgentLabel, escape.Replace(exe))
}
