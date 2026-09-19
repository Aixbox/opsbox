//go:build windows

package tray

import (
	_ "embed"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"fyne.io/systray"
	"golang.org/x/sys/windows/registry"
)

//go:embed icon.ico
var iconICO []byte

var hooks Hooks

// autoItem 是「开机自启」菜单项的包级引用：设置页改完注册表后据此同步勾选。
// nil 表示托盘尚未就绪（SyncAutostart 此时静默跳过）。
var (
	autoMu   sync.Mutex
	autoItem *systray.MenuItem
)

// Start 创建托盘图标并开始服务菜单事件。非阻塞（专用线程）。
// systray 的窗口创建与消息泵必须固定在同一个 OS 线程上（库的 init() 只锁
// 主线程，而主线程归 Wails 所有），所以这里显式 LockOSThread 后再 Run，
// 避免调度器把 goroutine 搬到别的线程导致托盘消息/行为异常。
func Start(h Hooks) error {
	hooks = h
	go func() {
		defer func() {
			// systray 内部出错可能 panic（如窗口创建失败），托盘静默消失最难排查，
			// 这里把 panic 落到 stderr 保留现场。
			if r := recover(); r != nil {
				slog.Error("tray: systray goroutine panic", "panic", r)
			}
		}()
		runtime.LockOSThread()
		slog.Info("tray: systray thread starting")
		systray.Run(onReady, onExit)
		slog.Info("tray: systray loop exited")
	}()
	return nil
}

// Stop 移除托盘图标（应用退出前调用）。
func Stop() { systray.Quit() }

func onReady() {
	slog.Info("tray: onReady, registering icon")
	// SetIcon 内部吞错误只打库日志（GUI 进程里不可见），这里用返回 error 的
	// 入口并把失败写进 slog，托盘图标消失时至少能从 stderr/日志定位。
	if path, err := writeIconFile(); err != nil {
		slog.Error("tray: write icon file", "error", err)
	} else if err := systray.SetIconFromFilePath(path); err != nil {
		slog.Error("tray: set icon", "error", err)
	} else {
		slog.Info("tray: icon registered", "path", path)
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
	autoMu.Lock()
	autoItem = mAuto
	autoMu.Unlock()
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
				// 注册表是唯一状态源：不依赖菜单勾选缓存，设置页改过后这里仍翻转正确。
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

// syncAutostart 同步托盘菜单「开机自启」的勾选（设置页改注册表后调用）。
// systray 菜单项方法从任意 goroutine 调用是库支持的用法（与 onReady 内响应
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
