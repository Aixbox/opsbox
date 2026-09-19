// Package climgr 管理运维 CLI（sshctl / sqlctl / redisctl）的应用内安装：
// CLI 二进制在 wails build 前由构建脚本（Windows: scripts/build-all.ps1，
// macOS: scripts/build-all.sh）填充到 clis/ 目录并 go:embed 进主程序，
// 用户在 opsbox 界面一键安装到用户级目录并配置 PATH——产品用户不需要
// 源码、脚本或手动改环境变量。
//
// 平台差异收口在 climgr_windows.go / climgr_darwin.go / climgr_other.go：
//   - cliBinaryNames / installDir：可执行文件名与安装目录
//   - pathConfigured / addUserPath / removeUserPath：用户级 PATH 的读写
//     （Windows = HKCU\Environment 注册表；macOS = ~/.zshenv 标记块）
//   - userPathEntries：PATH 条目清单（冲突扫描用）
//   - removePathEntry：清理冲突目录的 PATH 条目（Windows 注册表可写回；
//     macOS 上无法改写进程/系统 PATH，为 no-op）
package climgr

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed all:clis
var clisFS embed.FS

// cliNames 是随主程序分发的 CLI 可执行文件（按平台命名，见 cliBinaryNames）。
var cliNames = cliBinaryNames()

// InstallDir 返回 CLI 安装目标目录（用户级，无需管理员权限）。
func InstallDir() string { return installDir() }

// Status 是 CLI 安装状态，供前端「AI CLI」面板展示。
type Status struct {
	// Bundled 表示当前主程序是否内嵌了 CLI 二进制（开发者本地只跑 go build 时可能为 false）。
	Bundled bool `json:"bundled"`
	// Installed 表示安装目录里三个 CLI 是否齐全。
	Installed bool `json:"installed"`
	// InPath 表示安装目录是否已在用户 PATH 配置中。
	InPath bool `json:"inPath"`
	// Version 是内嵌 CLI 的版本（构建时注入）。
	Version string `json:"version"`
	// InstallDir 是安装目录。
	InstallDir string `json:"installDir"`
	// Conflicts 是 PATH 中其他存在的同名 CLI 所在目录（如旧平台 padmin），按 PATH 顺序可能遮蔽新命令。
	Conflicts []string `json:"conflicts,omitempty"`
}

// Version 返回内嵌 CLI 版本号（构建脚本写入 clis/VERSION）。
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

// Current 汇总当前安装状态。
func Current() Status {
	dir := InstallDir()
	installed := true
	for _, name := range cliNames {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			installed = false
			break
		}
	}
	var conflicts []string
	for _, entry := range userPathEntries() {
		if strings.EqualFold(entry, dir) {
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
		InPath:     pathConfigured(dir),
		Version:    Version(),
		InstallDir: dir,
		Conflicts:  conflicts,
	}
}

// Install 释放内嵌 CLI 到安装目录并确保在用户 PATH 配置中（幂等，重复执行即升级覆盖）。
func Install() (Status, error) {
	if !Bundled() {
		return Status{}, fmt.Errorf("当前 opsbox 程序未内嵌 CLI（开发者请先运行构建脚本 scripts/build-all.ps1 或 build-all.sh 重新构建）")
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
	if !pathConfigured(dir) {
		if err := addUserPath(dir); err != nil {
			return Status{}, fmt.Errorf("写入用户 PATH 失败: %w", err)
		}
	}
	return Current(), nil
}

// Uninstall 删除安装目录并从用户 PATH 配置移除安装目录条目。
func Uninstall() (Status, error) {
	// 删上一级安装根目录，一并清掉可能的临时文件
	if err := os.RemoveAll(filepath.Dir(InstallDir())); err != nil {
		return Status{}, fmt.Errorf("删除安装目录失败: %w", err)
	}
	if err := removeUserPath(InstallDir()); err != nil {
		return Status{}, fmt.Errorf("写回用户 PATH 失败: %w", err)
	}
	return Current(), nil
}

// TakeOverConflicts 清理 PATH 中遮蔽 opsbox 命令的旧版同名 CLI（如旧平台 padmin）：
// 删除冲突目录里的三个同名可执行文件，目录因此清空则一并移除，并从用户 PATH
// 配置去掉该目录。只动与三个 CLI 同名的文件，目录里的其他内容不受影响。
func TakeOverConflicts() (Status, error) {
	conflicts := currentConflicts()
	for _, dir := range conflicts {
		if err := takeOverDir(dir); err != nil {
			return Current(), fmt.Errorf("清理 %s 失败: %w", dir, err)
		}
		// 目录里已没有同名命令时，PATH 条目才移除；否则保留（用户可能还有别的用途）
		if dirHasCLI(dir) {
			continue
		}
		if err := removePathEntry(dir); err != nil {
			return Current(), fmt.Errorf("更新用户 PATH 失败: %w", err)
		}
	}
	return Current(), nil
}

// currentConflicts 返回当前 PATH 中存在同名 CLI 的其他目录（去重）。
func currentConflicts() []string {
	var out []string
	seen := map[string]bool{}
	for _, entry := range userPathEntries() {
		if strings.EqualFold(entry, InstallDir()) || seen[strings.ToLower(entry)] {
			continue
		}
		if dirHasCLI(entry) {
			out = append(out, entry)
			seen[strings.ToLower(entry)] = true
		}
	}
	return out
}

// dirHasCLI 判断目录里是否存在任一同名 CLI。
func dirHasCLI(dir string) bool {
	for _, name := range cliNames {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// takeOverDir 删除目录里的三个同名 CLI 可执行文件；目录因此变空则整个移除。
func takeOverDir(dir string) error {
	for _, name := range cliNames {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	rest, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(rest) == 0 {
		return os.Remove(dir)
	}
	return nil
}
