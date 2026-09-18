package cliapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// 审批预检协议的 CLI 侧共享逻辑：三个运维 CLI（sshctl / sqlctl / redisctl）一致。
//
// 流程（对 AI agent）：
//  1. 直接执行命令。平台判定需要审批且未携带预检令牌时返回 *_CHECK_REQUIRED（退出码 5），
//     错误输出里已包含判定原因、预检令牌与下一步指引——这次被拒的响应本身就是预检结果。
//  2. 向用户完整展示命令与影响，取得明确确认。
//  3. 原样重跑同一条命令并加 --confirm-token <token>；命令进入平台待批队列，
//     用户在 opsbox 窗口的「待批准」里批准后才会真正执行。
//
// 也可以用 --check 显式预检（不执行、无副作用），在执行前就知道判定结果。

// IsCheckRequired 判断错误是否为审批预检拦截（*_CHECK_REQUIRED）。
func IsCheckRequired(err error) bool {
	return strings.HasSuffix(APIErrorCode(err), "_CHECK_REQUIRED")
}

// ReportCheckOutcome 输出预检结果并返回进程退出错误：
// execute 返回 nil；approval 返回 ExitApprovalNeeded；blocked 返回 ExitRejected。
func ReportCheckOutcome(check CheckOutcome, stdout, stderr io.Writer, asJSON bool) error {
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(check)
	}
	switch check.Action {
	case "execute":
		if !asJSON {
			fmt.Fprintf(stderr, "%s\n", check.Guidance)
		}
		return nil
	case "approval":
		if !asJSON {
			fmt.Fprintf(stderr, "%s\n预检令牌：%s\n（--check 仅预检不执行；确认后重新执行原命令并加 --confirm-token <token>）\n", check.Guidance, check.Token)
		}
		return &ExitError{Code: ExitApprovalNeeded}
	default: // blocked
		if !asJSON {
			fmt.Fprintf(stderr, "%s\n", check.Guidance)
		}
		return &ExitError{Code: ExitRejected}
	}
}

// ReportCheckRequired 输出 CHECK_REQUIRED 携带的预检结果与重试指引，恒返回 ExitApprovalNeeded。
// confirmFlag 是本 CLI 的确认标志名（--confirm-token）。
func ReportCheckRequired(err error, confirmFlag string, stderr io.Writer) error {
	var check CheckOutcome
	if !DecodeAPIErrorData(err, &check) {
		return err
	}
	fmt.Fprintf(stderr, "%s\n预检令牌：%s\n（已取消本次提交。向用户展示这条命令并取得明确确认后，原样重跑它并加上 %s %s，命令会进入待批队列）\n",
		check.Guidance, check.Token, confirmFlag, check.Token)
	return &ExitError{Code: ExitApprovalNeeded}
}

// DoCheck 调用预检接口并输出结果（--check 标志的实现）。
func DoCheck(ctx context.Context, client *Client, path string, body any, stdout, stderr io.Writer, asJSON bool) error {
	var check CheckOutcome
	if _, err := client.Do(ctx, http.MethodPost, path, body, &check); err != nil {
		return err
	}
	return ReportCheckOutcome(check, stdout, stderr, asJSON)
}
