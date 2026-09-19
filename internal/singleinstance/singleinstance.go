// Package singleinstance 保证 opsbox 桌面应用单实例运行：
// 已有实例存活时，新启动的进程唤出已有窗口后退出——数据目录（SQLite）不会被双开，
// 本地服务端口也稳定在首选值，CLI 的端口发现因此可以依赖固定窗口。
//
// 锁原语按平台实现：Windows = 命名互斥锁（singleinstance_windows.go），
// macOS = 锁文件 flock（singleinstance_darwin.go）；其余平台无约束。
package singleinstance

import (
	"bytes"
	"net/http"
	"time"

	"opsbox/internal/cliapp"
)

// Acquire 尝试获取单实例锁。返回 false 表示已有实例在运行（调用方直接退出）：
// 优先经 /ui/show 唤出已有实例的窗口后静默退出；唤不醒（如旧版本实例）才弹提示。
// 锁资源随进程存活（Windows 句柄 / darwin flock 描述符），进程退出即自动释放，
// 无残留锁问题。
func Acquire() bool {
	if tryLock() {
		return true
	}
	if wakeExistingInstance() {
		return false
	}
	alertAlreadyRunning()
	return false
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
