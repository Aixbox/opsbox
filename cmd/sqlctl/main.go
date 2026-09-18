// sqlctl 是给 AI（及人）用的数据库运维 CLI：执行与审计都在本地 opsbox 服务里。
//
//	sqlctl connections list             # 列出可用连接
//	sqlctl sessions list                # 列出已打开的控制台（只有这些连接能操作）
//	sqlctl tables prod                  # 自省：列出表
//	sqlctl schema prod users            # 自省：表结构（列 / 索引 / DDL）
//	sqlctl query prod -- SELECT ...     # 执行 SQL（confirm 策略的写操作会进待批队列）
//
// 会话即授权：查询只能发往你在 opsbox 窗口打开了控制台的连接，语句与结果实时显示在那个面板里；
// 连接没有打开的控制台时一律拒绝，关掉面板即刻失权。
//
// 退出码：0 成功；1 SQL 报错（透传驱动错误文本）；2 被黑名单拦截 / 被拒绝 / 只读拒绝 / 等待批准超时 / 无可用会话；3 查询超时；4 本地服务 API 错误；
// 5 该写操作需要平台审批且尚未取得用户确认（先向用户展示 SQL 并获得确认，再带 --confirm-token 重跑，见错误输出指引）。
package main

import (
	"context"
	"encoding/csv"
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
		Use:     "sqlctl",
		Short:   "opsbox 数据库运维 CLI（MySQL / PostgreSQL，全程审计）",
		Version: version,
	}
	root.PersistentFlags().StringVar(&serverURL, "url", "", "覆盖本地服务地址（默认自动探测 127.0.0.1:37421 起；也可用环境变量 OPSBOX_URL）")
	root.AddCommand(connectionsCommand(), tablesCommand(), schemaCommand(), queryCommand(), statusCommand(), sessionsCommand())
	cliapp.Run(root)
}

// ---- 数据结构（与服务端 JSON 对齐） ----

type connection struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Engine      string `json:"engine"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Database    string `json:"database"`
	WritePolicy string `json:"writePolicy"`
	Enabled     bool   `json:"enabled"`
	Remark      string `json:"remark"`
}

type tableSchema struct {
	Name    string   `json:"name"`
	Comment string   `json:"comment,omitempty"`
	Columns []column `json:"columns"`
	Indexes []index  `json:"indexes"`
	DDL     string   `json:"ddl,omitempty"`
}

type column struct {
	Name     string `json:"name"`
	DataType string `json:"dataType"`
	Nullable bool   `json:"nullable"`
	Default  any    `json:"default"`
	Key      string `json:"key,omitempty"`
	Extra    string `json:"extra,omitempty"`
}

type index struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns,omitempty"`
	Unique     bool     `json:"unique"`
	Definition string   `json:"definition,omitempty"`
}

type outcome struct {
	ID            int64    `json:"id"`
	Status        string   `json:"status"`
	Kind          string   `json:"kind"`
	Columns       []string `json:"columns,omitempty"`
	Rows          [][]any  `json:"rows,omitempty"`
	RowsReturned  int      `json:"rowsReturned"`
	RowsAffected  int64    `json:"rowsAffected"`
	Truncated     bool     `json:"truncated"`
	DurationMs    int64    `json:"durationMs"`
	Error         string   `json:"error,omitempty"`
	BlockedRule   string   `json:"blockedRule,omitempty"`
	BlockedReason string   `json:"blockedReason,omitempty"`
	OutputExpired bool     `json:"outputExpired,omitempty"`
}

// ---- helpers ----

func newClient() (*cliapp.Client, error) { return cliapp.NewClient(serverURL) }

// resolveConnection 按名称（不区分大小写）或数字 id 找连接。
func resolveConnection(ctx context.Context, client *cliapp.Client, ref string) (connection, error) {
	var data struct {
		Items []connection `json:"items"`
	}
	if _, err := client.Do(ctx, http.MethodGet, "/api/v1/sql/connections", nil, &data); err != nil {
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

// waitForQuery 轮询待批 / 执行中的查询直到终态或超时。wait<=0 表示不等待。
func waitForQuery(ctx context.Context, client *cliapp.Client, id int64, wait time.Duration, stderr io.Writer) (outcome, error) {
	deadline := time.Now().Add(wait)
	announced := false
	for {
		var result outcome
		if _, err := client.Do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/sql/queries/%d", id), nil, &result); err != nil {
			return outcome{}, err
		}
		switch result.Status {
		case "pending", "running":
			if wait <= 0 || time.Now().After(deadline) {
				return result, nil
			}
			if !announced && result.Status == "pending" {
				fmt.Fprintf(stderr, "写操作 #%d 等待批准（请在 opsbox 窗口「数据库 → 待批准写操作」中处理）…\n", id)
				announced = true
			}
			if err := cliapp.Sleep(ctx, 3*time.Second); err != nil {
				return outcome{}, cliapp.Exit(cliapp.ExitRejected, "已取消等待，写操作 #%d 仍在队列中；稍后可用 sqlctl status %d 查看", id, id)
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
			fmt.Fprintf(stderr, "SQL #%d 被拒绝：%s\n", result.ID, result.Error)
		}
		return &cliapp.ExitError{Code: cliapp.ExitRejected}
	case "pending", "running":
		if !asJSON {
			fmt.Fprintf(stderr, "SQL #%d 尚未完成（%s）。不要重复提交；稍后执行 sqlctl status %d 查看结果。\n", result.ID, result.Status, result.ID)
		}
		return &cliapp.ExitError{Code: cliapp.ExitRejected}
	case "timeout":
		if !asJSON {
			fmt.Fprintf(stderr, "查询超时：%s\n", result.Error)
		}
		return &cliapp.ExitError{Code: cliapp.ExitTimeout}
	}
	if !asJSON {
		switch {
		case result.OutputExpired:
			// 结果已过保存期（或设置为不保存）：只剩元数据，需要数据请重新执行查询
			fmt.Fprintf(stderr, "（SQL #%d 已完成，但结果已过保存期，只剩元数据：%d 行 / 影响 %d 行）\n", result.ID, result.RowsReturned, result.RowsAffected)
		case result.Kind == "read":
			writeTable(result, stdout)
			fmt.Fprintf(stderr, "%d 行", result.RowsReturned)
			if result.Truncated {
				fmt.Fprintf(stderr, "（达到行数上限已截断，结果不完整）")
			}
			fmt.Fprintln(stderr)
		default:
			fmt.Fprintf(stderr, "影响 %d 行\n", result.RowsAffected)
		}
		if result.Error != "" {
			fmt.Fprintln(stderr, "执行失败："+result.Error)
		}
	}
	if result.Status != "success" {
		return &cliapp.ExitError{Code: cliapp.ExitGeneric}
	}
	return nil
}

// writeTable 以人读表格输出查询结果；NULL 显示为 NULL。
func writeTable(result outcome, stdout io.Writer) {
	if len(result.Columns) == 0 {
		return
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(result.Columns, "\t"))
	for _, row := range result.Rows {
		cells := make([]string, len(row))
		for i, value := range row {
			cells[i] = displayValue(value)
		}
		fmt.Fprintln(w, strings.Join(cells, "\t"))
	}
	_ = w.Flush()
}

func displayValue(value any) string {
	if value == nil {
		return "NULL"
	}
	return fmt.Sprintf("%v", value)
}

// writeCSV 输出 RFC 4180 CSV（首行列名）。
func writeCSV(result outcome, stdout io.Writer) error {
	w := csv.NewWriter(stdout)
	if err := w.Write(result.Columns); err != nil {
		return err
	}
	for _, row := range result.Rows {
		cells := make([]string, len(row))
		for i, value := range row {
			if value == nil {
				continue
			}
			if text, ok := value.(string); ok {
				cells[i] = text
			} else {
				cells[i] = fmt.Sprintf("%v", value)
			}
		}
		if err := w.Write(cells); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// ---- connections ----

func connectionsCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "connections", Aliases: []string{"conn"}, Short: "连接管理"}
	var asJSON bool
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "列出可用的数据库连接",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newClient()
			if err != nil {
				return err
			}
			var data struct {
				Items []connection `json:"items"`
			}
			if _, err := client.Do(cmd.Context(), http.MethodGet, "/api/v1/sql/connections", nil, &data); err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(data.Items)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tENGINE\tHOST\tUSER\tDATABASE\tPOLICY\tENABLED\tREMARK")
			for _, item := range data.Items {
				fmt.Fprintf(w, "%s\t%s\t%s:%d\t%s\t%s\t%s\t%v\t%s\n", item.Name, item.Engine, item.Host, item.Port, item.Username, item.Database, item.WritePolicy, item.Enabled, item.Remark)
			}
			return w.Flush()
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出")
	cmd.AddCommand(list)
	return cmd
}

// ---- 自省 ----

func tablesCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "tables <连接名>",
		Short: "自省：列出当前库（MySQL）/ 当前模式（PG）下的表与视图",
		Args:  cobra.ExactArgs(1),
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
			var data struct {
				Items []string `json:"items"`
			}
			if _, err := client.Do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/sql/connections/%d/tables", conn.ID), nil, &data); err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(data.Items)
			}
			for _, name := range data.Items {
				fmt.Fprintln(cmd.OutOrStdout(), name)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出")
	return cmd
}

func schemaCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "schema <连接名> [表名]",
		Short: "自省：不指定表名时输出各表的列摘要；指定表名时输出完整结构（列 / 索引 / DDL）",
		Args:  cobra.RangeArgs(1, 2),
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
			// 未指定表名：列出全部表再逐个拉列摘要，方便先摸清结构
			if len(args) == 1 {
				var data struct {
					Items []string `json:"items"`
				}
				if _, err := client.Do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/sql/connections/%d/tables", conn.ID), nil, &data); err != nil {
					return err
				}
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "TABLE\tCOLUMN\tTYPE\tNULL\tKEY")
				for _, table := range data.Items {
					var schema tableSchema
					if _, err := client.Do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/sql/connections/%d/tables/%s/schema", conn.ID, table), nil, &schema); err != nil {
						return err
					}
					for i, column := range schema.Columns {
						if i >= 8 {
							fmt.Fprintf(w, "%s\t…\t…\t…\t…\n", table)
							break
						}
						fmt.Fprintf(w, "%s\t%s\t%s\t%v\t%s\n", table, column.Name, column.DataType, column.Nullable, column.Key)
					}
				}
				return w.Flush()
			}
			var schema tableSchema
			if _, err := client.Do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/sql/connections/%d/tables/%s/schema", conn.ID, args[1]), nil, &schema); err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(schema)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			if schema.Comment != "" {
				fmt.Fprintf(w, "-- %s\n", schema.Comment)
			}
			fmt.Fprintln(w, "COLUMN\tTYPE\tNULL\tDEFAULT\tKEY\tEXTRA")
			for _, column := range schema.Columns {
				fmt.Fprintf(w, "%s\t%s\t%v\t%v\t%s\t%s\n", column.Name, column.DataType, column.Nullable, displayValue(column.Default), column.Key, column.Extra)
			}
			fmt.Fprintln(w, "\nINDEX\tUNIQUE\tCOLUMNS")
			for _, index := range schema.Indexes {
				definition := index.Definition
				if definition == "" {
					definition = strings.Join(index.Columns, ", ")
				}
				fmt.Fprintf(w, "%s\t%v\t%s\n", index.Name, index.Unique, definition)
			}
			_ = w.Flush()
			if schema.DDL != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "\n%s\n", schema.DDL)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出")
	return cmd
}

// ---- query / status ----

func queryCommand() *cobra.Command {
	var timeout, wait time.Duration
	var tx, asJSON, asCSV, checkOnly bool
	var confirmToken string
	cmd := &cobra.Command{
		Use:   "query <连接名> -- <SQL...>",
		Short: "执行 SQL（可多语句；探索性 SELECT 请带 LIMIT 并用 --json / --csv）",
		Long: `SQL 原样提交执行。服务端先切分并分类：读操作直接执行；写操作按连接策略——
readonly 直接拒绝、confirm 进待批队列（批准后自动继续）、allow 直接执行。
黑名单（DROP DATABASE、无 WHERE 的 UPDATE/DELETE 等）任何策略下都会拦截（退出码 2）。
--tx 把多条语句包进一个事务，任一失败整体回滚。

审批协议（AI agent 必读）：confirm 策略下的写操作直接执行会返回退出码 5 并给出预检令牌——
先向用户完整展示这条 SQL 与影响，取得明确确认后，原样重跑并加 --confirm-token <令牌>；
SQL 随后进入待批队列，用户在 opsbox 窗口「数据库 → 待批准写操作」批准后才会执行。
--check 只预检不执行：不跑 SQL 也能拿到判定结果与预检令牌（无副作用）。`,
		Example: `  sqlctl query prod -- SELECT id, name FROM users LIMIT 20
  sqlctl query prod --json -- SELECT * FROM orders WHERE created_at > '2026-01-01' LIMIT 100
  sqlctl query prod --csv -- SELECT * FROM stocks
  sqlctl query prod --check -- "DELETE FROM logs WHERE id=1"                # 预检：返回是否需要审批 + 预检令牌
  sqlctl query prod --confirm-token <令牌> -- "DELETE FROM logs WHERE id=1" # 用户确认后带令牌重新提交
  sqlctl query prod --tx --timeout 120s -- "UPDATE a SET x=1 WHERE id=1" "INSERT INTO log VALUES (1)"`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if asJSON && asCSV {
				return cliapp.Exit(cliapp.ExitAPI, "--json 与 --csv 不能同时使用")
			}
			sqlText := strings.TrimSpace(strings.Join(args[1:], " "))
			if sqlText == "" {
				return cliapp.Exit(cliapp.ExitAPI, "SQL 不能为空")
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
				return cliapp.DoCheck(ctx, client, fmt.Sprintf("/api/v1/sql/connections/%d/query/check", conn.ID),
					map[string]any{"sql": sqlText}, cmd.OutOrStdout(), cmd.ErrOrStderr(), asJSON)
			}
			body := map[string]any{"sql": sqlText, "tx": tx}
			if timeout > 0 {
				body["timeoutSeconds"] = int(timeout.Seconds())
			}
			if confirmToken != "" {
				body["confirmToken"] = confirmToken
			}
			var result outcome
			if _, err := client.Do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/sql/connections/%d/query", conn.ID), body, &result); err != nil {
				if cliapp.IsCheckRequired(err) {
					return cliapp.ReportCheckRequired(err, "--confirm-token", cmd.ErrOrStderr())
				}
				return err
			}
			if result.Status == "pending" {
				if result, err = waitForQuery(ctx, client, result.ID, wait, cmd.ErrOrStderr()); err != nil {
					return err
				}
			}
			switch {
			case asJSON:
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			case asCSV:
				if result.Status != "success" {
					return report(result, false, io.Discard, cmd.ErrOrStderr())
				}
				if err := writeCSV(result, cmd.OutOrStdout()); err != nil {
					return cliapp.Exit(cliapp.ExitAPI, "输出 CSV 失败: %v", err)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "%d 行", result.RowsReturned)
				if result.Truncated {
					fmt.Fprintf(cmd.ErrOrStderr(), "（已截断）")
				}
				fmt.Fprintln(cmd.ErrOrStderr())
				return nil
			}
			return report(result, false, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "查询超时（默认取平台设置，上限 300s）")
	cmd.Flags().DurationVar(&wait, "wait", 5*time.Minute, "confirm 模式下等待批准的最长时间，0 表示不等待")
	cmd.Flags().BoolVar(&tx, "tx", false, "多语句单事务：任一失败整体回滚")
	cmd.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出完整结果（便于解析）")
	cmd.Flags().BoolVar(&asCSV, "csv", false, "以 CSV 输出结果行")
	cmd.Flags().BoolVar(&checkOnly, "check", false, "只预检不执行：返回是否需要平台审批、预检令牌与指引")
	cmd.Flags().StringVar(&confirmToken, "confirm-token", "", "审批预检令牌（需审批写操作在用户确认后携带它重新提交）")
	return cmd
}

func statusCommand() *cobra.Command {
	var wait time.Duration
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status <SQL ID>",
		Short: "查看（或继续等待）某条 SQL 的执行结果",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || id < 1 {
				return cliapp.Exit(cliapp.ExitAPI, "SQL ID 无效")
			}
			client, err := newClient()
			if err != nil {
				return err
			}
			result, err := waitForQuery(cmd.Context(), client, id, wait, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			return report(result, false, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().DurationVar(&wait, "wait", 0, "继续等待的最长时间，0 表示只查一次")
	cmd.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出")
	return cmd
}

// sessionSummary 是「已打开的控制台会话」列表项。会话即授权：列表里没有的连接，query 会被拒。
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
		Short:   "列出已打开的控制台会话（只有这些连接能被 query 操作）",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newClient()
			if err != nil {
				return err
			}
			var data struct {
				Items []sessionSummary `json:"items"`
			}
			if _, err := client.Do(cmd.Context(), http.MethodGet, "/api/v1/sql/sessions", nil, &data); err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(data.Items)
			}
			if len(data.Items) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "没有已打开的控制台会话。请先在 opsbox 窗口「数据库 → 控制台」打开一个连接的控制台。")
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
