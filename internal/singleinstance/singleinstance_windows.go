//go:build windows

// Package singleinstance 保证 opsbox 桌面应用单实例运行：
// 已有实例存活时，新启动的进程弹提示后退出——数据目录（SQLite）不会被双开，
// 本地服务端口也稳定在首选值，CLI 的端口发现因此可以依赖固定窗口。
package singleinstance

import (
	"bytes"
	"net/http"
	"time"
	"unsafe"

	"opsbox/internal/cliapp"

	"golang.org/x/sys/windows"
)

const mutexName = `Local\opsbox-singleton`

// Acquire 尝试获取单实例锁。返回 false 表示已有实例在运行（调用方直接退出）：
// 优先经 /ui/show 唤出已有实例的窗口后静默退出；唤不醒（如旧版本实例）才弹提示。
// 锁句柄故意不释放：随进程存活，进程退出即自动释放，无残留锁问题。
func Acquire() bool {
	name, err := windows.UTF16PtrFromString(mutexName)
	if err != nil {
		// 名称构造失败属极端情况：放行启动，不因锁机制拒绝服务
		return true
	}
	// 已存在时返回 ERROR_ALREADY_EXISTS；任何拿不到锁的情况都不应双开
	if _, err = windows.CreateMutex(nil, true, name); err != nil {
		if !wakeExistingInstance() {
			alertAlreadyRunning()
		}
		return false
	}
	return true
}

// wakeExistingInstance 通过本地服务找到已运行的 opsbox 并请求其显示主窗口。
// 成功返回 true（本进程静默退出即可）。
func wakeExistingInstance() bool {
	base, err := cliapp.DiscoverBaseURL()
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Post(base+"/ui/show", "application/json", bytes.NewReader(nil))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func alertAlreadyRunning() {
	user32 := windows.NewLazySystemDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")
	title, _ := windows.UTF16PtrFromString("opsbox")
	text, _ := windows.UTF16PtrFromString("opsbox 已在运行：请点任务栏右下角「^」（显示隐藏的图标）找到 opsbox 打开窗口；若托盘无图标，请在任务管理器结束 opsbox.exe 后重试。")
	_, _, _ = messageBox.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x40 /* MB_ICONINFORMATION */)
}
