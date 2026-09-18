// sshctl 是给 AI（及人）用的 SSH 运维 CLI：执行、传输与审计都在本地 opsbox 服务里。
//
//	sshctl connections list               # 列出可用连接
//	sshctl sessions list                  # 列出已打开的终端（只有这些能操作）
//	sshctl exec prod -- df -h             # 在已打开终端的连接上执行，退出码透传
//	sshctl upload prod ./a.tgz /tmp/a.tgz # SFTP 上传 / 下载
//	sshctl shell prod                     # 接入已打开的终端（给人用）
//
// 会话即授权：命令在你于 opsbox 窗口打开的终端会话所属的 SSH 连接上另开通道执行，命令与输出实时回显进那个终端；
// 连接没有打开的终端时一律拒绝，用户结束终端即刻失权。CLI 不能自己新建会话。
//
// 退出码：0 成功；远端命令退出码原样透传；2 被黑名单拦截 / 被拒绝 / 等待批准超时 / 无可用会话；3 远端超时；4 本地服务 API 错误；
// 5 该命令需要平台审批且尚未取得用户确认（先向用户展示命令并获得确认，再带 --confirm-token 重跑，见错误输出指引）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"opsbox/internal/cliapp"
)

var serverURL string

// version 由构建注入（-ldflags "-X main.version=…"）。
var version = "dev"

func main() {
	root := &cobra.Command{
		Use:     "sshctl",
		Short:   "opsbox SSH 运维 CLI（命令执行 / SFTP / 终端，全程审计）",
		Version: version,
	}
	root.PersistentFlags().StringVar(&serverURL, "url", "", "覆盖本地服务地址（默认自动探测 127.0.0.1:37421 起；也可用环境变量 OPSBOX_URL）")
	root.AddCommand(connectionsCommand(), execCommand(), statusCommand(), uploadCommand(), downloadCommand(), shellCommand(), sessionsCommand())
	cliapp.Run(root)
}

// ---- 数据结构（与服务端 JSON 对齐） ----

type connection struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	ExecPolicy string `json:"execPolicy"`
	Enabled    bool   `json:"enabled"`
	Remark     string `json:"remark"`
}

type outcome struct {
	ID            int64  `json:"id"`
	Status        string `json:"status"`
	ExitCode      *int   `json:"exitCode,omitempty"`
	Stdout        string `json:"stdout,omitempty"`
	Stderr        string `json:"stderr,omitempty"`
	StdoutBytes   int    `json:"stdoutBytes"`
	StderrBytes   int    `json:"stderrBytes"`
	Truncated     bool   `json:"truncated"`
	DurationMs    int64  `json:"durationMs"`
	Error         string `json:"error,omitempty"`
	BlockedRule   string `json:"blockedRule,omitempty"`
	BlockedReason string `json:"blockedReason,omitempty"`
	Dynamic       bool   `json:"dynamic,omitempty"`
	DynamicReason string `json:"dynamicReason,omitempty"`
	OutputExpired bool   `json:"outputExpired,omitempty"`
}

type execLog struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	Kind   string `json:"kind"`
	Error  string `json:"error"`
}

type sessionInfo struct {
	LogID     int64  `json:"logId"`
	Status    string `json:"status"`
	SessionID string `json:"sessionId"`
	Ticket    string `json:"ticket"`
}

// sessionSummary 是「已打开的终端会话」列表项。会话即授权：列表里没有的连接，exec / 传输都会被拒。
type sessionSummary struct {
	SessionID      string    `json:"sessionId"`
	Seq            int64     `json:"seq"`
	Name           string    `json:"name"`
	ConnectionID   int64     `json:"connectionId"`
	ConnectionName string    `json:"connectionName"`
	Attached       int       `json:"attached"`
	LastSeenAt     time.Time `json:"lastSeenAt"`
}

// sessionName 是给人看的会话名：用户起过名字就用名字，否则退回默认的「会话 #N」。
func sessionName(item sessionSummary) string {
	if item.Name != "" {
		return item.Name
	}
	return fmt.Sprintf("会话 #%d", item.Seq)
}

// ---- helpers ----

func newClient() (*cliapp.Client, error) { return cliapp.NewClient(serverURL) }

// resolveConnection 按名称（不区分大小写）或数字 id 找连接。
func resolveConnection(ctx context.Context, client *cliapp.Client, ref string) (connection, error) {
	var data struct {
		Items []connection `json:"items"`
	}
	if _, err := client.Do(ctx, http.MethodGet, "/api/v1/ssh/connections", nil, &data); err != nil {
		return connection{}, err
	}
	id, _ := strconv.ParseInt(ref, 10, 64)
	names := make([]string, 0, len(data.Items))
	for _, item := range data.Items {
		names = append(names, item.Name)
		if strings.EqualFold(item.Name, ref) || (id > 0 && item.ID == id) {
			if !item.Enabled {
				return connection{}, cliapp.Exit(cliapp.ExitAPI, "连接 %s 已停用", item.Name)
			}
			return item, nil
		}
	}
	return connection{}, cliapp.Exit(cliapp.ExitAPI, "找不到连接 %q；可用连接：%s", ref, strings.Join(names, ", "))
}

// waitForCommand 轮询待批 / 执行中的命令直到终态或超时。wait<=0 表示不等待。
func waitForCommand(ctx context.Context, client *cliapp.Client, id int64, wait time.Duration, stderr io.Writer) (outcome, error) {
	deadline := time.Now().Add(wait)
	announced := false
	for {
		var result outcome
		if _, err := client.Do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/ssh/commands/%d", id), nil, &result); err != nil {
			return outcome{}, err
		}
		switch result.Status {
		case "pending", "approved", "running":
			if wait <= 0 || time.Now().After(deadline) {
				return result, nil
			}
			if !announced && result.Status == "pending" {
				fmt.Fprintf(stderr, "命令 #%d 等待批准（请在 opsbox 窗口「SSH → 待批准」中处理）…\n", id)
				announced = true
			}
			if err := cliapp.Sleep(ctx, 3*time.Second); err != nil {
				return outcome{}, cliapp.Exit(cliapp.ExitRejected, "已取消等待，命令 #%d 仍在队列中；稍后可用 sshctl status %d 查看", id, id)
			}
		default:
			return result, nil
		}
	}
}

// report 输出结果并返回对应退出码错误（nil 表示 0）。
func report(result outcome, asJSON bool, stdout, stderr io.Writer) error {
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(result)
	}
	switch result.Status {
	case "blocked":
		if !asJSON {
			fmt.Fprintf(stderr, "已被黑名单拦截 [%s]：%s\n", result.BlockedRule, result.BlockedReason)
		}
		return &cliapp.ExitError{Code: cliapp.ExitRejected}
	case "rejected":
		if !asJSON {
			fmt.Fprintf(stderr, "命令 #%d 已被拒绝：%s\n", result.ID, result.Error)
		}
		return &cliapp.ExitError{Code: cliapp.ExitRejected}
	case "pending", "approved", "running":
		if !asJSON {
			fmt.Fprintf(stderr, "命令 #%d 尚未完成（%s）。不要重复提交；稍后执行 sshctl status %d 查看结果。\n", result.ID, result.Status, result.ID)
		}
		return &cliapp.ExitError{Code: cliapp.ExitRejected}
	case "timeout":
		if !asJSON {
			writeOutput(result, stdout, stderr)
			fmt.Fprintf(stderr, "远端超时：%s\n", result.Error)
		}
		return &cliapp.ExitError{Code: cliapp.ExitTimeout}
	}
	if !asJSON {
		writeOutput(result, stdout, stderr)
		if result.Error != "" && result.ExitCode == nil {
			fmt.Fprintln(stderr, "执行失败："+result.Error)
		}
	}
	if result.ExitCode == nil {
		if result.Status == "success" {
			return nil
		}
		return &cliapp.ExitError{Code: cliapp.ExitGeneric}
	}
	if *result.ExitCode != 0 {
		return &cliapp.ExitError{Code: *result.ExitCode}
	}
	return nil
}

func writeOutput(result outcome, stdout, stderr io.Writer) {
	if result.OutputExpired {
		fmt.Fprintf(stderr, "（命令 #%d 已完成，但输出缓存已过期，只剩元数据：exit=%v stdout=%dB stderr=%dB）\n", result.ID, derefInt(result.ExitCode), result.StdoutBytes, result.StderrBytes)
		return
	}
	if result.Stdout != "" {
		io.WriteString(stdout, result.Stdout)
		if !strings.HasSuffix(result.Stdout, "\n") {
			io.WriteString(stdout, "\n")
		}
	}
	if result.Stderr != "" {
		io.WriteString(stderr, result.Stderr)
		if !strings.HasSuffix(result.Stderr, "\n") {
			io.WriteString(stderr, "\n")
		}
	}
	if result.Truncated {
		fmt.Fprintf(stderr, "[输出已截断：stdout %d 字节 / stderr %d 字节；需要完整输出请在服务器上重定向到文件再分段查看]\n", result.StdoutBytes, result.StderrBytes)
	}
}

func derefInt(v *int) any {
	if v == nil {
		return "-"
	}
	return *v
}

// ---- connections ----

func connectionsCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "connections", Aliases: []string{"conn"}, Short: "连接管理"}
	var asJSON bool
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "列出可用的 SSH 连接",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newClient()
			if err != nil {
				return err
			}
			var data struct {
				Items []connection `json:"items"`
			}
			if _, err := client.Do(cmd.Context(), http.MethodGet, "/api/v1/ssh/connections", nil, &data); err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(data.Items)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tHOST\tUSER\tPOLICY\tENABLED\tREMARK")
			for _, item := range data.Items {
				fmt.Fprintf(w, "%s\t%s:%d\t%s\t%s\t%v\t%s\n", item.Name, item.Host, item.Port, item.Username, item.ExecPolicy, item.Enabled, item.Remark)
			}
			return w.Flush()
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出")
	cmd.AddCommand(list)
	return cmd
}

// ---- exec / status ----

func execCommand() *cobra.Command {
	var timeout, wait time.Duration
	var pty, asJSON, checkOnly bool
	var sessionID, confirmToken string
	cmd := &cobra.Command{
		Use:   "exec <连接名> -- <命令...>",
		Short: "在已打开终端会话的连接上执行一条命令",
		Long: `命令在你于 opsbox 窗口打开的终端会话所属的 SSH 连接上另开一个通道执行——命令与输出会实时回显进那个终端，
你能看到 AI 做了什么；命令有独立的 shell，不受终端前台正在跑什么（vim / top）影响，也不共享它的 cwd / 环境变量。
因此该连接必须先有一个打开着的终端（sshctl sessions list 查看），否则直接拒绝。
audit 模式立即执行；approve 模式（或命中「动态命令强制审批」）需要先通过审批预检（见下），提交后进入待批队列并等待（--wait）。

审批协议（AI agent 必读）：需审批的命令直接执行会返回退出码 5 并给出预检令牌——
先向用户完整展示这条命令与影响，取得明确确认后，原样重跑并加 --confirm-token <令牌>；
命令随后进入待批队列，用户在 opsbox 窗口「SSH → 待批准」批准后才会执行。
--check 只预检不执行：不跑命令也能拿到判定结果与预检令牌（无副作用）。
长任务不要加大 --timeout，改为在服务器上 nohup 后台运行并轮询日志。`,
		Example: `  sshctl sessions list                  # 先看有哪些终端开着
  sshctl exec prod -- df -h
  sshctl exec prod --check -- "rm -rf /tmp/old"        # 预检：返回是否需要审批 + 预检令牌
  sshctl exec prod --confirm-token <令牌> -- "rm -rf /tmp/old"  # 用户确认后带令牌重新提交
  sshctl exec prod --timeout 120s -- "tail -n 200 /var/log/nginx/error.log | grep -i timeout"
  sshctl exec prod --session <会话ID> -- uptime
  sshctl exec prod --json -- cat /etc/os-release`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			command := strings.Join(args[1:], " ")
			if strings.TrimSpace(command) == "" {
				return cliapp.Exit(cliapp.ExitAPI, "命令不能为空")
			}
			client, err := newClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			conn, err := resolveConnection(ctx, client, args[0])
			if err != nil {
				return err
			}
			if checkOnly {
				return cliapp.DoCheck(ctx, client, fmt.Sprintf("/api/v1/ssh/connections/%d/exec/check", conn.ID),
					map[string]any{"command": command}, cmd.OutOrStdout(), cmd.ErrOrStderr(), asJSON)
			}
			body := map[string]any{"command": command, "pty": pty}
			if timeout > 0 {
				body["timeoutSeconds"] = int(timeout.Seconds())
			}
			if sessionID != "" {
				body["sessionId"] = sessionID
			}
			if confirmToken != "" {
				body["confirmToken"] = confirmToken
			}
			var result outcome
			if _, err := client.Do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/ssh/connections/%d/exec", conn.ID), body, &result); err != nil {
				if cliapp.IsCheckRequired(err) {
					return cliapp.ReportCheckRequired(err, "--confirm-token", cmd.ErrOrStderr())
				}
				return err
			}
			if result.Status == "pending" {
				if result.Dynamic && !asJSON {
					fmt.Fprintf(cmd.ErrOrStderr(), "命令被判定为动态命令（%s），需人工批准。\n", result.DynamicReason)
				}
				if result, err = waitForCommand(ctx, client, result.ID, wait, cmd.ErrOrStderr()); err != nil {
					return err
				}
			}
			return report(result, asJSON, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "远端命令超时（默认取平台设置，上限 600s）")
	cmd.Flags().DurationVar(&wait, "wait", 5*time.Minute, "approve 模式下等待批准的最长时间，0 表示不等待")
	cmd.Flags().BoolVar(&pty, "pty", false, "申请伪终端（sudo / 需要 TTY 的脚本；stderr 会并入 stdout）")
	cmd.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出完整结果（便于解析）")
	cmd.Flags().StringVar(&sessionID, "session", "", "指定在哪个终端会话的连接上执行（默认选最近活跃的）")
	cmd.Flags().BoolVar(&checkOnly, "check", false, "只预检不执行：返回是否需要平台审批、预检令牌与指引")
	cmd.Flags().StringVar(&confirmToken, "confirm-token", "", "审批预检令牌（需审批命令在用户确认后携带它重新提交）")
	return cmd
}

func statusCommand() *cobra.Command {
	var wait time.Duration
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status <命令ID>",
		Short: "查看（或继续等待）某条命令的执行结果",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || id < 1 {
				return cliapp.Exit(cliapp.ExitAPI, "命令 ID 无效")
			}
			client, err := newClient()
			if err != nil {
				return err
			}
			result, err := waitForCommand(cmd.Context(), client, id, wait, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			return report(result, asJSON, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().DurationVar(&wait, "wait", 0, "继续等待的最长时间，0 表示只查一次")
	cmd.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出")
	return cmd
}

// ---- upload / download ----

// defaultChunkSize 与服务端单片上限（32MB）保持一致的安全值。
const defaultChunkSize = 16 << 20

type transferResult struct {
	LogID    int64 `json:"logId"`
	Written  int64 `json:"written"`
	Total    int64 `json:"total"`
	Complete bool  `json:"complete"`
}

type remoteStat struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Size   int64  `json:"size"`
}

// registerTransfer 登记传输意图并在 approve 模式下等待批准；返回 transferId。
func registerTransfer(ctx context.Context, client *cliapp.Client, conn connection, kind, remotePath string, size int64, wait time.Duration, stderr io.Writer) (int64, error) {
	var entry execLog
	status, err := client.Do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/ssh/connections/%d/transfers", conn.ID),
		map[string]any{"kind": kind, "path": remotePath, "size": size}, &entry)
	if err != nil {
		return 0, err
	}
	if status == http.StatusAccepted || entry.Status == "pending" {
		result, err := waitForCommand(ctx, client, entry.ID, wait, stderr)
		if err != nil {
			return 0, err
		}
		if result.Status != "approved" {
			return 0, report(result, false, io.Discard, stderr)
		}
	}
	return entry.ID, nil
}

func statRemote(ctx context.Context, client *cliapp.Client, conn connection, remotePath string) (remoteStat, error) {
	var stat remoteStat
	query := url.Values{"path": {remotePath}}
	_, err := client.Do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/ssh/connections/%d/files/stat?%s", conn.ID, query.Encode()), nil, &stat)
	return stat, err
}

func uploadCommand() *cobra.Command {
	var wait time.Duration
	var chunkSize int64
	var retries int
	cmd := &cobra.Command{
		Use:   "upload <连接名> <本地文件> <远端路径>",
		Short: "通过 SFTP 上传文件（远端为绝对路径，父目录自动创建）",
		Long: `文件大小不限：本地按 --chunk-size 切片顺序上传，单片失败自动重试。
中断后重新执行同一条命令会先查远端已有大小，从断点继续，不会重传已完成的部分。`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			localPath, remotePath := args[1], args[2]
			file, err := os.Open(localPath)
			if err != nil {
				return cliapp.Exit(cliapp.ExitAPI, "打开本地文件失败: %v", err)
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil || info.IsDir() {
				return cliapp.Exit(cliapp.ExitAPI, "本地路径不是文件")
			}
			if chunkSize < 1 {
				chunkSize = defaultChunkSize
			}
			client, err := newClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			conn, err := resolveConnection(ctx, client, args[0])
			if err != nil {
				return err
			}
			stderr := cmd.ErrOrStderr()
			// 断点续传：远端已有的字节数就是起点（大小超过本地则视为异常文件，重头覆盖）
			offset := int64(0)
			if stat, err := statRemote(ctx, client, conn, remotePath); err == nil && stat.Exists && stat.Size < info.Size() {
				offset = stat.Size
				fmt.Fprintf(stderr, "远端已有 %d 字节，从断点续传\n", offset)
			}
			transferID, err := registerTransfer(ctx, client, conn, "upload", remotePath, info.Size(), wait, stderr)
			if err != nil {
				return err
			}
			if _, err := file.Seek(offset, io.SeekStart); err != nil {
				return cliapp.Exit(cliapp.ExitAPI, "定位本地文件失败: %v", err)
			}
			started := time.Now()
			for offset < info.Size() {
				size := min(chunkSize, info.Size()-offset)
				final := offset+size >= info.Size()
				query := url.Values{
					"path":       {remotePath},
					"transferId": {strconv.FormatInt(transferID, 10)},
					"offset":     {strconv.FormatInt(offset, 10)},
				}
				if final {
					query.Set("final", "true")
				}
				path := fmt.Sprintf("/api/v1/ssh/connections/%d/files?%s", conn.ID, query.Encode())
				var result transferResult
				var lastErr error
				for attempt := 0; attempt <= retries; attempt++ {
					if attempt > 0 {
						fmt.Fprintf(stderr, "分片 @%d 上传失败（%v），第 %d 次重试…\n", offset, lastErr, attempt)
						if err := cliapp.Sleep(ctx, time.Duration(attempt)*2*time.Second); err != nil {
							return cliapp.Exit(cliapp.ExitAPI, "已取消上传")
						}
						if _, err := file.Seek(offset, io.SeekStart); err != nil {
							return cliapp.Exit(cliapp.ExitAPI, "定位本地文件失败: %v", err)
						}
					}
					_, lastErr = client.DoRaw(ctx, http.MethodPut, path, io.LimitReader(file, size), "application/octet-stream", &result)
					if lastErr == nil {
						break
					}
				}
				if lastErr != nil {
					return cliapp.Exit(cliapp.ExitAPI, "上传中断于偏移 %d：%v；重新执行同一条命令可从断点续传", offset, lastErr)
				}
				offset += size
				if info.Size() > chunkSize {
					fmt.Fprintf(stderr, "\r已上传 %d / %d 字节 (%.1f%%)", offset, info.Size(), float64(offset)*100/float64(info.Size()))
				}
			}
			if info.Size() > chunkSize {
				fmt.Fprintln(stderr)
			}
			fmt.Fprintf(stderr, "✓ 已上传 %s → %s:%s (%d 字节, %.1fs)\n", localPath, conn.Name, remotePath, info.Size(), time.Since(started).Seconds())
			return nil
		},
	}
	cmd.Flags().DurationVar(&wait, "wait", 5*time.Minute, "approve 模式下等待批准的最长时间")
	cmd.Flags().Int64Var(&chunkSize, "chunk-size", defaultChunkSize, "分片字节数（上限 32MB）")
	cmd.Flags().IntVar(&retries, "retries", 3, "单个分片的重试次数")
	return cmd
}

func downloadCommand() *cobra.Command {
	var wait time.Duration
	var resume bool
	cmd := &cobra.Command{
		Use:   "download <连接名> <远端路径> <本地文件>",
		Short: "通过 SFTP 下载文件（大文件支持断点续传）",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			remotePath, localPath := args[1], args[2]
			client, err := newClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			conn, err := resolveConnection(ctx, client, args[0])
			if err != nil {
				return err
			}
			offset := int64(0)
			flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
			if resume {
				if info, statErr := os.Stat(localPath); statErr == nil && info.Size() > 0 {
					offset = info.Size()
					flags = os.O_WRONLY | os.O_APPEND
					fmt.Fprintf(cmd.ErrOrStderr(), "本地已有 %d 字节，从断点续传\n", offset)
				}
			}
			transferID, err := registerTransfer(ctx, client, conn, "download", remotePath, 0, wait, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			query := url.Values{
				"path":       {remotePath},
				"transferId": {strconv.FormatInt(transferID, 10)},
				"offset":     {strconv.FormatInt(offset, 10)},
			}
			response, err := client.Download(ctx, fmt.Sprintf("/api/v1/ssh/connections/%d/files?%s", conn.ID, query.Encode()))
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if response.StatusCode == http.StatusAccepted {
				return cliapp.Exit(cliapp.ExitRejected, "传输仍待批准")
			}
			out, err := os.OpenFile(localPath, flags, 0o600)
			if err != nil {
				return cliapp.Exit(cliapp.ExitAPI, "打开本地文件失败: %v", err)
			}
			defer out.Close()
			written, err := io.Copy(out, response.Body)
			if err != nil {
				return cliapp.Exit(cliapp.ExitAPI, "下载中断于 %d 字节：%v；加 --resume 重新执行可续传", offset+written, err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "✓ 已下载 %s:%s → %s (%d 字节)\n", conn.Name, remotePath, localPath, offset+written)
			return nil
		},
	}
	cmd.Flags().DurationVar(&wait, "wait", 5*time.Minute, "approve 模式下等待批准的最长时间")
	cmd.Flags().BoolVar(&resume, "resume", false, "本地文件已存在时从其大小处续传，而不是覆盖重下")
	return cmd
}

// ---- shell ----

func shellCommand() *cobra.Command {
	var sessionID string
	cmd := &cobra.Command{
		Use:   "shell <连接名>",
		Short: "接入一条已打开的交互式终端（给人用；AI 请用 exec）",
		Long: `接入 opsbox 窗口已经打开的终端会话，与窗口共享同一个 shell（双方都能看到对方的输入输出）。
CLI 不能自己新建会话：开终端的权力留在 opsbox 窗口，这样「AI 只能操作我正开着的终端」这条约束才成立。
没有可接入的会话时，先在 opsbox 窗口「SSH → 终端」打开对应连接。`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return cliapp.Exit(cliapp.ExitAPI, "shell 需要在真实终端里运行；非交互场景请用 exec")
			}
			client, err := newClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			conn, err := resolveConnection(ctx, client, args[0])
			if err != nil {
				return err
			}
			cols, rows, _ := term.GetSize(int(os.Stdout.Fd()))
			body := map[string]any{"rows": rows, "cols": cols}
			if sessionID != "" {
				body["sessionId"] = sessionID
			}
			var info sessionInfo
			if _, err := client.Do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/ssh/connections/%d/sessions", conn.ID), body, &info); err != nil {
				return err
			}
			if info.SessionID == "" {
				return cliapp.Exit(cliapp.ExitRejected, "没有可接入的终端会话；请先在 opsbox 窗口打开 %s 的终端", conn.Name)
			}
			return runShell(ctx, client, conn, info)
		},
	}
	cmd.Flags().StringVar(&sessionID, "session", "", "指定要接入的会话 ID（默认接入最近活跃的那个）")
	return cmd
}

// sessionsCommand 列出当前已打开的终端会话——CLI 能操作哪些连接，看这里。
func sessionsCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "sessions", Short: "终端会话"}
	var asJSON bool
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "列出已打开的终端会话（只有这些连接能被 exec / 传输操作）",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newClient()
			if err != nil {
				return err
			}
			var data struct {
				Items []sessionSummary `json:"items"`
			}
			if _, err := client.Do(cmd.Context(), http.MethodGet, "/api/v1/ssh/sessions", nil, &data); err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(data.Items)
			}
			if len(data.Items) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "没有已打开的终端会话。请先在 opsbox 窗口「SSH → 终端」打开一个连接的终端。")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSESSION\tCONNECTION\tATTACHED\tLAST ACTIVE")
			for _, item := range data.Items {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", sessionName(item), item.SessionID, item.ConnectionName, item.Attached, item.LastSeenAt.Format(time.RFC3339))
			}
			return w.Flush()
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出")
	cmd.AddCommand(list)
	return cmd
}

func runShell(ctx context.Context, client *cliapp.Client, conn connection, info sessionInfo) error {
	wsURL, err := url.Parse(client.BaseURL)
	if err != nil {
		return cliapp.Exit(cliapp.ExitAPI, "本地服务地址无效")
	}
	switch wsURL.Scheme {
	case "https":
		wsURL.Scheme = "wss"
	default:
		wsURL.Scheme = "ws"
	}
	wsURL.Path = fmt.Sprintf("/api/v1/ssh/connections/%d/sessions/%s/ws", conn.ID, info.SessionID)
	wsURL.RawQuery = url.Values{"ticket": {info.Ticket}}.Encode()
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	ws, _, err := dialer.DialContext(ctx, wsURL.String(), http.Header{"User-Agent": {"opsbox-cli"}})
	if err != nil {
		return cliapp.Exit(cliapp.ExitAPI, "连接终端会话失败: %v", err)
	}
	defer ws.Close()
	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return cliapp.Exit(cliapp.ExitAPI, "设置终端 raw 模式失败: %v", err)
	}
	defer term.Restore(fd, state)
	fmt.Fprintf(os.Stderr, "已连接 %s（%s@%s）。退出远端 shell（exit / Ctrl+D）即断开。\r\n", conn.Name, conn.Username, conn.Host)
	done := make(chan struct{})
	// 远端 → 本地
	go func() {
		defer close(done)
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			_, _ = os.Stdout.Write(data)
		}
	}()
	// 本地 → 远端
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buffer)
			if n > 0 {
				if err := ws.WriteMessage(websocket.BinaryMessage, buffer[:n]); err != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	// 尺寸变化：轮询（跨平台，避免 SIGWINCH 的平台差异）
	lastCols, lastRows, _ := term.GetSize(int(os.Stdout.Fd()))
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			fmt.Fprint(os.Stderr, "\r\n会话已结束。\r\n")
			return nil
		case <-ctx.Done():
			_ = ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return nil
		case <-ticker.C:
			cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
			if err != nil || (cols == lastCols && rows == lastRows) {
				continue
			}
			lastCols, lastRows = cols, rows
			resizeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, _ = client.Do(resizeCtx, http.MethodPost, fmt.Sprintf("/api/v1/ssh/connections/%d/sessions/%s/resize", conn.ID, info.SessionID), map[string]int{"rows": rows, "cols": cols}, nil)
			cancel()
		}
	}
}
