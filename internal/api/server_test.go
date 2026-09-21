package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
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

// TestUIShow 验证：/ui/show 触发宿主回调；未注册回调时返回 503。
func TestUIShow(t *testing.T) {
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

	// 未注册 hook → 503
	resp, err := http.Post(base+"/ui/show", "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("no-hook status = %d, want 503", resp.StatusCode)
	}

	// 注册 hook → 200 且回调被调用
	called := make(chan struct{}, 1)
	server.SetShowUI(func() { called <- struct{}{} })
	resp, err = http.Post(base+"/ui/show", "application/json", nil)
	if err != nil {
		t.Fatalf("post with hook: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("hooked status = %d, want 200", resp.StatusCode)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("showUI hook was not called")
	}
}

// TestWSEndpointsRegistered 验证：三模块的 WS 端点都已注册、凭 ticket 鉴权。
// 用伪造 ticket 请求 /ws，应到达 handler 并被鉴权拒绝（403/409），而不是 404（路由没注册）。
// 回归背景：SSH 的 WS 路由曾注册在从未被调用的方法里，终端一律「会话已结束」，
// 前端兜底文案掩盖了 404 真相——本测试确保这类问题在 CI 就暴露。
// 普通 GET 即可：ticket 校验先于 WebSocket 升级，不需要带 Upgrade 头。
func TestWSEndpointsRegistered(t *testing.T) {
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
		name string
		path string
		want int
	}{
		{"SSH 终端 WS", "/api/v1/ssh/connections/1/sessions/1/ws?ticket=bogus", http.StatusForbidden},
		// SQL 的 fail() 把 ErrForbidden 与 ErrConflict 归入同一个 409 case
		{"SQL 控制台 WS", "/api/v1/sql/connections/1/sessions/1/ws?ticket=bogus", http.StatusConflict},
		{"Redis 控制台 WS", "/api/v1/redis/connections/1/sessions/1/ws?ticket=bogus", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(base + tc.path)
			if err != nil {
				t.Fatalf("GET %s: %v", tc.path, err)
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			if resp.StatusCode == http.StatusNotFound {
				t.Fatalf("%s 返回 404：WS 路由未注册", tc.path)
			}
			if resp.StatusCode != tc.want {
				t.Fatalf("%s status = %d, want %d", tc.path, resp.StatusCode, tc.want)
			}
		})
	}
}

// TestTestByParamsEndpoints 验证三模块的 POST /connections/test（添加 / 编辑抽屉的「测试连接」）：
// 路由与 :id/test 共存、JSON 绑定、编辑回退（fromId 不存在 → 404）、上游错误映射（指向关闭端口 → 502）。
func TestTestByParamsEndpoints(t *testing.T) {
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

	post := func(path, payload string) (int, []byte) {
		resp, err := http.Post(base+path, "application/json", strings.NewReader(payload))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return resp.StatusCode, body
	}

	cases := []struct {
		name       string
		path       string
		payload    string
		mergeEmpty string // 不带凭证字段的 payload：命中 fromId 回退分支（不存在 → 404）
	}{
		{"sql", "/api/v1/sql/connections/test", `{"engine":"mysql","host":"127.0.0.1","port":1,"username":"root","password":"x","database":"app"}`, `{"engine":"mysql","host":"127.0.0.1","port":1,"username":"root","database":"app"}`},
		{"redis", "/api/v1/redis/connections/test", `{"name":"t","host":"127.0.0.1","port":1,"db":0,"writePolicy":"confirm"}`, `{"name":"t","host":"127.0.0.1","port":1,"db":0,"writePolicy":"confirm"}`},
		{"ssh", "/api/v1/ssh/connections/test", `{"name":"t","host":"127.0.0.1","port":1,"username":"root","authType":"password","password":"x"}`, `{"name":"t","host":"127.0.0.1","port":1,"username":"root","authType":"password"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/closed-port", func(t *testing.T) {
			status, body := post(tc.path, tc.payload)
			if status != http.StatusBadGateway {
				t.Fatalf("status = %d body = %s，期望 502（上游不可达）", status, body)
			}
		})
		t.Run(tc.name+"/missing-fromId", func(t *testing.T) {
			status, body := post(tc.path+"?fromId=999", tc.mergeEmpty)
			if status != http.StatusNotFound {
				t.Fatalf("status = %d body = %s，期望 404（回退的连接不存在）", status, body)
			}
		})
	}
}

// TestTestTargetEditFallback 复现用户反馈「添加里测试正常、编辑里测试失败」：
// 真实创建带密码的连接，再用编辑抽屉的请求形态（fromId + 不带凭证字段）测试。
// 回退分支应与添加场景一样走到上游拨号（关闭端口 → 502，错误信息一致），
// 而不是在中途以 500/422 等不同方式失败。
func TestTestTargetEditFallback(t *testing.T) {
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

	post := func(path, payload string) (int, []byte) {
		resp, err := http.Post(base+path, "application/json", strings.NewReader(payload))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return resp.StatusCode, body
	}
	dataID := func(t *testing.T, body []byte) string {
		var envelope struct {
			Code string `json:"code"`
			Data struct {
				ID int64 `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil || envelope.Code != "OK" {
			t.Fatalf("create envelope = %s", body)
		}
		return itoa(int(envelope.Data.ID))
	}

	cases := []struct {
		name       string
		createPath string
		createBody string
		testPath   string
		editBody   string // 编辑抽屉形态：凭证字段整个不发
		addBody    string // 添加抽屉形态：凭证随表单发送
	}{
		{
			name:       "sql",
			createPath: "/api/v1/sql/connections",
			createBody: `{"name":"prod","engine":"mysql","host":"127.0.0.1","port":1,"username":"root","password":"s3cret","database":"app","writePolicy":"confirm"}`,
			testPath:   "/api/v1/sql/connections/test",
			editBody:   `{"name":"prod","engine":"mysql","host":"127.0.0.1","port":1,"username":"root","database":"app","writePolicy":"confirm"}`,
			addBody:    `{"name":"prod2","engine":"mysql","host":"127.0.0.1","port":1,"username":"root","password":"s3cret","database":"app","writePolicy":"confirm"}`,
		},
		{
			name:       "redis",
			createPath: "/api/v1/redis/connections",
			createBody: `{"name":"cache","host":"127.0.0.1","port":1,"db":0,"password":"s3cret","writePolicy":"confirm"}`,
			testPath:   "/api/v1/redis/connections/test",
			editBody:   `{"name":"cache","host":"127.0.0.1","port":1,"db":0,"writePolicy":"confirm"}`,
			addBody:    `{"name":"cache2","host":"127.0.0.1","port":1,"db":0,"password":"s3cret","writePolicy":"confirm"}`,
		},
		{
			name:       "ssh",
			createPath: "/api/v1/ssh/connections",
			createBody: `{"name":"web","host":"127.0.0.1","port":1,"username":"root","authType":"password","password":"s3cret","execPolicy":"audit"}`,
			testPath:   "/api/v1/ssh/connections/test",
			editBody:   `{"name":"web","host":"127.0.0.1","port":1,"username":"root","authType":"password","execPolicy":"audit"}`,
			addBody:    `{"name":"web2","host":"127.0.0.1","port":1,"username":"root","authType":"password","password":"s3cret","execPolicy":"audit"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := post(tc.createPath, tc.createBody)
			if status != http.StatusCreated {
				t.Fatalf("create status = %d body = %s", status, body)
			}
			id := dataID(t, body)

			// 编辑场景：fromId + 不带凭证 → 应与添加场景同样到达上游拨号
			editStatus, editBody := post(tc.testPath+"?fromId="+id, tc.editBody)
			addStatus, addBody := post(tc.testPath, tc.addBody)
			if editStatus != addStatus {
				t.Fatalf("编辑测试 status = %d body = %s；添加测试 status = %d body = %s；两者应一致",
					editStatus, editBody, addStatus, addBody)
			}
			if editStatus != http.StatusBadGateway {
				t.Fatalf("status = %d（编辑 body = %s），期望两者同为 502（关闭端口）", editStatus, editBody)
			}
			if string(editBody) != string(addBody) {
				t.Fatalf("编辑测试错误信息与添加不一致：edit = %s, add = %s", editBody, addBody)
			}
		})
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
