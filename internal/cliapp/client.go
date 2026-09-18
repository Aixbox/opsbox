// Package cliapp 是 opsbox 三个运维 CLI（sshctl / sqlctl / redisctl）的共享骨架：
// 本地服务 HTTP 客户端（端口自动发现）、审批预检协议与统一退出码。
//
// opsbox 是本地单用户应用：CLI 与桌面 UI 走同一个 127.0.0.1 服务，
// 无登录令牌——安全边界是「本机进程」+ 会话即授权 + 操作级闸门（黑名单 / 审批队列）。
package cliapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 统一退出码（三个 CLI 一致，AI 文档据此判断）。
const (
	ExitOK       = 0
	ExitGeneric  = 1 // 远端命令失败但无退出码 / 其他错误
	ExitRejected = 2 // 被黑名单拦截 / 被拒绝 / 等待批准超时
	ExitTimeout  = 3 // 远端命令超时
	ExitAPI      = 4 // 本地服务 API 错误（服务未启动、请求无效、网络）
	// ExitApprovalNeeded 表示该操作需要平台审批且尚未取得用户确认（退出码 5）。
	// 与退出码 2 的区别：2 是「停止，不要重试」，5 是「先向用户展示命令并取得确认，
	// 再携带 --confirm-token 重新执行同一条命令」。错误输出里已给出预检令牌与指引。
	ExitApprovalNeeded = 5
)

const (
	// PreferredBasePort 与服务端默认端口一致；被占用时服务端向上顺延，CLI 探测同一范围。
	PreferredBasePort = 37421
	// PortScanCount 是端口探测数量（与服务端顺延上限一致）。
	PortScanCount = 25
)

// ExitError 携带退出码的错误；Run 据此设置进程退出码。
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

// Exit 构造 ExitError。
func Exit(code int, format string, args ...any) error {
	return &ExitError{Code: code, Err: fmt.Errorf(format, args...)}
}

// APIError 是服务返回的错误信封。
type APIError struct {
	Status    int
	Code      string
	Message   string
	RequestID string
	// Data 是错误响应信封里的 data 字段（原始 JSON）。
	// 审批预检（*_CHECK_REQUIRED）会把预检结果放这里，用 DecodeAPIErrorData 解出。
	Data json.RawMessage
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s (%s, HTTP %d)", e.Message, e.Code, e.Status)
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// Client 是本地 opsbox 服务的 API 客户端。
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient 构造客户端。地址优先级：--url 参数 → OPSBOX_URL 环境变量 → 发现文件 → 端口探测。
func NewClient(urlOverride string) (*Client, error) {
	baseURL := strings.TrimSpace(urlOverride)
	if baseURL == "" {
		baseURL = strings.TrimSpace(os.Getenv("OPSBOX_URL"))
	}
	if baseURL == "" {
		discovered, err := DiscoverBaseURL()
		if err != nil {
			return nil, Exit(ExitAPI, "未发现本地 opsbox 服务：请先启动 opsbox 桌面应用，或用 --url 指定地址")
		}
		baseURL = discovered
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 0}}, nil
}

// healthInfo 是 /healthz 的身份标识；只有 app == "opsbox" 的服务才被 CLI 认可，
// 防止端口窗口内恰好有其他带健康探针的本地服务被误连。
type healthInfo struct {
	Status  string `json:"status"`
	App     string `json:"app"`
	Version string `json:"version"`
}

// verifyOpsbox 探测 base 上的 /healthz 并校验是 opsbox 服务。
func verifyOpsbox(base string, probe *http.Client) bool {
	response, err := probe.Get(base + "/healthz")
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	var info healthInfo
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<10)).Decode(&info); err != nil {
		return false
	}
	return info.App == "opsbox"
}

// discoveryFilePath 是桌面应用启动时写下的端口发现文件（<UserConfigDir>/opsbox/server.json）。
type discoveryFilePath struct {
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Version string `json:"version"`
}

// readDiscoveryPort 从发现文件读上次记录的端口。文件缺失、损坏或端口非法时返回 0。
func readDiscoveryPort() int {
	base, err := os.UserConfigDir()
	if err != nil {
		return 0
	}
	raw, err := os.ReadFile(filepath.Join(base, "opsbox", "server.json"))
	if err != nil {
		return 0
	}
	var info discoveryFilePath
	if err := json.Unmarshal(raw, &info); err != nil {
		return 0
	}
	if info.Port < 1 || info.Port > 65535 {
		return 0
	}
	return info.Port
}

// DiscoverBaseURL 定位本地 opsbox 服务：
//  1. 读桌面应用写下的发现文件（端口顺延场景 O(1) 命中）；
//  2. 回落为从 37421 起逐口扫描（与服务端顺延范围一致）。
//
// 两条路径都以 /healthz 的 app 身份校验为准，杜绝误连其他本地服务。
func DiscoverBaseURL() (string, error) {
	probe := &http.Client{Timeout: 1200 * time.Millisecond}
	candidates := make([]string, 0, PortScanCount+1)
	if port := readDiscoveryPort(); port > 0 {
		candidates = append(candidates, fmt.Sprintf("http://127.0.0.1:%d", port))
	}
	for offset := 0; offset < PortScanCount; offset++ {
		candidates = append(candidates, fmt.Sprintf("http://127.0.0.1:%d", PreferredBasePort+offset))
	}
	var lastErr error
	for _, base := range candidates {
		response, err := probe.Get(base + "/healthz")
		if err != nil {
			lastErr = err
			continue
		}
		var info healthInfo
		err = json.NewDecoder(io.LimitReader(response.Body, 4<<10)).Decode(&info)
		response.Body.Close()
		if err == nil && info.App == "opsbox" {
			return base, nil
		}
	}
	return "", lastErr
}

type envelope struct {
	Code      string          `json:"code"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
	RequestID string          `json:"requestId"`
}

// Do 发送 JSON 请求；out 非 nil 时解出信封的 data。返回 HTTP 状态码便于区分 200/202。
func (c *Client) Do(ctx context.Context, method, path string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return 0, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return c.send(request, out)
}

// DoRaw 发送原始字节流请求（SFTP 上传）。
func (c *Client) DoRaw(ctx context.Context, method, path string, body io.Reader, contentType string, out any) (int, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", contentType)
	return c.send(request, out)
}

func (c *Client) send(request *http.Request, out any) (int, error) {
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "opsbox-cli")
	response, err := c.HTTP.Do(request)
	if err != nil {
		return 0, Exit(ExitAPI, "请求本地服务失败（opsbox 是否正在运行？）: %v", err)
	}
	defer response.Body.Close()
	return c.parse(response, out)
}

func (c *Client) parse(response *http.Response, out any) (int, error) {
	if response.StatusCode == http.StatusNoContent {
		return response.StatusCode, nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		return response.StatusCode, Exit(ExitAPI, "读取响应失败: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		if response.StatusCode >= 400 {
			return response.StatusCode, &ExitError{Code: ExitAPI, Err: &APIError{Status: response.StatusCode, Message: strings.TrimSpace(string(raw))}}
		}
		return response.StatusCode, Exit(ExitAPI, "响应不是合法 JSON")
	}
	if response.StatusCode >= 400 {
		apiErr := &APIError{Status: response.StatusCode, Code: env.Code, Message: env.Message, RequestID: env.RequestID, Data: env.Data}
		return response.StatusCode, &ExitError{Code: ExitAPI, Err: apiErr}
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return response.StatusCode, Exit(ExitAPI, "解析响应失败: %v", err)
		}
	}
	return response.StatusCode, nil
}

// Download 发起 GET 并返回原始响应（调用方负责关闭 Body）；非 2xx 或 JSON 信封时按 Do 的规则处理。
func (c *Client) Download(ctx context.Context, path string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "opsbox-cli")
	response, err := c.HTTP.Do(request)
	if err != nil {
		return nil, Exit(ExitAPI, "请求本地服务失败: %v", err)
	}
	if response.StatusCode >= 400 {
		defer response.Body.Close()
		_, err := c.parse(response, nil)
		return nil, err
	}
	return response, nil
}

// IsAPIStatus 判断错误是否为指定 HTTP 状态的服务错误。
func IsAPIStatus(err error, status int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == status
}

// CheckOutcome 是审批预检结果（与服务端 opscheck.Outcome 对齐）。
type CheckOutcome struct {
	Action   string `json:"action"` // execute | approval | blocked
	Reason   string `json:"reason,omitempty"`
	Token    string `json:"token,omitempty"`
	Guidance string `json:"guidance"`
}

// APIErrorCode 返回服务错误信封的错误码（如 SSH_CHECK_REQUIRED）；非 API 错误返回空串。
func APIErrorCode(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

// DecodeAPIErrorData 解出服务错误信封 data 字段（如 *_CHECK_REQUIRED 携带的预检结果）。
func DecodeAPIErrorData(err error, out any) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || len(apiErr.Data) == 0 {
		return false
	}
	return json.Unmarshal(apiErr.Data, out) == nil
}

// Sleep 可被 ctx 打断的 sleep。
func Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
