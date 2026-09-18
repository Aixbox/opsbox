//go:build !windows

package cliapp

// pauseForError 在非 Windows 平台是空操作：类 Unix 的终端由 shell 持有，不会随进程退出而关闭。
func pauseForError() {}
