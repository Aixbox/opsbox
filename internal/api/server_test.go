package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"opsbox/internal/platform/security"
	"opsbox/internal/store"
)

// TestServerEndToEnd 验证：建库迁移 → 装配路由 → 监听 → healthz 与 SSH 连接列表可用。
func TestServerEndToEnd(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	cipher, err := security.NewTokenCipher("test-secret")
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	server, err := New(db, cipher, slog.Default(), "test")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	// 0 = 让系统挑空闲端口，避免测试环境端口冲突
	if err := server.Listen(0); err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = server.Serve() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	base := "http://127.0.0.1:" + itoa(server.Port())

	get := func(path string) (int, []byte) {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return resp.StatusCode, body
	}

	status, body := get("/healthz")
	if status != http.StatusOK {
		t.Fatalf("healthz status = %d", status)
	}
	var health struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &health); err != nil || health.Status != "ok" {
		t.Fatalf("healthz payload = %s", body)
	}

	status, body = get("/api/v1/ssh/connections")
	if status != http.StatusOK {
		t.Fatalf("connections status = %d body = %s", status, body)
	}
	var envelope struct {
		Code string `json:"code"`
		Data struct {
			Items []struct {
				ID int64 `json:"id"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Code != "OK" {
		t.Fatalf("connections envelope = %s", body)
	}

	status, _ = get("/api/v1/ssh/settings")
	if status != http.StatusOK {
		t.Fatalf("settings status = %d", status)
	}

	status, _ = get("/api/v1/sql/connections")
	if status != http.StatusOK {
		t.Fatalf("sql connections status = %d", status)
	}
	status, _ = get("/api/v1/sql/settings")
	if status != http.StatusOK {
		t.Fatalf("sql settings status = %d", status)
	}
	status, _ = get("/api/v1/sql/pending-queries")
	if status != http.StatusOK {
		t.Fatalf("sql pending-queries status = %d", status)
	}

	status, _ = get("/api/v1/redis/connections")
	if status != http.StatusOK {
		t.Fatalf("redis connections status = %d", status)
	}
	status, _ = get("/api/v1/redis/settings")
	if status != http.StatusOK {
		t.Fatalf("redis settings status = %d", status)
	}
	status, _ = get("/api/v1/redis/pending-commands")
	if status != http.StatusOK {
		t.Fatalf("redis pending-commands status = %d", status)
	}
}

// TestOriginGuard 验证：非本地来源的浏览器请求被拒，CLI（无 Origin）与本地 UI 放行。
func TestOriginGuard(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	cipher, err := security.NewTokenCipher("test-secret")
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	server, err := New(db, cipher, slog.Default(), "test")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	if err := server.Listen(0); err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = server.Serve() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	base := "http://127.0.0.1:" + itoa(server.Port())

	cases := []struct {
		name   string
		origin string
		want   int
	}{
		{"无 Origin（CLI）", "", http.StatusOK},
		{"Wails WebView", "http://wails.localhost", http.StatusOK},
		{"本机 dev server", "http://localhost:5173", http.StatusOK},
		{"file://", "null", http.StatusOK},
		{"恶意网页", "https://evil.example.com", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, base+"/api/v1/ssh/connections", nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			resp, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != tc.want {
				t.Fatalf("Origin %q: status = %d, want %d", tc.origin, resp.StatusCode, tc.want)
			}
		})
	}
}

// TestApproveRequiresWebUI 验证：CLI（无 Origin）不能调用 approve——批准权只在 UI。
func TestApproveRequiresWebUI(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	cipher, err := security.NewTokenCipher("test-secret")
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	server, err := New(db, cipher, slog.Default(), "test")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	if err := server.Listen(0); err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = server.Serve() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	base := "http://127.0.0.1:" + itoa(server.Port())

	// 用一个不存在的命令 ID：CLI 来源应在进入业务前就被 403，而不是 404
	request, err := http.NewRequest(http.MethodPost, base+"/api/v1/ssh/commands/999/approve", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cli approve status = %d, want 403", resp.StatusCode)
	}

	// UI 来源（带本地 Origin）应通过守卫进入业务（这里是 404：ID 不存在）
	request.Header.Set("Origin", "http://wails.localhost")
	resp, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do web: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("web approve status = %d, want 404", resp.StatusCode)
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}
