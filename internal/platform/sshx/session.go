package sshx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrSessionNotFound 表示会话不存在或已被回收。
var ErrSessionNotFound = errors.New("终端会话不存在或已关闭")

// ErrSessionAttached 表示会话已有客户端接入（保留给需要独占接入的场景）。
var ErrSessionAttached = errors.New("终端会话已被其他客户端接入")

// scrollbackLimit 是每个会话保留的历史输出上限：新客户端接入 / 断线重连时先补这段，避免空屏。
const scrollbackLimit = 64 << 10

// subscriberBuffer 是 channel 订阅者的缓冲深度；写满即丢弃（显示类订阅者不做背压，
// 否则一个卡住的浏览器会拖垮整个会话，连带阻塞 CLI 命令的输出回显）。
const subscriberBuffer = 256

// keepaliveInterval 是向远端发送 keepalive 的间隔。会话是持久的——用户关掉面板后它仍留在服务端，
// 无人接入的空闲 TCP 连接会被 NAT / 防火墙悄悄掐掉，不保活的话用户回来时只看到「会话已结束」。
const keepaliveInterval = 30 * time.Second

// Session 是一条交互式 PTY 会话：持有 SSH 连接与 shell 会话，
// 输出经 sink 列表同步分发给所有订阅者（Web 终端、CLI shell）。
//
// 会话在服务端持久保留：Web 面板关掉只是 Detach，会话与远端连接都还在，重新打开面板即可接回
// （scrollback 回放历史）；只有用户主动「结束会话」或闲置超过回收阈值才真正关闭。
type Session struct {
	ID string
	// Seq 是进程内单调递增的序号——给人看的短标识，界面上显示为「会话 #12」，
	// 同一连接开了多个会话时靠它区分与切换。
	Seq          int64
	ConnectionID int64
	UserID       int64
	LogID        int64
	CreatedAt    time.Time

	client  *Client
	session *ssh.Session
	stdin   io.WriteCloser
	done    chan struct{}

	// emitMu 串行化输出分发与订阅/退订，保证订阅者看到的字节顺序一致，
	// 且 replay 的历史与后续增量之间不会插入其他 chunk。
	emitMu     sync.Mutex
	sinks      map[int64]func([]byte)
	nextSink   int64
	scrollback []byte

	// execSlot 串行化 CLI 命令：同一会话同一时刻只跑一条，终端里的回显才不会互相穿插。
	// 容量 1 的 channel 而不是 Mutex，是为了能带 ctx 等待（前一条没结束时排队而不是立刻拒绝）。
	execSlot chan struct{}

	mu          sync.Mutex
	name        string
	attachCount int
	lastSeen    time.Time
	closeErr    error
	closed      bool
}

// SessionManager 维护进程内的 PTY 会话表（单实例部署，状态放内存即可），并按闲置时间回收。
type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	idle     time.Duration
	onClose  func(*Session)
	seq      atomic.Int64
}

// NewSessionManager 创建会话管理器；idle 是「无人接入且无 IO」的回收阈值。
// onClose 在会话结束时回调（模块层用它收尾审计日志）。
func NewSessionManager(idle time.Duration, onClose func(*Session)) *SessionManager {
	if idle <= 0 {
		idle = time.Hour
	}
	return &SessionManager{sessions: map[string]*Session{}, idle: idle, onClose: onClose}
}

// Create 在 client 上开一个带 PTY 的 shell 会话并登记。会话所有权（连接）转移给 Session，关闭时一并断开。
func (m *SessionManager) Create(id string, client *Client, connectionID, userID, logID int64, rows, cols int) (*Session, error) {
	sshSession, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 80
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sshSession.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		_ = sshSession.Close()
		return nil, err
	}
	stdin, err := sshSession.StdinPipe()
	if err != nil {
		_ = sshSession.Close()
		return nil, err
	}
	stdout, err := sshSession.StdoutPipe()
	if err != nil {
		_ = sshSession.Close()
		return nil, err
	}
	stderr, err := sshSession.StderrPipe()
	if err != nil {
		_ = sshSession.Close()
		return nil, err
	}
	if err := sshSession.Shell(); err != nil {
		_ = sshSession.Close()
		return nil, err
	}
	now := time.Now()
	s := &Session{
		ID: id, Seq: m.seq.Add(1), ConnectionID: connectionID, UserID: userID, LogID: logID, CreatedAt: now,
		client: client, session: sshSession, stdin: stdin,
		done: make(chan struct{}), sinks: map[int64]func([]byte){}, execSlot: make(chan struct{}, 1), lastSeen: now,
	}
	go s.pump(stdout)
	go s.pump(stderr)
	go s.keepalive()
	go func() {
		err := sshSession.Wait()
		s.finish(err)
		m.remove(s)
	}()
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()
	return s, nil
}

// Get 返回会话；不存在返回 ErrSessionNotFound。
func (m *SessionManager) Get(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return s, nil
}

// Close 主动关闭会话。
func (m *SessionManager) Close(id string) error {
	s, err := m.Get(id)
	if err != nil {
		return err
	}
	s.Close()
	return nil
}

// snapshot 返回当前全部会话（无锁遍历用）。
func (m *SessionManager) snapshot() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	return all
}

// byRecent 按最近活动时间倒序，最活跃的排在最前（CLI 未指定会话时默认选它）。
func byRecent(items []*Session) []*Session {
	sort.SliceStable(items, func(i, j int) bool { return items[i].LastSeen().After(items[j].LastSeen()) })
	return items
}

// FindByConnection 返回该用户在指定连接上已打开的会话，按最近活动时间倒序。
// CLI 的「必须先打开终端」门禁与目标会话选择都基于它。
func (m *SessionManager) FindByConnection(connectionID, userID int64) []*Session {
	var found []*Session
	for _, s := range m.snapshot() {
		if s.ConnectionID == connectionID && s.UserID == userID && !s.IsClosed() {
			found = append(found, s)
		}
	}
	return byRecent(found)
}

// List 返回该用户当前打开的全部会话，按最近活动时间倒序。
func (m *SessionManager) List(userID int64) []*Session {
	var found []*Session
	for _, s := range m.snapshot() {
		if s.UserID == userID && !s.IsClosed() {
			found = append(found, s)
		}
	}
	return byRecent(found)
}

// Run 周期回收闲置会话，直到 ctx 结束；结束时关闭全部会话。
//
// 只回收「无人接入且超过 idle 没有任何 IO」的会话：面板关掉但 CLI 还在用的会话会被 IO 持续续命，
// 真正被遗忘的才会被收掉。
func (m *SessionManager) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			for _, s := range m.snapshot() {
				s.Close()
			}
			return
		case now := <-ticker.C:
			var stale []*Session
			for _, s := range m.snapshot() {
				s.mu.Lock()
				if s.attachCount == 0 && now.Sub(s.lastSeen) > m.idle {
					stale = append(stale, s)
				}
				s.mu.Unlock()
			}
			for _, s := range stale {
				s.Close()
			}
		}
	}
}

// Count 返回当前活跃会话数。
func (m *SessionManager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

func (m *SessionManager) remove(s *Session) {
	m.mu.Lock()
	if current, ok := m.sessions[s.ID]; ok && current == s {
		delete(m.sessions, s.ID)
	}
	m.mu.Unlock()
	if m.onClose != nil {
		m.onClose(s)
	}
}

// ---- 输出分发 ----

func (s *Session) pump(reader io.Reader) {
	buffer := make([]byte, 32*1024)
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			s.broadcast(buffer[:n])
			s.touch()
		}
		if err != nil {
			return
		}
	}
}

// broadcast 把一段输出同步分发给所有 sink，并追加到 scrollback。
// sink 必须是快操作（追加缓冲 / 非阻塞投递），不得回调 Session 的方法，否则死锁。
func (s *Session) broadcast(chunk []byte) {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	s.scrollback = append(s.scrollback, chunk...)
	if len(s.scrollback) > scrollbackLimit {
		s.scrollback = append([]byte(nil), s.scrollback[len(s.scrollback)-scrollbackLimit:]...)
	}
	for _, sink := range s.sinks {
		sink(chunk)
	}
}

// Subscribe 注册一个输出 sink，返回退订函数。replay 为 true 时先把 scrollback 回放给它。
func (s *Session) Subscribe(fn func([]byte), replay bool) func() {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	if replay && len(s.scrollback) > 0 {
		fn(append([]byte(nil), s.scrollback...))
	}
	s.nextSink++
	id := s.nextSink
	s.sinks[id] = fn
	return func() {
		s.emitMu.Lock()
		defer s.emitMu.Unlock()
		delete(s.sinks, id)
	}
}

// Subscriber 是 channel 形式的订阅者，供 WebSocket 桥接使用。
// 缓冲写满时丢弃新数据（显示类订阅者不做背压）。
type Subscriber struct {
	ch    chan []byte
	unsub func()
	once  sync.Once
}

// NewSubscriber 创建 channel 订阅者；replay 为 true 时先收到历史输出。
func (s *Session) NewSubscriber(replay bool) *Subscriber {
	sub := &Subscriber{ch: make(chan []byte, subscriberBuffer)}
	sub.unsub = s.Subscribe(func(chunk []byte) {
		select {
		case sub.ch <- append([]byte(nil), chunk...):
		default: // 订阅者跟不上：丢弃这一片，保证其他订阅者不受影响
		}
	}, replay)
	return sub
}

// C 返回输出通道。
func (sub *Subscriber) C() <-chan []byte { return sub.ch }

// Close 退订（幂等）。
func (sub *Subscriber) Close() { sub.once.Do(sub.unsub) }

// ---- 保活 ----

// keepalive 周期性向远端发 keepalive 请求；连接已死（出错或迟迟无应答）时关闭会话，
// 让它进入正常的收尾流程，而不是留一具僵尸在会话表里骗过门禁。
func (s *Session) keepalive() {
	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			replied := make(chan error, 1)
			go func() {
				_, _, err := s.client.SendRequest("keepalive@openssh.com", true, nil)
				replied <- err
			}()
			select {
			case err := <-replied:
				if err != nil {
					s.Close()
					return
				}
			case <-time.After(keepaliveInterval):
				s.Close()
				return
			case <-s.done:
				return
			}
		}
	}
}

// ---- 接入与状态 ----

func (s *Session) touch() {
	s.mu.Lock()
	s.lastSeen = time.Now()
	s.mu.Unlock()
}

// Attach 登记一个接入的客户端。Web 终端与 CLI shell 可同时接入同一会话（tmux 式共享）。
func (s *Session) Attach() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.attachCount++
	s.lastSeen = time.Now()
	return true
}

// Detach 注销一个接入的客户端。降到 0 后会话仍然保留（持久会话），直到闲置回收或用户主动结束。
func (s *Session) Detach() {
	s.mu.Lock()
	if s.attachCount > 0 {
		s.attachCount--
	}
	s.lastSeen = time.Now()
	s.mu.Unlock()
}

// AttachCount 返回当前接入的客户端数。
func (s *Session) AttachCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attachCount
}

// Name 返回用户给会话起的名字；空串表示没起过（界面显示默认的「会话 #N」）。
func (s *Session) Name() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.name
}

// SetName 设置会话名字（调用方负责校验）。
func (s *Session) SetName(name string) {
	s.mu.Lock()
	s.name = name
	s.lastSeen = time.Now()
	s.mu.Unlock()
}

// LastSeen 返回最近一次 IO / 接入变动的时间。
func (s *Session) LastSeen() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSeen
}

// IsClosed 返回会话是否已结束。
func (s *Session) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Done 在会话结束时关闭。
func (s *Session) Done() <-chan struct{} { return s.done }

// Err 返回会话结束原因（正常退出为 nil）。
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeErr
}

// Write 把用户输入写到远端 stdin。
func (s *Session) Write(data []byte) error {
	s.touch()
	_, err := s.stdin.Write(data)
	return err
}

// Resize 调整远端终端尺寸。
func (s *Session) Resize(rows, cols int) error {
	if rows <= 0 || cols <= 0 {
		return fmt.Errorf("终端尺寸无效")
	}
	s.touch()
	return s.session.WindowChange(rows, cols)
}

// Close 关闭会话与底层 SSH 连接（幂等）。同一连接上正在跑的 CLI 命令通道也会随之断开。
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.stdin.Close()
	_ = s.session.Close()
	_ = s.client.Close()
}

func (s *Session) finish(err error) {
	s.mu.Lock()
	if s.closeErr == nil {
		var exitErr *ssh.ExitError
		var missing *ssh.ExitMissingError
		// 非零退出码 / 被本端关闭导致没有 exit-status 都属于正常收尾
		if err != nil && !errors.As(err, &exitErr) && !errors.As(err, &missing) && !errors.Is(err, io.EOF) {
			s.closeErr = err
		}
	}
	s.closed = true
	s.mu.Unlock()
	close(s.done)
	_ = s.client.Close()
}
