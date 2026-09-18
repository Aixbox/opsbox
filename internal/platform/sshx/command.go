package sshx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrExecBusy 表示等待会话上前一条 CLI 命令结束时超时。
var ErrExecBusy = errors.New("该终端会话上前一条 CLI 命令尚未结束")

// RunCommand 在会话所属的 SSH 连接上另开一个 exec 通道执行命令，并把命令与输出实时回显进会话输出流，
// 正看着终端的人能看到 AI 在做什么。
//
// 不写 PTY stdin：命令有自己独立的 shell，与用户前台正在跑什么（vim / top / 交互式程序）无关，
// 退出码由 SSH 协议直接给出，stdout / stderr 各自独立。代价是不共享交互 shell 的状态
// （cwd / 环境变量 / 别名）——命令从登录目录开始执行，需要切目录就写进命令里。
//
// 同一会话上的命令串行执行：前一条没结束时后一条排队等待（占用自己的超时预算），
// 等到 ctx 到期仍拿不到执行权返回 ErrExecBusy。
func (s *Session) RunCommand(ctx context.Context, command string, options ExecOptions) (ExecResult, error) {
	if s.IsClosed() {
		return ExecResult{}, ErrSessionNotFound
	}
	select {
	case s.execSlot <- struct{}{}:
	case <-ctx.Done():
		return ExecResult{}, ErrExecBusy
	case <-s.done:
		return ExecResult{}, ErrSessionNotFound
	}
	defer func() { <-s.execSlot }()
	if s.IsClosed() {
		return ExecResult{}, ErrSessionNotFound
	}
	s.echo(commandBanner(command))
	s.touch()
	options.Stream = &echoWriter{session: s}
	result, err := RunExec(ctx, s.client, command, options)
	s.echo(resultBanner(result, err))
	s.touch()
	return result, err
}

// echo 把一段终端可直接显示的文本投进输出流（记入 scrollback，新接入的客户端也能看到）。
func (s *Session) echo(text string) {
	s.broadcast([]byte(text))
}

// echoWriter 把命令输出转成终端形式（\n → \r\n）后回显进会话输出流。
type echoWriter struct{ session *Session }

func (w *echoWriter) Write(data []byte) (int, error) {
	w.session.echo(toTerminal(string(data)))
	return len(data), nil
}

// toTerminal 把裸换行翻成 PTY 风格的 \r\n；已带 \r 的行不再重复。
func toTerminal(text string) string {
	text = strings.ReplaceAll(text, "\n", "\r\n")
	return strings.ReplaceAll(text, "\r\r\n", "\r\n")
}

// commandBanner 是命令开始时的提示行：青色加粗，与用户自己敲的命令区分开。
func commandBanner(command string) string {
	command = strings.TrimRight(command, "\r\n")
	return "\r\n\x1b[1;36m[CLI] $ " + toTerminal(command) + "\x1b[0m\r\n"
}

// resultBanner 是命令结束时的提示行：成功青色、失败红色、超时黄色。
func resultBanner(result ExecResult, err error) string {
	elapsed := result.Duration.Round(time.Millisecond)
	switch {
	case result.TimedOut:
		return fmt.Sprintf("\x1b[1;33m[CLI] 超时，已终止 · %s\x1b[0m\r\n", elapsed)
	case err != nil:
		return fmt.Sprintf("\x1b[1;31m[CLI] 执行失败：%s\x1b[0m\r\n", toTerminal(err.Error()))
	case result.ExitCode == 0:
		return fmt.Sprintf("\x1b[1;36m[CLI] exit 0 · %s\x1b[0m\r\n", elapsed)
	default:
		return fmt.Sprintf("\x1b[1;31m[CLI] exit %d · %s\x1b[0m\r\n", result.ExitCode, elapsed)
	}
}
