//go:build windows

package tray

import (
	_ "embed"
	"errors"
	"log/slog"
	"os"

	"fyne.io/systray"
	"golang.org/x/sys/windows/registry"
)

//go:embed icon.ico
var iconICO []byte

var hooks Hooks

// Start 创建托盘图标并开始服务菜单事件。非阻塞（内部 goroutine）。
func Start(h Hooks) error {
	hooks = h
	go systray.Run(onReady, onExit)
	return nil
}

// Stop 移除托盘图标（应用退出前调用）。
func Stop() { systray.Quit() }

func onReady() {
	systray.SetIcon(iconICO)
	systray.SetTooltip("opsbox — 本地运维工具箱")
	// 左键单击托盘 = 唤回主窗口（Docker Desktop 惯例）。
	systray.SetOnTapped(func() {
		if hooks.Show != nil {
			hooks.Show()
		}
	})

	mShow := systray.AddMenuItem("打开 opsbox", "显示主窗口")
	mAuto := systray.AddMenuItemCheckbox("开机自启", "登录 Windows 后自动运行 opsbox", autostartEnabled())
	mQuit := systray.AddMenuItem("退出", "退出 opsbox（本地服务与 CLI 随之停止）")

	// 开启自启但 exe 位置变了（升级/移动）时静默修正注册表路径。
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
				enable := !mAuto.Checked()
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

func onExit() {}

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
