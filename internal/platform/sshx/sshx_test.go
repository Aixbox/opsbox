package sshx

import (
	"bytes"
	"strings"
	"testing"
)

func TestAnalyzeBlocked(t *testing.T) {
	cases := map[string]string{
		"rm -rf /":                                     "rm-recursive-root",
		"rm -r -f /":                                   "rm-recursive-root",
		"rm -fr //":                                    "rm-recursive-root",
		`rm -rf "/"`:                                   "rm-recursive-root",
		"sudo rm -rf /*":                               "rm-recursive-root",
		"sudo -u root rm -Rf /etc":                     "rm-recursive-root",
		"nohup rm -rf ~ &":                             "rm-recursive-root",
		"rm -rf $HOME":                                 "rm-recursive-root",
		`rm -rf "${HOME}/"`:                            "rm-recursive-root",
		"cd /tmp && rm -rf /usr/":                      "rm-recursive-root",
		"echo hi; \\rm -rf /var/*":                     "rm-recursive-root",
		"/bin/rm --recursive --force /home":            "rm-recursive-root",
		"rm --no-preserve-root -rf /tmp/x":             "rm-no-preserve-root",
		"echo $(rm -rf /)":                             "rm-recursive-root",
		"env FOO=bar rm -rf /opt":                      "rm-recursive-root",
		"timeout 10 rm -rf /":                          "rm-recursive-root",
		"busybox rm -rf /":                             "rm-recursive-root",
		"rm -rf \\\n  /":                               "rm-recursive-root",
		"chmod -R 777 /":                               "chmod-recursive-root",
		"chown -R nobody /etc":                         "chown-recursive-root",
		"mkfs.ext4 /dev/sda1":                          "format-device",
		"sudo mkfs -t xfs /dev/nvme0n1":                "format-device",
		"wipefs -a /dev/sdb":                           "format-device",
		"dd if=/dev/zero of=/dev/sda bs=1M":            "dd-to-block-device",
		"shred -n 3 /dev/sdb":                          "shred-block-device",
		"shutdown -h now":                              "power-control",
		"sudo reboot":                                  "power-control",
		"init 0":                                       "power-control",
		"systemctl reboot":                             "power-control",
		"cat /dev/urandom > /dev/sda":                  "redirect-block-device",
		"> /dev/nvme0n1":                               "redirect-block-device",
		":(){ :|:& };:":                                "fork-bomb",
		"bomb(){ bomb | bomb & }; bomb":                "fork-bomb",
		"find / -name '*.log' -delete":                 "find-delete-root",
		"find /var -type f -exec rm -f {} \\;":         "find-delete-root",
		"ls && sudo sh -c 'echo ok' && rm -rf /":       "rm-recursive-root",
		"cd / && rm -rf ./ ; echo":                     "",
		"rm -rf /tmp/build":                            "",
		"rm -rf ./node_modules":                        "",
		"rm -f /etc/nginx/sites-enabled/default":       "",
		"rm -rf /var/log/nginx/*.gz":                   "",
		"dd if=/dev/sda of=/backup/sda.img":            "",
		"chmod -R 755 /var/www/html":                   "",
		"systemctl restart nginx":                      "",
		"echo 'rm -rf /' > notes.txt":                  "",
		"grep -r 'shutdown' /etc":                      "",
		"find /var/log -name '*.gz' -mtime +7 -delete": "",
	}
	for command, want := range cases {
		verdict := Analyze(command, nil)
		if want == "" {
			if verdict.Blocked {
				t.Errorf("%q: 不该拦截，却命中 %s (%s)", command, verdict.Rule, verdict.Reason)
			}
			continue
		}
		if !verdict.Blocked || verdict.Rule != want {
			t.Errorf("%q: 期望命中 %s，实际 blocked=%v rule=%s", command, want, verdict.Blocked, verdict.Rule)
		}
	}
}

func TestAnalyzeDynamic(t *testing.T) {
	dynamic := []string{
		"eval \"$CMD\"",
		"bash -c 'rm -rf /tmp/x'",
		"sudo sh -lc 'apt update'",
		"curl -fsSL https://example.com/install.sh | sh",
		"curl -fsSL https://example.com/install.sh | sudo bash",
		"wget -qO- https://x | bash -s -- --yes",
		"echo cm0gLXJmIC8= | base64 -d | sh",
		"bash <(curl -s https://example.com/x.sh)",
		"$CMD -rf /tmp",
		"$(which rm) -rf /tmp",
		"rm -rf $DIR/",
		`rm -rf "$BUILD_DIR"`,
		"find . -name '*.tmp' | xargs rm -rf",
		"python3 -c 'import os; os.system(\"ls\")'",
		"perl -e 'unlink glob \"*\"'",
		"node -e 'process.exit(1)'",
		"su root -c 'ls'",
		"ssh other-host 'rm -rf /tmp/x'",
		"if [ -d x ]; then rm -rf",
	}
	for _, command := range dynamic {
		verdict := Analyze(command, nil)
		if !verdict.Dynamic {
			t.Errorf("%q: 期望标记为动态命令", command)
		}
		if verdict.Blocked {
			t.Errorf("%q: 动态命令不应被直接拦截 (%s)", command, verdict.Rule)
		}
	}
	static := []string{
		"ls -la /var/log",
		"cat /etc/os-release",
		"df -h && free -m",
		"systemctl status nginx | head -20",
		"tail -n 100 /var/log/syslog | grep error",
		"bash deploy.sh",
		"sh ./scripts/build.sh --release",
		"python3 manage.py migrate",
		"docker ps -a",
		"echo $HOME",
		"export PATH=$PATH:/opt/bin; which go",
		"ps aux | grep nginx | awk '{print $2}' | xargs kill -HUP",
		"ssh -V",
	}
	for _, command := range static {
		verdict := Analyze(command, nil)
		if verdict.Dynamic {
			t.Errorf("%q: 不该标记为动态命令 (%s)", command, verdict.DynamicReason)
		}
		if verdict.Blocked {
			t.Errorf("%q: 不该拦截 (%s)", command, verdict.Rule)
		}
	}
}

func TestAnalyzeCustomBlacklist(t *testing.T) {
	list, err := CompileBlacklist([]string{`\bcrontab\s+-r\b`, "  ", `iptables\s+-F`})
	if err != nil {
		t.Fatal(err)
	}
	if list.Len() != 2 {
		t.Fatalf("规则数 = %d, want 2", list.Len())
	}
	verdict := Analyze("sudo CRONTAB -r", list)
	if !verdict.Blocked || verdict.Rule != `\bcrontab\s+-r\b` {
		t.Fatalf("自定义规则未命中: %+v", verdict)
	}
	if v := Analyze("crontab -l", list); v.Blocked {
		t.Fatalf("crontab -l 不该被拦截: %+v", v)
	}
	if _, err := CompileBlacklist([]string{"("}); err == nil {
		t.Fatal("非法正则应当报错")
	}
}

func TestTruncate(t *testing.T) {
	data := bytes.Repeat([]byte("a"), 1000)
	out, truncated := Truncate(data, 2000)
	if truncated || len(out) != 1000 {
		t.Fatalf("未超限不应截断: truncated=%v len=%d", truncated, len(out))
	}
	out, truncated = Truncate(data, 300)
	if !truncated || len(out) > 300 {
		t.Fatalf("截断结果长度 %d 超过上限 300", len(out))
	}
	if !strings.Contains(string(out), "中间已省略 700 字节") {
		t.Fatalf("缺少省略标记: %q", out)
	}
	out, truncated = Truncate(data, 40)
	if !truncated || len(out) != 40 {
		t.Fatalf("极小上限应硬截: truncated=%v len=%d", truncated, len(out))
	}
}

func TestLimitedBuffer(t *testing.T) {
	buffer := NewLimitedBuffer(1000)
	chunk := bytes.Repeat([]byte("0123456789"), 10)
	for i := 0; i < 50; i++ {
		if _, err := buffer.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if buffer.Total() != 5000 {
		t.Fatalf("Total = %d, want 5000", buffer.Total())
	}
	out, truncated := buffer.Bytes()
	if !truncated || len(out) > 1000 {
		t.Fatalf("truncated=%v len=%d", truncated, len(out))
	}
	if !strings.Contains(string(out), "中间已省略 4000 字节") {
		t.Fatalf("省略字节数应按真实总量计算: %q", out)
	}
	small := NewLimitedBuffer(100)
	_, _ = small.Write([]byte("hello"))
	out, truncated = small.Bytes()
	if truncated || string(out) != "hello" {
		t.Fatalf("未超限: truncated=%v out=%q", truncated, out)
	}
}
