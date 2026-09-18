// redisctl 是给 AI（及人）用的 Redis / Valkey 运维 CLI：执行与审计都在本地 opsbox 服务里。
//
//	redisctl connections list               # 列出可用连接
//	redisctl sessions list                  # 列出已打开的控制台（只有这些连接能操作）
//	redisctl exec prod -- HGETALL user:1    # 执行一条命令（-- 之后按空格分词）
//	redisctl exec prod --arg "HSET" --arg "user:1" --arg "有 空格 的值"   # 含空格的参数用 --arg
//	redisctl scan prod "user:*"             # 安全 SCAN 摸 key 分布（不要用 KEYS）
//
// 会话即授权：命令只能发往你在 opsbox 窗口打开了控制台的连接，执行结果实时显示在那个面板里；
// 连接没有打开的控制台时一律拒绝，关掉面板即刻失权。
//
// 退出码：0 成功；Redis 错误 = 1（透传，如 WRONGTYPE、MOVED——后者提示 v1 不支持集群）；
// 2 被黑名单拦截 / 被拒绝 / 等待批准超时 / 无可用会话；3 命令超时；4 本地服务 API 错误；
// 5 该写命令需要平台审批且尚未取得用户确认（先向用户展示命令并获得确认，再带 --confirm-token 重跑，见错误输出指引）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"opsbox/internal/cliapp"
)

var serverURL string

// version 由构建注入（-ldflags "-X main.version=…"）。
var version = "dev"

func main() {
	root := &cobra.Command{
		Use:     "redisctl",
		Short:   "opsbox Redis 运维 CLI（命令执行 / 安全 SCAN，全程审计）",
		Version: version,
	}
	root.PersistentFlags().StringVar(&serverURL, "url", "", "覆盖本地服务地址（默认自动探测 127.0.0.1:37421 起；也可用环境变量 OPSBOX_URL）")
	root.AddCommand(connectionsCommand(), execCommand(), scanCommand(), statusCommand(), sessionsCommand())
	cliapp.Run(root)
}

// ---- 数据结构（与服务端 JSON 对齐） ----

type connection struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	HasPassword bool   `json:"hasPassword"`
	DB          int    `json:"db"`
	TLS         bool   `json:"tls"`
	WritePolicy string `json:"writePolicy"`
	Enabled     bool   `json:"enabled"`
	Remark      string `json:"remark"`
}

type outcome struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Reply      any    `json:"reply,omitempty"`
	Text       string `json:"text,omitempty"`
	ReplyBytes int    `json:"replyBytes"`
	Truncated  bool   `json:"truncated"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
	Reason     string `json:"reason,omitempty"`
	ReplyNote  string `json:"replyNote,omitempty"`
}

type scanResult struct {
	Keys       []string `json:"keys"`
	Truncated  bool     `json:"truncated"`
	Total      int      `json:"total"`
	DurationMs int64    `json:"durationMs"`
}

// ---- helpers ----

func newClient() (*cliapp.Client, error) { return cliapp.NewClient(serverURL) }

// resolveConnection 按名称（不区分大小写）或数字 id 找连接。
func resolveConnection(ctx context.Context, client *cliapp.Client, ref string) (connection, error) {
	var data struct {
		Items []connection `json:"items"`
	}
	if _, err := client.Do(ctx, http.MethodGet, "/api/v1/redis/connections", nil, &data); err != nil {
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

// waitForCommand 轮询命令状态直到终态或超时。wait<=0 表示不等待。
func waitForCommand(ctx context.Context, client *cliapp.Client, id int64, wait time.Duration, stderr io.Writer) (outcome, error) {
	deadline := time.Now().Add(wait)
	announced := false
	for {
		var result outcome
		if _, err := client.Do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/redis/commands/%d", id), nil, &result); err != nil {
			return outcome{}, err
		}
		if result.Status == "pending" || result.Status == "running" {
			if wait <= 0 || time.Now().After(deadline) {
				return result, nil
			}
			if !announced && result.Status == "pending" {
				fmt.Fprintf(stderr, "命令 #%d 等待批准（请在 opsbox 窗口「Redis → 待批准操作」中处理）…\n", id)
				announced = true
			}
			if err := cliapp.Sleep(ctx, 3*time.Second); err != nil {
				return outcome{}, cliapp.Exit(cliapp.ExitRejected, "已取消等待，命令 #%d 仍在队列中；稍后可用 redisctl status %d 查看", id, id)
			}
			continue
		}
		return result, nil
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
			fmt.Fprintf(stderr, "已被黑名单拦截：%s\n", result.Error)
		}
		return &cliapp.ExitError{Code: cliapp.ExitRejected}
	case "rejected":
		if !asJSON {
			fmt.Fprintf(stderr, "命令 #%d 已被拒绝：%s\n", result.ID, result.Error)
		}
		return &cliapp.ExitError{Code: cliapp.ExitRejected}
	case "pending", "running":
		if !asJSON {
			fmt.Fprintf(stderr, "命令 #%d 尚未完成（%s）。不要重复提交；稍后执行 redisctl status %d 查看结果。\n", result.ID, result.Status, result.ID)
		}
		return &cliapp.ExitError{Code: cliapp.ExitRejected}
	case "timeout":
		if !asJSON {
			fmt.Fprintf(stderr, "命令超时：%s\n", result.Error)
		}
		return &cliapp.ExitError{Code: cliapp.ExitTimeout}
	case "failed":
		if !asJSON {
			fmt.Fprintf(stderr, "Redis 错误：%s\n", redisErrorHint(result.Error))
		}
		return &cliapp.ExitError{Code: cliapp.ExitGeneric}
	}
	if !asJSON {
		writeReply(result, stdout, stderr)
		if result.ReplyNote != "" {
			fmt.Fprintln(stderr, result.ReplyNote)
		}
	}
	if result.Status == "success" {
		return nil
	}
	return &cliapp.ExitError{Code: cliapp.ExitGeneric}
}

// redisErrorHint 给常见 Redis 错误追加可行动的提示（错误文本原样透传）。
func redisErrorHint(errText string) string {
	switch {
	case strings.Contains(errText, "MOVED"):
		return errText + "（v1 暂不支持集群模式：key 落在其他节点，请直连该分片）"
	case strings.Contains(errText, "WRONGTYPE"):
		return errText + "（value 类型与命令不符：先用 TYPE <key> 确认类型）"
	}
	return errText
}

func writeReply(result outcome, stdout, stderr io.Writer) {
	if result.Text != "" {
		io.WriteString(stdout, result.Text)
		if !strings.HasSuffix(result.Text, "\n") {
			io.WriteString(stdout, "\n")
		}
	}
	if result.Truncated {
		fmt.Fprintf(stderr, "[回复已截断：原始 %d 字节；大 value 请先 STRLEN / MEMORY USAGE 探查，再分段取]\n", result.ReplyBytes)
	}
}

// ---- connections ----

func connectionsCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "connections", Aliases: []string{"conn"}, Short: "连接管理"}
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "列出可用的 Redis 连接",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newClient()
			if err != nil {
				return err
			}
			var data struct {
				Items []connection `json:"items"`
			}
			if _, err := client.Do(cmd.Context(), http.MethodGet, "/api/v1/redis/connections", nil, &data); err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(data.Items)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tADDRESS\tDB\tTLS\tPOLICY\tPASSWORD\tENABLED\tREMARK")
			for _, item := range data.Items {
				password := "-"
				if item.HasPassword {
					password = "已配置"
				}
				fmt.Fprintf(w, "%s\t%s:%d\t%d\t%v\t%s\t%s\t%v\t%s\n", item.Name, item.Host, item.Port, item.DB, item.TLS, item.WritePolicy, password, item.Enabled, item.Remark)
			}
			return w.Flush()
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出")
	cmd.AddCommand(list)
	return cmd
}

// ---- exec ----

func execCommand() *cobra.Command {
	var commandArgs []string
	var wait time.Duration
	var asJSON, checkOnly bool
	var confirmToken string
	cmd := &cobra.Command{
		Use:   "exec <连接名> -- <命令...>",
		Short: "在目标 Redis 实例执行一条命令",
		Long: `"--" 之后的参数按空格分词作为命令参数；含空格 / 特殊字符的参数改用重复的 --arg 传入。
读命令直接执行；confirm 策略（默认）下写命令需要先通过审批预检（见下），提交后进入待批队列并等待（--wait）；
readonly 策略拒绝写命令，allow 策略全放行（仍审计）。

审批协议（AI agent 必读）：需审批的写命令直接执行会返回退出码 5 并给出预检令牌——
先向用户完整展示这条命令与影响，取得明确确认后，原样重跑并加 --confirm-token <令牌>；
命令随后进入待批队列，用户在 opsbox 窗口「Redis → 待批准操作」批准后才会执行。
--check 只预检不执行：不跑命令也能拿到判定结果与预检令牌（无副作用）。`,
		Example: `  redisctl exec prod -- HGETALL user:1
  redisctl exec prod -- SET counter 100
  redisctl exec prod --check -- FLUSHDB                            # 预检：返回是否需要审批 + 预检令牌
  redisctl exec prod --confirm-token <令牌> -- FLUSHDB             # 用户确认后带令牌重新提交
  redisctl exec prod --arg "HSET" --arg "user:1" --arg "bio" --arg "hello world"
  redisctl exec prod --json -- LRANGE queue 0 -1`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			command := commandArgs
			if len(command) == 0 {
				if len(args) < 2 {
					return cliapp.Exit(cliapp.ExitAPI, "缺少命令参数：exec <连接名> -- <命令...>，或用 --arg 逐个传入")
				}
				command = args[1:]
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
				return cliapp.DoCheck(ctx, client, fmt.Sprintf("/api/v1/redis/connections/%d/exec/check", conn.ID),
					map[string]any{"args": command}, cmd.OutOrStdout(), cmd.ErrOrStderr(), asJSON)
			}
			body := map[string]any{"args": command}
			if confirmToken != "" {
				body["confirmToken"] = confirmToken
			}
			var result outcome
			if _, err := client.Do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/redis/connections/%d/exec", conn.ID), body, &result); err != nil {
				if cliapp.IsCheckRequired(err) {
					return cliapp.ReportCheckRequired(err, "--confirm-token", cmd.ErrOrStderr())
				}
				return err
			}
			if result.Status == "pending" {
				if result, err = waitForCommand(ctx, client, result.ID, wait, cmd.ErrOrStderr()); err != nil {
					return err
				}
			}
			return report(result, asJSON, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringArrayVar(&commandArgs, "arg", nil, "命令参数（可重复；提供时忽略 \"--\" 之后的位置参数）")
	cmd.Flags().DurationVar(&wait, "wait", 5*time.Minute, "confirm 策略下等待批准的最长时间，0 表示不等待")
	cmd.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出完整结果（便于解析，AI 优先使用）")
	cmd.Flags().BoolVar(&checkOnly, "check", false, "只预检不执行：返回是否需要平台审批、预检令牌与指引")
	cmd.Flags().StringVar(&confirmToken, "confirm-token", "", "审批预检令牌（需审批写命令在用户确认后携带它重新提交）")
	return cmd
}

// ---- scan ----

func scanCommand() *cobra.Command {
	var count, limit int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "scan <连接名> [pattern]",
		Short: "安全 SCAN 摸 key 分布（替代 KEYS，避免大库阻塞）",
		Example: `  redisctl scan prod
  redisctl scan prod "user:*"
  redisctl scan prod "session:*" --limit 200 --json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			conn, err := resolveConnection(ctx, client, args[0])
			if err != nil {
				return err
			}
			body := map[string]any{"pattern": args[1]}
			if count > 0 {
				body["count"] = count
			}
			if limit > 0 {
				body["limit"] = limit
			}
			var result scanResult
			if _, err := client.Do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/redis/connections/%d/scan", conn.ID), body, &result); err != nil {
				// SCAN 失败多为 Redis 侧错误（拨号 / 超时），按退出码 1 报告而不是 API 错误
				if cliapp.IsAPIStatus(err, http.StatusBadGateway) {
					return cliapp.Exit(cliapp.ExitGeneric, "%v", err)
				}
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			for _, key := range result.Keys {
				fmt.Fprintln(cmd.OutOrStdout(), key)
			}
			if result.Truncated {
				fmt.Fprintf(cmd.ErrOrStderr(), "（达到 %d 个上限，未遍历完）\n", result.Total)
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "✓ 共 %d 个 key（%.1fs）\n", result.Total, time.Duration(result.DurationMs).Seconds())
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&count, "count", 0, "每批 SCAN 的 COUNT 提示值（默认 100）")
	cmd.Flags().IntVar(&limit, "limit", 0, "key 数量上限（默认/上限取平台设置，默认 1000）")
	cmd.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出")
	return cmd
}

// ---- status ----

func statusCommand() *cobra.Command {
	var wait time.Duration
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status <命令ID>",
		Short: "查看（或继续等待）某条命令的执行状态",
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

// sessionSummary 是「已打开的控制台会话」列表项。会话即授权：列表里没有的连接，exec / scan 都会被拒。
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

// sessionsCommand 列出当前已打开的控制台会话——CLI 能操作哪些连接，看这里。
func sessionsCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "sessions", Short: "控制台会话"}
	var asJSON bool
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "列出已打开的控制台会话（只有这些连接能被 exec / scan 操作）",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newClient()
			if err != nil {
				return err
			}
			var data struct {
				Items []sessionSummary `json:"items"`
			}
			if _, err := client.Do(cmd.Context(), http.MethodGet, "/api/v1/redis/sessions", nil, &data); err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(data.Items)
			}
			if len(data.Items) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "没有已打开的控制台会话。请先在 opsbox 窗口「Redis → 控制台」打开一个连接的控制台。")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSESSION\tCONNECTION\tVIEWERS\tLAST ACTIVE")
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
