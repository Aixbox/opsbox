package sshx

import (
	"context"
	"errors"
	"io"
	"time"

	"golang.org/x/crypto/ssh"
)

// ExecOptions 控制单命令执行。
type ExecOptions struct {
	// PTY 为 true 时申请伪终端（给 sudo / 交互式安装脚本探测 TTY 用）；stderr 会并入 stdout。
	PTY bool
	// OutputLimit 是 stdout / stderr 各自的保留上限（字节）；<=0 不限制。
	OutputLimit int
	// Stream 非空时，stdout / stderr 的每一片输出都会实时同步写给它（不受 OutputLimit 约束）。
	// 会话用它把命令输出回显进终端。
	Stream io.Writer
}

// ExecResult 是单命令的执行结果；Stdout/Stderr 已按 OutputLimit 截断。
type ExecResult struct {
	ExitCode    int
	Stdout      []byte
	Stderr      []byte
	StdoutBytes int
	StderrBytes int
	Truncated   bool
	Duration    time.Duration
	// TimedOut 表示因 ctx 到期被终止；此时 ExitCode 无意义（置 -1）。
	TimedOut bool
}

// RunExec 在已连接的 client 上执行一条命令；ctx 到期时向远端发 KILL 并关闭会话。
// 返回的 error 只表示「无法执行」（开会话失败等），远端非零退出码不算 error。
func RunExec(ctx context.Context, client *Client, command string, options ExecOptions) (ExecResult, error) {
	started := time.Now()
	session, err := client.NewSession()
	if err != nil {
		return ExecResult{}, err
	}
	defer session.Close()
	stdout := NewLimitedBuffer(options.OutputLimit)
	stderr := NewLimitedBuffer(options.OutputLimit)
	session.Stdout, session.Stderr = stdout, stderr
	if options.Stream != nil {
		session.Stdout = io.MultiWriter(stdout, options.Stream)
		session.Stderr = io.MultiWriter(stderr, options.Stream)
	}
	if options.PTY {
		modes := ssh.TerminalModes{ssh.ECHO: 0, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
		if err := session.RequestPty("xterm-256color", 40, 160, modes); err != nil {
			return ExecResult{}, err
		}
	}
	if err := session.Start(command); err != nil {
		return ExecResult{}, err
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- session.Wait() }()
	result := ExecResult{}
	select {
	case err = <-waitErr:
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		result.TimedOut = true
		// 给远端一点时间回收；不等太久，避免卡住调用方
		select {
		case <-waitErr:
		case <-time.After(2 * time.Second):
		}
		err = nil
	}
	result.Duration = time.Since(started)
	result.Stdout, result.Truncated = stdout.Bytes()
	var stderrTruncated bool
	result.Stderr, stderrTruncated = stderr.Bytes()
	result.Truncated = result.Truncated || stderrTruncated
	result.StdoutBytes = stdout.Total()
	result.StderrBytes = stderr.Total()
	if result.TimedOut {
		result.ExitCode = -1
		return result, nil
	}
	var exitErr *ssh.ExitError
	switch {
	case err == nil:
		result.ExitCode = 0
	case errors.As(err, &exitErr):
		result.ExitCode = exitErr.ExitStatus()
	default:
		// ExitMissingError 等：远端被信号杀掉且没有返回状态
		result.ExitCode = -1
		return result, err
	}
	return result, nil
}
