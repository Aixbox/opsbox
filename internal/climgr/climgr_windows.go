//go:build windows

package climgr

import (
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

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

// setUserPath 写回用户 PATH（HKCU\Environment）。已打开的终端不会感知，需重开终端生效。
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
