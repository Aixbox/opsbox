package console

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// 会话名称规整：首尾空白去掉、空串表示恢复默认、超长与控制字符一律拒绝。
func TestNormalizeName(t *testing.T) {
	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{"  部署  ", "部署", true},
		{"", "", true},
		{"   ", "", true},
		{strings.Repeat("长", MaxNameLength), strings.Repeat("长", MaxNameLength), true},
		{strings.Repeat("长", MaxNameLength+1), "", false},
		{"a\tb", "", false},
		{"a\nb", "", false},
	}
	for _, c := range cases {
		got, err := NormalizeName(c.input)
		if c.ok {
			if err != nil || got != c.want {
				t.Fatalf("NormalizeName(%q) = %q, %v; want %q", c.input, got, err, c.want)
			}
			continue
		}
		if !errors.Is(err, ErrInvalidName) {
			t.Fatalf("NormalizeName(%q) 应报 ErrInvalidName，实际 %v", c.input, err)
		}
	}
}

// 会话是持久的：所有客户端 Detach 后仍在列表里，名字也还在；只有 Close 才把它移除。
func TestSessionSurvivesDetachAndKeepsName(t *testing.T) {
	manager := NewManager("redis", time.Minute)
	session := manager.Create(7, 42)
	if session.Seq != 1 {
		t.Fatalf("seq = %d, want 1", session.Seq)
	}
	session.SetName("缓存排查")
	if !session.Attach() {
		t.Fatal("接入失败")
	}
	session.Detach()
	if session.AttachCount() != 0 || session.IsClosed() {
		t.Fatalf("Detach 后会话应保留: attached=%d closed=%v", session.AttachCount(), session.IsClosed())
	}
	found := manager.FindByConnection(7, 42)
	if len(found) != 1 || found[0].Name() != "缓存排查" {
		t.Fatalf("无人接入的会话仍应在列表里且名字不丢: %+v", found)
	}
	if err := manager.Close(session.ID); err != nil {
		t.Fatal(err)
	}
	if got := manager.FindByConnection(7, 42); len(got) != 0 {
		t.Fatalf("Close 后不应再列出, got %d", len(got))
	}
}
