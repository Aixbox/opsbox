//go:build darwin

package climgr

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// cliBinaryNames：macOS 分发的可执行文件为裸名（无后缀）。
func cliBinaryNames() []string { return []string{"sshctl", "sqlctl", "redisctl"} }

// installDir：~/.local/bin 是 macOS 用户级 CLI 的现代惯例（gh、uv 等独立分发
// 普遍采用），无需管理员权限；PATH 由 addUserPath 写入 ~/.zshenv 打通。
func installDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".local", "bin")
}

// ---- PATH 配置：~/.zshenv 标记块 ----

const (
	pathMarkBegin  = "# >>> opsbox cli >>>"
	pathMarkEnd    = "# <<< opsbox cli <<<"
	pathExportLine = `export PATH="$HOME/.local/bin:$PATH"`
)

// zshenvPath：~/.zshenv 对所有 zsh 调用生效（登录、交互、脚本，含非交互执行），
// macOS Catalina 起默认 shell 为 zsh，因此作为 CLI PATH 的配置点。
// bash 用户不在覆盖范围内，需自行把安装目录加入 PATH（界面文案与 README 有说明）。
func zshenvPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".zshenv")
}

// pathConfigured：zshenv 含 opsbox 标记块即视为已配置（块内容是固定的 export 行，
// 与 installDir 语义绑定，无需解析具体路径）。
func pathConfigured(dir string) bool {
	raw, err := os.ReadFile(zshenvPath())
	if err != nil {
		return false
	}
	return bytes.Contains(raw, []byte(pathMarkBegin))
}

// addUserPath 在 ~/.zshenv 追加标记块（幂等）。GUI 应用与已开终端不读 zshenv，
// 新开的 zsh 会话由此拿到 CLI。
func addUserPath(dir string) error {
	path := zshenvPath()
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if bytes.Contains(raw, []byte(pathMarkBegin)) {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	block := "\n" + pathMarkBegin + "\n" + pathExportLine + "\n" + pathMarkEnd + "\n"
	_, err = f.WriteString(block)
	return err
}

// removeUserPath 删除 zshenv 标记块。dir 不是安装目录时不动作——
// 本应用在 macOS 上只写过安装目录这一个 PATH 条目。
func removeUserPath(dir string) error {
	if dir != installDir() {
		return nil
	}
	path := zshenvPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	begin := bytes.Index(raw, []byte(pathMarkBegin))
	if begin < 0 {
		return nil
	}
	end := bytes.Index(raw, []byte(pathMarkEnd))
	if end < 0 {
		return fmt.Errorf("%s 中 opsbox 标记块不完整（缺少结束标记），请手动清理", path)
	}
	end += len(pathMarkEnd)
	cleaned := string(raw[:begin]) + string(raw[end:])
	cleaned = strings.TrimPrefix(cleaned, "\n")
	return os.WriteFile(path, []byte(cleaned), 0o644)
}

// removePathEntry：macOS 上本应用不管理安装目录以外的 PATH 条目（来自系统、
// shell 配置或进程继承的目录无法也不应改写），冲突目录只删文件、不动 PATH。
func removePathEntry(string) error { return nil }

// userPathEntries 返回当前进程可见的 PATH 条目：GUI 应用继承 launchd 的最小
// PATH，终端里则是 shell 配置的结果——用于冲突扫描（发现同名 CLI 的目录）足够。
func userPathEntries() []string {
	var entries []string
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if entry = strings.TrimSpace(entry); entry != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}
