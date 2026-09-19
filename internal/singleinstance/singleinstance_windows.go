//go:build windows

package singleinstance

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const mutexName = `Local\opsbox-singleton`

// tryLock 获取 Windows 命名互斥锁；拿到返回 true。
// 已存在时返回 ERROR_ALREADY_EXISTS；任何拿不到锁的情况都不应双开。
// 名称构造失败属极端情况：放行启动，不因锁机制拒绝服务。
func tryLock() bool {
	name, err := windows.UTF16PtrFromString(mutexName)
	if err != nil {
		return true
	}
	if _, err = windows.CreateMutex(nil, true, name); err != nil {
		return false
	}
	return true
}

// alertAlreadyRunning 弹 Win32 消息框（唤不醒已有实例时的兜底提示）。
func alertAlreadyRunning() {
	user32 := windows.NewLazySystemDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")
	title, _ := windows.UTF16PtrFromString("opsbox")
	text, _ := windows.UTF16PtrFromString("opsbox 已在运行：请点任务栏右下角「^」（显示隐藏的图标）找到 opsbox 打开窗口；若托盘无图标，请在任务管理器结束 opsbox.exe 后重试。")
	_, _, _ = messageBox.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x40 /* MB_ICONINFORMATION */)
}
