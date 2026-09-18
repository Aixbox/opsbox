package sshtest

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

// shell 是一个最小的交互式 shell：像真 PTY 一样回显输入，按 `;` 拆语句依次执行，
// 支持变量赋值 / 展开与 printf 转义，供终端会话（Web 终端 / CLI shell 共享接入）的测试使用。
//
// 真 shell 的回显由 readline 逐行完成；这里同样「读到一行就先回显再执行」，
// 好让会话层面对的字节序列与真实环境一致。
func (s *Server) shell(channel ssh.Channel) {
	out := &ptyWriter{w: channel}
	_, _ = io.WriteString(out, "test-shell$ ")
	vars := map[string]string{}
	var line []byte
	buffer := make([]byte, 1024)
	for {
		n, err := channel.Read(buffer)
		for _, b := range buffer[:n] {
			switch b {
			case '\r', '\n':
				_, _ = io.WriteString(out, "\r\n")
				s.runLine(out, string(line), vars)
				line = line[:0]
				_, _ = io.WriteString(out, "test-shell$ ")
			case 0x03: // Ctrl+C：丢弃当前行
				_, _ = io.WriteString(out, "^C\r\n")
				line = line[:0]
				_, _ = io.WriteString(out, "test-shell$ ")
			case 0x7f: // 退格
				if len(line) > 0 {
					line = line[:len(line)-1]
					_, _ = io.WriteString(out, "\b \b")
				}
			default:
				line = append(line, b)
				_, _ = out.Write([]byte{b}) // 回显
			}
		}
		if err != nil {
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			return
		}
	}
}

// runLine 执行一行（可含多条以 `;` 分隔的语句），维护 $? 供下一条语句读取。
func (s *Server) runLine(out io.Writer, line string, vars map[string]string) {
	if strings.TrimSpace(line) != "" {
		// 记进 Commands()：测试据此断言「被拦截的命令没有到达服务器」
		s.mu.Lock()
		s.commands = append(s.commands, line)
		s.mu.Unlock()
	}
	exit := 0
	for _, statement := range splitStatements(line) {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		vars["?"] = strconv.Itoa(exit)
		exit = s.runStatement(out, statement, vars)
	}
}

// runStatement 执行单条语句：变量赋值、printf，或交给 run 解释的固定命令。
func (s *Server) runStatement(out io.Writer, statement string, vars map[string]string) int {
	fields := tokenize(statement, vars)
	if len(fields) == 0 {
		return 0
	}
	// VAR=value 形式的赋值
	if name, value, ok := strings.Cut(fields[0], "="); ok && len(fields) == 1 && isName(name) {
		vars[name] = value
		return 0
	}
	if fields[0] == "printf" && len(fields) >= 2 {
		_, _ = io.WriteString(out, formatPrintf(fields[1], fields[2:]))
		return 0
	}
	return s.runShellCommand(out, fields)
}

// runShellCommand 复用 exec 通道那套固定命令，但 PTY 下 stdout / stderr 合流。
func (s *Server) runShellCommand(out io.Writer, fields []string) int {
	switch fields[0] {
	case "echo":
		_, _ = io.WriteString(out, strings.Join(fields[1:], " ")+"\n")
		return 0
	case "exit":
		code := 0
		if len(fields) > 1 {
			code, _ = strconv.Atoi(fields[1])
		}
		_, _ = io.WriteString(out, "failing on purpose\n")
		return code
	case "yes":
		n := 0
		if len(fields) > 1 {
			n, _ = strconv.Atoi(fields[1])
		}
		for i := 0; i < n; i++ {
			_, _ = fmt.Fprintf(out, "line %06d\n", i)
		}
		return 0
	case "uname":
		_, _ = io.WriteString(out, "Linux sshtest 6.0 #1 SMP x86_64 GNU/Linux\n")
		return 0
	case "true":
		return 0
	case "false":
		return 1
	}
	_, _ = io.WriteString(out, "sh: "+fields[0]+": command not found\n")
	return 127
}

// splitStatements 按 `;` 拆语句，跳过引号内的分号。
func splitStatements(line string) []string {
	var parts []string
	var current strings.Builder
	var quote rune
	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
			current.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			current.WriteRune(r)
		case r == ';':
			parts = append(parts, current.String())
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	return append(parts, current.String())
}

// tokenize 按空白切词，处理引号与 $VAR 展开（双引号内展开，单引号内不展开）。
func tokenize(statement string, vars map[string]string) []string {
	var fields []string
	var current strings.Builder
	var quote rune
	started := false
	flush := func() {
		if started {
			fields = append(fields, current.String())
			current.Reset()
			started = false
		}
	}
	runes := []rune(statement)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case quote == '"':
			if r == '"' {
				quote = 0
			} else if r == '$' {
				name, width := readVarName(runes[i+1:])
				current.WriteString(vars[name])
				i += width
			} else {
				current.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			started = true
		case r == ' ' || r == '\t':
			flush()
		case r == '$':
			name, width := readVarName(runes[i+1:])
			current.WriteString(vars[name])
			i += width
			started = true
		default:
			current.WriteRune(r)
			started = true
		}
		if quote != 0 {
			started = true
		}
	}
	flush()
	return fields
}

// readVarName 读出 $ 后面的变量名，返回名字与消耗的字符数。
func readVarName(runes []rune) (string, int) {
	if len(runes) > 0 && runes[0] == '?' {
		return "?", 1
	}
	var name strings.Builder
	count := 0
	for _, r := range runes {
		if r != '_' && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			break
		}
		name.WriteRune(r)
		count++
	}
	return name.String(), count
}

func isName(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if r != '_' && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// formatPrintf 解释 printf 的转义（\033 \n \\）与 %d / %s 占位符。
func formatPrintf(format string, args []string) string {
	var out strings.Builder
	next := 0
	runes := []rune(format)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\\' && i+1 < len(runes) {
			i++
			switch runes[i] {
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			case '\\':
				out.WriteByte('\\')
			case '0': // 八进制：\033 这类
				digits := ""
				for j := i + 1; j < len(runes) && len(digits) < 2 && runes[j] >= '0' && runes[j] <= '7'; j++ {
					digits += string(runes[j])
				}
				value, _ := strconv.ParseInt("0"+digits, 8, 32)
				out.WriteByte(byte(value))
				i += len(digits)
			default:
				out.WriteRune(runes[i])
			}
			continue
		}
		if r == '%' && i+1 < len(runes) {
			i++
			if runes[i] == '%' {
				out.WriteByte('%')
				continue
			}
			value := ""
			if next < len(args) {
				value = args[next]
				next++
			}
			out.WriteString(value)
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

// ptyWriter 把 \n 翻成 \r\n，模拟 PTY 的行尾转换（哨兵截取要处理这个）。
type ptyWriter struct{ w io.Writer }

func (p *ptyWriter) Write(data []byte) (int, error) {
	converted := strings.ReplaceAll(string(data), "\n", "\r\n")
	converted = strings.ReplaceAll(converted, "\r\r\n", "\r\n")
	if _, err := p.w.Write([]byte(converted)); err != nil {
		return 0, err
	}
	return len(data), nil
}
