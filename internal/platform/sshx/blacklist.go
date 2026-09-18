// Package sshx 封装平台托管 SSH 连接的底层能力：拨号（host key TOFU）、单命令执行、
// SFTP 流式传输、PTY 会话管理，以及黑名单匹配 / 输出截断等纯函数。
// 业务策略（审批、审计、权限）在 modules/ssh，本包不碰数据库。
package sshx

import (
	"fmt"
	"regexp"
	"strings"
)

// Blacklist 是编译好的黑名单规则集；每条规则是不区分大小写的 Go 正则。
type Blacklist struct {
	patterns []*regexp.Regexp
	sources  []string
}

// CompileBlacklist 编译黑名单模式串；任一条非法正则即返回错误（保存设置时拒绝）。
func CompileBlacklist(patterns []string) (*Blacklist, error) {
	list := &Blacklist{}
	for _, raw := range patterns {
		source := strings.TrimSpace(raw)
		if source == "" {
			continue
		}
		compiled, err := regexp.Compile("(?i)" + source)
		if err != nil {
			return nil, fmt.Errorf("黑名单规则 %q 不是合法正则: %w", source, err)
		}
		list.patterns = append(list.patterns, compiled)
		list.sources = append(list.sources, source)
	}
	return list, nil
}

// Match 返回命中的第一条规则源串；未命中返回 ("", false)。
// 匹配前把换行/制表折叠成单个空格，避免用换行绕过 `rm -rf /` 这类以空白分隔的规则。
func (b *Blacklist) Match(command string) (string, bool) {
	if b == nil {
		return "", false
	}
	normalized := strings.Join(strings.Fields(command), " ")
	for i, pattern := range b.patterns {
		if pattern.MatchString(normalized) || pattern.MatchString(command) {
			return b.sources[i], true
		}
	}
	return "", false
}

// Len 返回规则条数。
func (b *Blacklist) Len() int {
	if b == nil {
		return 0
	}
	return len(b.patterns)
}
