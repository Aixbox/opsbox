//go:build !windows

package climgr

// 非 Windows 平台暂不支持应用内安装（当前产品仅发布 Windows）。

func userPathEntries() []string   { return nil }
func setUserPath(string) error    { return nil }
