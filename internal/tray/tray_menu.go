//go:build windows || darwin

package tray

import (
	"log/slog"
	"runtime"

	"fyne.io/systray"
)

// 托盘消息循环与菜单：windows/darwin 共用，从各自平台文件的 Start 进入。
// stub 平台（tray_other.go）无托盘，不编译本文件——这也是它与 tray.go 分开
// 的原因：共享循环引用平台变量 hooks，放进无 tag 文件会让 stub 平台编译失败、
// staticcheck 判 startLoop/serveMenu unused。

// startLoop 在专用 OS 线程上运行 systray 消息循环。
// systray 的窗口创建与消息泵必须固定在同一个 OS 线程上（库的 init() 只锁
// 主线程，而主线程归 Wails 所有），所以这里显式 LockOSThread 后再 Run，
// 避免调度器把 goroutine 搬到别的线程导致托盘消息/行为异常。
func startLoop(iconSetup func() error, autoHint string) {
	go func() {
		defer func() {
			// systray 内部出错可能 panic（如托盘注册失败），托盘静默消失最难排查，
			// 这里把 panic 落到 stderr 保留现场。
			if r := recover(); r != nil {
				slog.Error("tray: systray goroutine panic", "panic", r)
			}
		}()
		runtime.LockOSThread()
		slog.Info("tray: systray thread starting")
		systray.Run(func() { serveMenu(iconSetup, autoHint) }, func() {})
		slog.Info("tray: systray loop exited")
	}()
}

// serveMenu 构建托盘菜单并服务菜单事件（在 systray.Run 的 onReady 回调里执行）。
// iconSetup 注册平台图标，失败只记日志不阻断菜单；autoHint 是「开机自启」的提示文案。
// 菜单项行为跨平台一致：打开主窗口 / 开机自启勾选（状态源在平台文件）/ 退出。
func serveMenu(iconSetup func() error, autoHint string) {
	slog.Info("tray: onReady, registering icon")
	if err := iconSetup(); err != nil {
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
	mAuto := systray.AddMenuItemCheckbox("开机自启", autoHint, autostartEnabled())
	autoMu.Lock()
	autoItem = mAuto
	autoMu.Unlock()
	mQuit := systray.AddMenuItem("退出", "退出 opsbox（本地服务与 CLI 随之停止）")

	// 开启自启但可执行文件位置变了（升级/移动安装位置）时静默修正状态源。
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
				// 平台状态源是唯一事实：不依赖菜单勾选缓存，设置页改过后这里仍翻转正确。
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
