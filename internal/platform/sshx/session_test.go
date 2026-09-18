package sshx

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"opsbox/internal/platform/sshx/sshtest"
)

// newTestSession 连上测试服务器并开一条 PTY 会话。
func newTestSession(t *testing.T) (*SessionManager, *Session) {
	t.Helper()
	server, err := sshtest.Start("u", "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	client, err := NetDialer{}.Dial(context.Background(), Target{Host: "127.0.0.1", Port: server.Port, Username: "u", AuthType: "password", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewSessionManager(time.Minute, nil)
	session, err := manager.Create("s1", client, 7, 42, 100, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	return manager, session
}

func runCommand(t *testing.T, session *Session, command string, timeout time.Duration) (ExecResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return session.RunCommand(ctx, command, ExecOptions{})
}

// CLI 命令走会话连接上的独立 exec 通道：输出与退出码由 SSH 协议直接给出，不经过 PTY。
func TestRunCommandEndToEnd(t *testing.T) {
	_, session := newTestSession(t)

	result, err := runCommand(t, session, "echo hello world", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(result.Stdout)) != "hello world" {
		t.Fatalf("stdout = %q, want %q", result.Stdout, "hello world")
	}
	if result.ExitCode != 0 {
		t.Fatalf("退出码 = %d, want 0", result.ExitCode)
	}
}

// 非零退出码要原样透传（CLI 靠它决定成败），且 stderr 与 stdout 各自独立。
func TestRunCommandExitCodeAndStderr(t *testing.T) {
	_, session := newTestSession(t)

	result, err := runCommand(t, session, "exit 3", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 3 {
		t.Fatalf("退出码 = %d, want 3", result.ExitCode)
	}
	if !strings.Contains(string(result.Stderr), "failing on purpose") || len(result.Stdout) != 0 {
		t.Fatalf("stderr 应独立于 stdout: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}
}

// 连续执行多条命令，彼此的输出不能串台。
func TestRunCommandSequential(t *testing.T) {
	_, session := newTestSession(t)

	for _, want := range []string{"alpha", "bravo", "charlie"} {
		result, err := runCommand(t, session, "echo "+want, 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(result.Stdout)); got != want {
			t.Fatalf("输出 = %q, want %q（命令间输出串台）", got, want)
		}
	}
}

// 命令与输出必须回显给会话的订阅者——这正是「CLI 操作在终端里可见」。
func TestRunCommandVisibleToSubscribers(t *testing.T) {
	_, session := newTestSession(t)

	var mu sync.Mutex
	var seen strings.Builder
	unsubscribe := session.Subscribe(func(chunk []byte) {
		mu.Lock()
		seen.Write(chunk)
		mu.Unlock()
	}, false)
	defer unsubscribe()

	if _, err := runCommand(t, session, "echo visible-to-user", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	text := seen.String()
	mu.Unlock()
	for _, want := range []string{"[CLI] $ echo visible-to-user", "visible-to-user\r\n", "[CLI] exit 0"} {
		if !strings.Contains(text, want) {
			t.Fatalf("订阅者应看到 %q，实际: %q", want, text)
		}
	}
	// 回显也要进 scrollback：之后接入的客户端同样能看到 CLI 做过什么
	sub := session.NewSubscriber(true)
	defer sub.Close()
	select {
	case chunk := <-sub.C():
		if !strings.Contains(string(chunk), "visible-to-user") {
			t.Fatalf("replay 应含 CLI 回显: %q", chunk)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("replay 未送达")
	}
}

// exec 通道与 PTY 无关：即便前台 shell 正在等一行输入（模拟停在交互式程序里），命令照样执行。
func TestRunCommandIndependentOfForeground(t *testing.T) {
	_, session := newTestSession(t)

	// 往 PTY 里敲半行不回车——前台 shell 停在 readline 里
	if err := session.Write([]byte("vim notes.txt")); err != nil {
		t.Fatal(err)
	}
	result, err := runCommand(t, session, "uname", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Stdout), "Linux") || result.ExitCode != 0 {
		t.Fatalf("前台被占用时命令仍应执行: %+v", result)
	}
}

// 超时要杀掉命令并标记 TimedOut，不能无限等。
func TestRunCommandTimeout(t *testing.T) {
	_, session := newTestSession(t)

	result, err := runCommand(t, session, "sleep 5", 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut || result.ExitCode != -1 {
		t.Fatalf("应报告超时: %+v", result)
	}
}

// 同一会话串行执行：前一条占着执行权时，后一条在自己的预算内等不到就报 ErrExecBusy。
func TestRunCommandBusy(t *testing.T) {
	_, session := newTestSession(t)

	started := make(chan struct{})
	go func() {
		close(started)
		_, _ = runCommand(t, session, "sleep 2", 10*time.Second)
	}()
	<-started
	time.Sleep(200 * time.Millisecond)
	if _, err := runCommand(t, session, "echo later", 300*time.Millisecond); err != ErrExecBusy {
		t.Fatalf("err = %v, want ErrExecBusy", err)
	}
	// 前一条结束后能继续
	result, err := runCommand(t, session, "echo later", 10*time.Second)
	if err != nil || strings.TrimSpace(string(result.Stdout)) != "later" {
		t.Fatalf("排队后应能执行: %+v err=%v", result, err)
	}
}

// 会话关闭后执行要报 ErrSessionNotFound，不能静默成功。
func TestRunCommandClosedSession(t *testing.T) {
	_, session := newTestSession(t)

	session.Close()
	if _, err := runCommand(t, session, "echo x", 5*time.Second); err != ErrSessionNotFound {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
}

// 多客户端可同时接入同一会话（Web 终端 + CLI shell 共享），且都能收到输出。
func TestSessionMultipleSubscribers(t *testing.T) {
	_, session := newTestSession(t)

	if !session.Attach() || !session.Attach() {
		t.Fatal("应允许多客户端接入")
	}
	if session.AttachCount() != 2 {
		t.Fatalf("接入数 = %d, want 2", session.AttachCount())
	}
	first, second := session.NewSubscriber(false), session.NewSubscriber(false)
	defer first.Close()
	defer second.Close()

	if _, err := runCommand(t, session, "echo broadcast", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	for index, sub := range []*Subscriber{first, second} {
		var got strings.Builder
		deadline := time.After(5 * time.Second)
		for !strings.Contains(got.String(), "broadcast") {
			select {
			case chunk := <-sub.C():
				got.Write(chunk)
			case <-deadline:
				t.Fatalf("订阅者 %d 未收到输出: %q", index, got.String())
			}
		}
	}
	session.Detach()
	if session.AttachCount() != 1 {
		t.Fatalf("Detach 后接入数 = %d, want 1", session.AttachCount())
	}
}

// 会话是持久的：所有客户端 Detach 后会话仍然存活，重新接入能拿到历史输出（断线重连 / 关掉面板再打开不空屏）。
func TestSessionSurvivesDetach(t *testing.T) {
	manager, session := newTestSession(t)

	if !session.Attach() {
		t.Fatal("接入失败")
	}
	if err := session.Write([]byte("echo persisted\n")); err != nil {
		t.Fatal(err)
	}
	session.Detach()
	if session.AttachCount() != 0 || session.IsClosed() {
		t.Fatalf("Detach 后会话应保留: attached=%d closed=%v", session.AttachCount(), session.IsClosed())
	}
	if got := manager.FindByConnection(session.ConnectionID, session.UserID); len(got) != 1 {
		t.Fatalf("无人接入的会话仍应在列表里, got %d", len(got))
	}
	// 重新接入：replay 补回此前的输出
	if !session.Attach() {
		t.Fatal("重新接入失败")
	}
	sub := session.NewSubscriber(true)
	defer sub.Close()
	var got strings.Builder
	deadline := time.After(5 * time.Second)
	for !strings.Contains(got.String(), "persisted") {
		select {
		case chunk := <-sub.C():
			got.Write(chunk)
		case <-deadline:
			t.Fatalf("重新接入后应回放历史: %q", got.String())
		}
	}
}

// 序号单调递增，供界面显示「会话 #N」并在多会话间切换。
func TestSessionSeq(t *testing.T) {
	manager, first := newTestSession(t)
	server, err := sshtest.Start("u", "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	client, err := NetDialer{}.Dial(context.Background(), Target{Host: "127.0.0.1", Port: server.Port, Username: "u", AuthType: "password", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Create("s2", client, 7, 42, 101, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Close)
	if first.Seq != 1 || second.Seq != 2 {
		t.Fatalf("seq = %d, %d; want 1, 2", first.Seq, second.Seq)
	}
}

// FindByConnection 只返回该用户在该连接上的会话——这是 CLI 门禁的判定依据。
func TestFindByConnectionScoping(t *testing.T) {
	manager, session := newTestSession(t)

	if got := manager.FindByConnection(session.ConnectionID, session.UserID); len(got) != 1 {
		t.Fatalf("应找到 1 个会话, got %d", len(got))
	}
	if got := manager.FindByConnection(session.ConnectionID, session.UserID+1); len(got) != 0 {
		t.Fatalf("别的用户不应看到该会话, got %d", len(got))
	}
	if got := manager.FindByConnection(session.ConnectionID+1, session.UserID); len(got) != 0 {
		t.Fatalf("别的连接不应匹配, got %d", len(got))
	}
	session.Close()
	if got := manager.FindByConnection(session.ConnectionID, session.UserID); len(got) != 0 {
		t.Fatalf("已关闭的会话不应再返回, got %d", len(got))
	}
}
