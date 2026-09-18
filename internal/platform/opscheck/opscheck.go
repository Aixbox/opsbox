// Package opscheck 是 SSH / SQL / Redis 三个 AI 运维模块共享的「审批预检协议」：
//
// CLI（即 AI agent）提交需审批的操作时，平台不再直接入待批队列，而是返回一次预检结果
// （判定 + 确定性令牌 + 下一步指引）。agent 必须先向用户展示命令并取得确认，
// 再携带令牌重新提交，命令才会进入待批队列。这样把「知情同意」前置到用户必然在场的时刻，
// 避免 CLI 同步等待批准超时导致 agent 任务失败。
//
// 令牌是 HMAC 确定性签名（无 TTL、无状态）：同一连接 + 同一命令永远得到同一令牌。
// 它不是授权凭证——check 无副作用、人人可调，泄露无害；真正的安全闸门是
// 提交时的实时判定（黑名单 / 策略可能已变）与平台待批队列的人工批准。
package opscheck

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"opsbox/internal/platform/security"
)

// 判定结果。
const (
	ActionExecute  = "execute"  // 无需平台审批，直接执行
	ActionApproval = "approval" // 需要平台审批：先取得用户确认，带令牌重新提交，进待批队列
	ActionBlocked  = "blocked"  // 必然被拦截 / 拒绝（黑名单、只读策略等），不要提交
)

// Outcome 是一次预检的结果，随 CHECK_REQUIRED 错误响应与 check 接口返回给 CLI。
type Outcome struct {
	Action string `json:"action"`
	// Reason 是判定原因（命中规则 / 策略名），人可读。
	Reason string `json:"reason,omitempty"`
	// Token 在 action=approval 时签发，重新提交时放在 confirmToken 字段回传。
	Token string `json:"token,omitempty"`
	// Guidance 是给 agent 的下一步指引（协议内置 CLI 输出，不依赖 skill 安装）。
	Guidance string `json:"guidance"`
}

// RequiredError 表示「该操作需要平台审批，但提交时未携带（或不匹配）预检令牌」。
// 携带完整预检结果，handler 据此返回 CHECK_REQUIRED 响应；CLI 端据此打印指引。
type RequiredError struct {
	Check Outcome
}

func (e *RequiredError) Error() string { return e.Check.Guidance }

// Token 计算确定性预检令牌：HMAC(密钥, scope:connectionID:SHA256(command))。
// command 必须是提交时的规范化原文（各模块先 TrimSpace / 规整后再传入），
// 重新提交时逐字符一致才能通过校验。
func Token(cipher *security.TokenCipher, scope string, connectionID int64, command string) string {
	digest := sha256.Sum256([]byte(command))
	return cipher.Sign(scope + ":" + strconv.FormatInt(connectionID, 10) + ":" + hex.EncodeToString(digest[:]))
}

// VerifyToken 校验确定性预检令牌。
func VerifyToken(cipher *security.TokenCipher, scope string, connectionID int64, command, token string) bool {
	if token == "" {
		return false
	}
	return cipher.VerifySign(scope+":"+strconv.FormatInt(connectionID, 10)+":"+commandDigest(command), token)
}

func commandDigest(command string) string {
	digest := sha256.Sum256([]byte(command))
	return hex.EncodeToString(digest[:])
}

// GuidanceFor 返回各判定的标准指引文案。tool 是 CLI 名称（sshctl / sqlctl / redisctl），
// pendingEntry 是平台待批入口的展示名（如「SSH 管理 → 待批准」）。
func GuidanceFor(action, tool, pendingEntry, reason string) string {
	switch action {
	case ActionExecute:
		return "该操作无需平台审批，直接提交执行即可。"
	case ActionApproval:
		return fmt.Sprintf(
			"该操作需要平台审批（%s）。流程：1) 先向用户完整展示这条操作与影响，获得明确确认；"+
				"2) 确认后携带 --confirm-token <token> 重新执行同一条命令；"+
				"3) 提交后进入待批队列，用户在 opsbox 窗口「%s」批准后才会执行。"+
				"不要在未获用户确认时重新提交，也不要改写命令绕过。", reason, pendingEntry)
	default:
		return fmt.Sprintf("该操作会被平台拦截或拒绝（%s），不要提交，也不要改写绕过；把原因告知用户。", reason)
	}
}
