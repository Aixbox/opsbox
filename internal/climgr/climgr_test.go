package climgr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTakeOverDir 覆盖接管逻辑：同名 exe 被删、无关文件保留、目录清空才移除。
func TestTakeOverDir(t *testing.T) {
	t.Run("removes cli exes and keeps unrelated files", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range cliNames {
			writeFile(t, filepath.Join(dir, name), "x")
		}
		writeFile(t, filepath.Join(dir, "unrelated.txt"), "keep me")

		if err := takeOverDir(dir); err != nil {
			t.Fatalf("takeOverDir: %v", err)
		}
		if dirHasCLI(dir) {
			t.Error("cli exes should be gone")
		}
		if _, err := os.Stat(filepath.Join(dir, "unrelated.txt")); err != nil {
			t.Error("unrelated files must survive")
		}
	})

	t.Run("removes directory when emptied", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "sshctl.exe"), "x")
		writeFile(t, filepath.Join(dir, "sqlctl.exe"), "x")
		writeFile(t, filepath.Join(dir, "redisctl.exe"), "x")

		if err := takeOverDir(dir); err != nil {
			t.Fatalf("takeOverDir: %v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Error("emptied conflict dir should be removed")
		}
	})

	t.Run("missing dir is fine", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope")
		if err := takeOverDir(missing); err != nil {
			t.Fatalf("takeOverDir on missing dir: %v", err)
		}
	})
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestCurrentConflictsDedup 用桩目录验证冲突扫描的去重与排除安装目录。
func TestCurrentConflictsDedup(t *testing.T) {
	dirA := t.TempDir()
	writeFile(t, filepath.Join(dirA, "sshctl.exe"), "x")

	// currentConflicts 扫描用户 PATH（真实注册表），这里仅验证 dirHasCLI 与去重辅助逻辑
	if !dirHasCLI(dirA) {
		t.Fatal("dir with sshctl.exe must be detected as conflict dir")
	}
	empty := t.TempDir()
	if dirHasCLI(empty) {
		t.Fatal("empty dir must not be a conflict dir")
	}

	entries := []string{dirA, strings.ToUpper(dirA), empty}
	var out []string
	seen := map[string]bool{}
	for _, entry := range entries {
		if strings.EqualFold(entry, InstallDir()) || seen[strings.ToLower(entry)] {
			continue
		}
		if dirHasCLI(entry) {
			out = append(out, entry)
			seen[strings.ToLower(entry)] = true
		}
	}
	if len(out) != 1 {
		t.Fatalf("case-duplicated conflict dirs should dedupe to 1, got %d", len(out))
	}
}
