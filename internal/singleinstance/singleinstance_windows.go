//go:build windows

// Package singleinstance 保证 opsbox 桌面应用单实例运行：
// 已有实例存活时，新启动的进程弹提示后退出——数据目录（SQLite）不会被双开，
// 本地服务端口也稳定在首选值，CLI 的端口发现因此可以依赖固定窗口。
package singleinstance

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const mutexName = `Local\opsbox-singleton`

// Acquire 尝试获取单实例锁。返回 false 表示已有实例在运行（此时已弹出提示，调用方直接退出）。
// 锁句柄故意不释放：随进程存活，进程退出即自动释放，无残留锁问题。
func Acquire() bool {
	name, err := windows.UTF16PtrFromString(mutexName)
	if err != nil {
		// 名称构造失败属极端情况：放行启动，不因锁机制拒绝服务
		return true
	}
	// 已存在时返回 ERROR_ALREADY_EXISTS；任何拿不到锁的情况都不应双开
	if _, err = windows.CreateMutex(nil, true, name); err != nil {
		alertAlreadyRunning()
		return false
	}
	return true
}

func alertAlreadyRunning() {
	user32 := windows.NewLazySystemDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")
	title, _ := windows.UTF16PtrFromString("opsbox")
	text, _ := windows.UTF16PtrFromString("opsbox 已在运行：可从任务栏右下角托盘图标打开窗口（点 X 只是收起到托盘，服务保持运行）。")
	_, _, _ = messageBox.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x40 /* MB_ICONINFORMATION */)
}
