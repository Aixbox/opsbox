package sshx

import (
	"context"
	"errors"
	"testing"

	"opsbox/internal/platform/sshx/sshtest"
)

// TestDialHostKeyMismatch：已记录指纹与实际不符时拒绝连接，且错误可被 errors.Is 识别（handler 据此回 502）。
func TestDialHostKeyMismatch(t *testing.T) {
	server, err := sshtest.Start("u", "p")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	_, err = NetDialer{}.Dial(context.Background(), Target{Host: "127.0.0.1", Port: server.Port, Username: "u", AuthType: "password", Password: "p", HostKey: "SHA256:bogus"})
	if err == nil {
		t.Fatal("应报错")
	}

	if !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("errors.Is 匹配失败: %v", err)
	}
}
