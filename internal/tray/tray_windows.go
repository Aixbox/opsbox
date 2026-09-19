//go:build windows

package tray

import (
	_ "embed"
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"fyne.io/systray"
	"golang.org/x/sys/windows/registry"
)

//go:embed icon.ico
var iconICO []byte

var hooks Hooks

// Start 创建托盘图标并开始服务菜单事件。非阻塞（专用线程见 startLoop）。
func Start(h Hooks) error {
	hooks = h
	startLoop(func() error {
		// SetIcon 内部吞错误只打库日志（GUI 进程里不可见），这里用返回 error 的
		// 入口并把失败写进 slog，托盘图标消失时至少能从 stderr/日志定位。
		path, err := writeIconFile()
		if err != nil {
			return err
		}
		if err := systray.SetIconFromFilePath(path); err != nil {
			return err
		}
		slog.Info("tray: icon registered", "path", path)
		return nil
	}, "登录 Windows 后自动运行 opsbox")
	return nil
}

// Stop 移除托盘图标（应用退出前调用）。
func Stop() { systray.Quit() }

// writeIconFile 把内嵌的 ico 落到临时文件（SetIconFromFilePath 需要）。
func writeIconFile() (string, error) {
	dir, err := os.MkdirTemp("", "opsbox-tray")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "opsbox.ico")
	if err := os.WriteFile(path, iconICO, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// autostartRegKey / autostartValue：HKCU Run 键，登录后由系统启动。
var (
	autostartRegKey = `Software\Microsoft\Windows\Run`
	autostartValue  = "opsbox"
)

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRegKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	val, _, err := k.GetStringValue(autostartValue)
	return err == nil && val != ""
}

func setAutostart(enable bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRegKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !enable {
		if err := k.DeleteValue(autostartValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(autostartValue, `"`+exe+`"`)
}
