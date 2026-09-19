//go:build !windows && !darwin

package climgr

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// 非 Windows/macOS 平台（仅作 CLI 开发环境，不发布桌面包）：提供可编译的
// 通用 unix 默认实现；PATH 不由应用管理（no-op / 报错），安装目录沿用
// unix 惯例 ~/.local/bin。

func cliBinaryNames() []string { return []string{"sshctl", "sqlctl", "redisctl"} }

func installDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".local", "bin")
}

// userPathEntries 返回当前进程 PATH 条目（冲突扫描用）。
func userPathEntries() []string {
	var entries []string
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if entry = strings.TrimSpace(entry); entry != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

func pathConfigured(string) bool { return false }

func addUserPath(dir string) error {
	return errors.New("当前平台不支持应用内写入 PATH，请手动把 " + dir + " 加入 shell 配置")
}

func removeUserPath(string) error { return nil }

func removePathEntry(string) error { return nil }
