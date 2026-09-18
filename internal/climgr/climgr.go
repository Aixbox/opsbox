// Package climgr 管理运维 CLI（sshctl / sqlctl / redisctl）的应用内安装：
// CLI 二进制在 wails build 前由 scripts/build-all.ps1 填充到 clis/ 目录并 go:embed 进主程序，
// 用户在 opsbox 界面一键安装到 %LOCALAPPDATA%\Programs\opsbox\bin 并写入用户 PATH——
// 产品用户不需要源码、脚本或手动改环境变量。
package climgr

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed all:clis
var clisFS embed.FS

// cliNames 是随主程序分发的 CLI 可执行文件。
var cliNames = []string{"sshctl.exe", "sqlctl.exe", "redisctl.exe"}

// InstallDir 返回 CLI 安装目标目录（用户级，无需管理员权限）。
func InstallDir() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "opsbox", "bin")
}

// Status 是 CLI 安装状态，供前端「AI CLI」面板展示。
type Status struct {
	// Bundled 表示当前 opsbox.exe 是否内嵌了 CLI 二进制（开发者本地只跑 go build 时可能为 false）。
	Bundled bool `json:"bundled"`
	// Installed 表示安装目录里三个 CLI 是否齐全。
	Installed bool `json:"installed"`
	// InPath 表示安装目录是否已在用户 PATH 中。
	InPath bool `json:"inPath"`
	// Version 是内嵌 CLI 的版本（构建时注入）。
	Version string `json:"version"`
	// InstallDir 是安装目录。
	InstallDir string `json:"installDir"`
	// Conflicts 是用户 PATH 中其他存在的同名 CLI 所在目录（如旧平台 padmin），按 PATH 顺序可能遮蔽新命令。
	Conflicts []string `json:"conflicts,omitempty"`
}

// Version 返回内嵌 CLI 版本号（build-all.ps1 写入 clis/VERSION）。
func Version() string {
	raw, err := clisFS.ReadFile("clis/VERSION")
	if err != nil {
		return "dev"
	}
	return strings.TrimSpace(string(raw))
}

// Bundled 判断主程序是否携带了完整的 CLI 二进制。
func Bundled() bool {
	for _, name := range cliNames {
		if _, err := clisFS.ReadFile("clis/" + name); err != nil {
			return false
		}
	}
	return true
}

// Status 汇总当前安装状态。
func Current() Status {
	dir := InstallDir()
	installed := true
	for _, name := range cliNames {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			installed = false
			break
		}
	}
	inPath := false
	var conflicts []string
	for _, entry := range userPathEntries() {
		if strings.EqualFold(entry, dir) {
			inPath = true
			continue
		}
		// PATH 里的其他目录若也有同名命令，会按 PATH 顺序遮蔽（排前则赢）
		for _, name := range cliNames {
			if _, err := os.Stat(filepath.Join(entry, name)); err == nil {
				conflicts = append(conflicts, entry)
				break
			}
		}
	}
	return Status{
		Bundled:    Bundled(),
		Installed:  installed,
		InPath:     inPath,
		Version:    Version(),
		InstallDir: dir,
		Conflicts:  conflicts,
	}
}

// Install 释放内嵌 CLI 到安装目录并确保在用户 PATH 中（幂等，重复执行即升级覆盖）。
func Install() (Status, error) {
	if !Bundled() {
		return Status{}, fmt.Errorf("当前 opsbox 程序未内嵌 CLI（开发者请先运行 scripts/build-all.ps1 重新构建）")
	}
	dir := InstallDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Status{}, fmt.Errorf("创建安装目录失败: %w", err)
	}
	for _, name := range cliNames {
		raw, err := clisFS.ReadFile("clis/" + name)
		if err != nil {
			return Status{}, fmt.Errorf("读取内嵌 %s 失败: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o755); err != nil {
			return Status{}, fmt.Errorf("写入 %s 失败: %w", name, err)
		}
	}
	entries := userPathEntries()
	found := false
	for _, entry := range entries {
		if strings.EqualFold(entry, dir) {
			found = true
			break
		}
	}
	if !found {
		joined := strings.Join(entries, ";")
		if joined != "" {
			joined += ";"
		}
		if err := setUserPath(joined + dir); err != nil {
			return Status{}, fmt.Errorf("写入用户 PATH 失败: %w", err)
		}
	}
	return Current(), nil
}

// Uninstall 删除安装目录并把安装目录从用户 PATH 移除。
func Uninstall() (Status, error) {
	// 删上一级（%LOCALAPPDATA%\Programs\opsbox），一并清掉可能的临时文件
	if err := os.RemoveAll(filepath.Dir(InstallDir())); err != nil {
		return Status{}, fmt.Errorf("删除安装目录失败: %w", err)
	}
	entries := userPathEntries()
	kept := make([]string, 0, len(entries))
	changed := false
	for _, entry := range entries {
		if strings.EqualFold(entry, InstallDir()) {
			changed = true
			continue
		}
		kept = append(kept, entry)
	}
	if changed {
		if err := setUserPath(strings.Join(kept, ";")); err != nil {
			return Status{}, fmt.Errorf("写回用户 PATH 失败: %w", err)
		}
	}
	return Current(), nil
}

// userPathEntries 与 setUserPath 在 climgr_windows.go / climgr_other.go 中按平台实现：
// Windows 读写 HKCU\Environment，其他平台返回空（当前产品仅发布 Windows）。
