//go:build !windows

// Package singleinstance 的非 Windows 实现：类 Unix 平台暂无单实例约束（当前产品仅发布 Windows）。
package singleinstance

// Acquire 恒返回 true。
func Acquire() bool { return true }
