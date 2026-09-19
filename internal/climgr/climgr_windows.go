//go:build windows

package climgr

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// cliBinaryNames：Windows 分发的可执行文件带 .exe 后缀。
func cliBinaryNames() []string { return []string{"sshctl.exe", "sqlctl.exe", "redisctl.exe"} }

// installDir：%LOCALAPPDATA%\Programs\opsbox\bin（用户级，无需管理员权限）。
func installDir() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "opsbox", "bin")
}

// userPathEntries 返回用户 PATH（HKCU\Environment）的条目列表（空条目剔除）。
func userPathEntries() []string {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer key.Close()
	raw, _, err := key.GetStringValue("Path")
	if err != nil {
		return nil
	}
	var entries []string
	for _, entry := range strings.Split(raw, ";") {
		if entry = strings.TrimSpace(entry); entry != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

// pathConfigured 判断 dir 是否已在用户 PATH（HKCU\Environment）中。
// Windows 路径大小写不敏感，比较用 EqualFold。
func pathConfigured(dir string) bool {
	for _, entry := range userPathEntries() {
		if strings.EqualFold(entry, dir) {
			return true
		}
	}
	return false
}

// addUserPath 把 dir 追加到用户 PATH（HKCU\Environment）。已打开的终端不会感知，
// 需重开终端生效。
func addUserPath(dir string) error {
	joined := strings.Join(userPathEntries(), ";")
	if joined != "" {
		joined += ";"
	}
	return setUserPath(joined + dir)
}

// removeUserPath 把 dir 从用户 PATH（HKCU\Environment）移除（幂等）。
func removeUserPath(dir string) error {
	entries := userPathEntries()
	kept := make([]string, 0, len(entries))
	changed := false
	for _, entry := range entries {
		if strings.EqualFold(entry, dir) {
			changed = true
			continue
		}
		kept = append(kept, entry)
	}
	if !changed {
		return nil
	}
	return setUserPath(strings.Join(kept, ";"))
}

// removePathEntry 与 removeUserPath 同义：冲突目录同样存于注册表 PATH，可直接写回。
func removePathEntry(dir string) error { return removeUserPath(dir) }

// setUserPath 写回用户 PATH（HKCU\Environment）。
func setUserPath(value string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if err := key.SetStringValue("Path", value); err != nil {
		return err
	}
	// 广播环境变量变更，让资源管理器等感知（新进程由此继承新 PATH）
	return notifyEnvironmentChange()
}

// notifyEnvironmentChange 广播 WM_SETTINGCHANGE，新启动的进程由此读到新 PATH。
func notifyEnvironmentChange() error {
	sendMessageTimeout := windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
	const (
		hwndBroadcast   = 0xFFFF
		wmSettingChange = 0x001A
		smtoAbortIfHung = 0x0002
	)
	env, err := windows.UTF16PtrFromString("Environment")
	if err != nil {
		return err
	}
	ret, _, _ := sendMessageTimeout.Call(
		hwndBroadcast,
		wmSettingChange,
		0,
		uintptr(unsafe.Pointer(env)),
		smtoAbortIfHung,
		1000,
		0,
	)
	if ret == 0 {
		return syscall.EINVAL
	}
	return nil
}
