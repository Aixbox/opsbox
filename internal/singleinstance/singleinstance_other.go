//go:build !windows && !darwin

package singleinstance

// 非 Windows/macOS 平台（仅 CLI 开发环境，不发布桌面包）无单实例约束。

func tryLock() bool { return true }

func alertAlreadyRunning() {}
