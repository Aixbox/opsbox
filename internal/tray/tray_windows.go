//go:build windows

package tray

import (
	_ "embed"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"fyne.io/systray"
	"golang.org/x/sys/windows/registry"
)

//go:embed icon.ico
var iconICO []byte

var hooks Hooks

// Start 创建托盘图标并开始服务菜单事件。非阻塞（专用线程）。
// systray 的窗口创建与消息泵必须固定在同一个 OS 线程上（库的 init() 只锁
// 主线程，而主线程归 Wails 所有），所以这里显式 LockOSThread 后再 Run，
// 避免调度器把 goroutine 搬到别的线程导致托盘消息/行为异常。
func Start(h Hooks) error {
	hooks = h
	go func() {
		runtime.LockOSThread()
		systray.Run(onReady, onExit)
	}()
	return nil
}

// Stop 移除托盘图标（应用退出前调用）。
func Stop() { systray.Quit() }

func onReady() {
	// SetIcon 内部吞错误只打库日志（GUI 进程里不可见），这里用返回 error 的
	// 入口并把失败写进 slog，托盘图标消失时至少能从 stderr/日志定位。
	if path, err := writeIconFile(); err != nil {
		slog.Error("tray: write icon file", "error", err)
	} else if err := systray.SetIconFromFilePath(path); err != nil {
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
