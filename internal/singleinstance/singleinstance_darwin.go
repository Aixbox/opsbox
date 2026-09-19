//go:build darwin

package singleinstance

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// lockFile 常驻包级变量：flock 锁随文件描述符存活，进程退出即自动释放。
var lockFile *os.File

// tryLock 用锁文件上的 flock 独占锁实现单实例（类 Unix 惯例），拿到返回 true。
// 锁文件不可用（数据目录不可写等）时放行启动，不因锁机制拒绝服务——
// 与 Windows 版「名称构造失败放行」的取舍一致。
func tryLock() bool {
	base, err := os.UserConfigDir()
	if err != nil {
		return true
	}
	dir := filepath.Join(base, "opsbox")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return true
	}
	f, err := os.OpenFile(filepath.Join(dir, "singleinstance.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return false
	}
	lockFile = f
	return true
}

// alertAlreadyRunning 经 osascript 弹系统对话框（唤不醒已有实例时的兜底提示；
// 无 GUI 会话时静默失败）。
func alertAlreadyRunning() {
	script := `display dialog "opsbox 已在运行：请从菜单栏或 Dock 打开 opsbox 窗口；若无法打开，请在活动监视器结束 opsbox 后重试。" with title "opsbox"`
	_ = exec.Command("osascript", "-e", script).Run()
}
