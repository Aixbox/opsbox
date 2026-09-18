// Package console 是 Redis / SQL 的「控制台会话」：与 SSH 的 PTY 会话同一心智模型——
// 会话打开即授权、关闭即失权，CLI 的每一次操作都广播给正在看的 Web 面板。
//
// 与 SSH 的区别：Redis / SQL 没有 PTY，命令仍走各自 service 的正常执行路径，
// 会话只承担两件事：授权凭据 + 操作广播。
//
// 会话是持久的：面板关掉只是 Detach，会话与历史事件都留在服务端，重新打开面板即可接回并回放历史；
// 只有用户主动「结束会话」或闲置超过回收阈值才真正关闭。
package console

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ErrSessionNotFound 表示会话不存在或已关闭。
var ErrSessionNotFound = errorString("控制台会话不存在或已关闭")

// ErrInvalidName 表示会话名称不合法。
var ErrInvalidName = errorString("会话名称无效")

type errorString string

func (e errorString) Error() string { return string(e) }

// MaxNameLength 是会话名称的字符上限：标签页上放得下，列表里也不至于挤掉别的列。
const MaxNameLength = 40

// NormalizeName 规整用户给会话起的名字：去首尾空白；空串表示「清掉名字，恢复默认的会话 #N」；
// 超长或含控制字符（换行、制表符…会弄乱标签页与 CLI 的表格输出）时返回 ErrInvalidName。
// SSH 终端与 Redis / SQL 控制台共用同一规则。
func NormalizeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if utf8.RuneCountInString(name) > MaxNameLength {
		return "", fmt.Errorf("%w：不能超过 %d 个字符", ErrInvalidName, MaxNameLength)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w：不能包含换行、制表等控制字符", ErrInvalidName)
		}
	}
	return name, nil
}

// historyLimit 是每个会话保留的事件条数：新接入的面板先补这些，不至于空屏。
const historyLimit = 200

// subscriberBuffer 是订阅者通道的缓冲深度；写满即丢弃（显示类订阅者不做背压）。
const subscriberBuffer = 128

// Event 是控制台上的一条记录：谁、执行了什么、结果如何。
type Event struct {
	Type string `json:"type"`
	// Source 区分来源：cli（AI / 命令行）、web（人在面板里手输）或 approved（审批后异步执行）
	Source     string    `json:"source"`
	LogID      int64     `json:"logId,omitempty"`
	Command    string    `json:"command,omitempty"`
	Kind       string    `json:"kind,omitempty"`
	Status     string    `json:"status,omitempty"`
	Result     any       `json:"result,omitempty"`
	Text       string    `json:"text,omitempty"`
	Columns    []string  `json:"columns,omitempty"`
	Rows       [][]any   `json:"rows,omitempty"`
	Error      string    `json:"error,omitempty"`
	Note       string    `json:"note,omitempty"`
	DurationMs int64     `json:"durationMs,omitempty"`
	Username   string    `json:"username,omitempty"`
	At         time.Time `json:"at"`
}

// Session 是一条控制台会话。
type Session struct {
	ID string
	// Seq 是进程内单调递增的序号——给人看的短标识，界面上显示为「会话 #12」，多会话切换靠它区分。
	Seq          int64
	Kind         string // redis | sql
	ConnectionID int64
	UserID       int64
	CreatedAt    time.Time

	mu          sync.Mutex
	name        string
	sinks       map[int64]func(Event)
	nextSink    int64
	history     []Event
	attachCount int
	lastSeen    time.Time
	closed      bool
	done        chan struct{}
}

// Manager 维护进程内的控制台会话表，并按闲置时间回收。结构对齐 sshx.SessionManager。
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	idle     time.Duration
	kind     string
	seq      atomic.Int64
}

// NewManager 创建管理器；idle 是「无人接入且无操作」的回收阈值。
func NewManager(kind string, idle time.Duration) *Manager {
	if idle <= 0 {
		idle = time.Hour
	}
	return &Manager{sessions: map[string]*Session{}, idle: idle, kind: kind}
}

// Create 新建一条会话。
func (m *Manager) Create(connectionID, userID int64) *Session {
	now := time.Now()
	s := &Session{
		ID: uuid.NewString(), Seq: m.seq.Add(1), Kind: m.kind, ConnectionID: connectionID, UserID: userID,
		CreatedAt: now, sinks: map[int64]func(Event){}, lastSeen: now, done: make(chan struct{}),
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	return s
}

// Get 返回会话；不存在返回 ErrSessionNotFound。
func (m *Manager) Get(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return s, nil
}

// Close 关闭并移除会话。
func (m *Manager) Close(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if !ok {
		return ErrSessionNotFound
	}
	s.close()
	return nil
}

func (m *Manager) snapshot() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	return all
}

// FindByConnection 返回该用户在指定连接上打开的会话，按最近活动倒序。CLI 门禁据此判定。
func (m *Manager) FindByConnection(connectionID, userID int64) []*Session {
	var found []*Session
	for _, s := range m.snapshot() {
		if s.ConnectionID == connectionID && s.UserID == userID && !s.IsClosed() {
			found = append(found, s)
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].LastSeen().After(found[j].LastSeen()) })
	return found
}

// List 返回该用户打开的全部会话，按最近活动倒序。
func (m *Manager) List(userID int64) []*Session {
	var found []*Session
	for _, s := range m.snapshot() {
		if s.UserID == userID && !s.IsClosed() {
			found = append(found, s)
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].LastSeen().After(found[j].LastSeen()) })
	return found
}

// Broadcast 把事件投给该连接上所有已打开的会话（CLI 执行后由 service 调用）。
func (m *Manager) Broadcast(connectionID, userID int64, event Event) {
	for _, s := range m.FindByConnection(connectionID, userID) {
		s.Broadcast(event)
	}
}

// BroadcastConnection 把事件投给该连接上的**全部**会话，不区分用户。
// 用于审批后的异步执行：批准者（浏览器里的操作人）通常不是命令的发起人，
// 而结果应该出现在所有正看着这个连接的面板里。
func (m *Manager) BroadcastConnection(connectionID int64, event Event) {
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	for _, s := range m.snapshot() {
		if s.ConnectionID == connectionID && !s.IsClosed() {
			s.Broadcast(event)
		}
	}
}

// Run 周期回收闲置会话，直到 ctx 结束。只回收「无人接入且超过 idle 没有任何操作」的会话：
// 面板关掉但 CLI 还在用的会话会被操作广播续命，真正被遗忘的才会被收掉。
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			for _, s := range m.snapshot() {
				_ = m.Close(s.ID)
			}
			return
		case now := <-ticker.C:
			for _, s := range m.snapshot() {
				s.mu.Lock()
				stale := s.attachCount == 0 && now.Sub(s.lastSeen) > m.idle
				s.mu.Unlock()
				if stale {
					_ = m.Close(s.ID)
				}
			}
		}
	}
}

// Count 返回当前会话数。
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// ---- 会话 ----

// Broadcast 把事件分发给所有订阅者，并记入历史。
func (s *Session) Broadcast(event Event) {
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	s.mu.Lock()
	s.lastSeen = time.Now()
	s.history = append(s.history, event)
	if len(s.history) > historyLimit {
		s.history = append([]Event(nil), s.history[len(s.history)-historyLimit:]...)
	}
	sinks := make([]func(Event), 0, len(s.sinks))
	for _, sink := range s.sinks {
		sinks = append(sinks, sink)
	}
	s.mu.Unlock()
	for _, sink := range sinks {
		sink(event)
	}
}

// Subscriber 是 channel 形式的订阅者，供 WebSocket 桥接使用。
type Subscriber struct {
	ch    chan Event
	unsub func()
	once  sync.Once
}

// NewSubscriber 创建订阅者；replay 为 true 时先收到历史事件。
func (s *Session) NewSubscriber(replay bool) *Subscriber {
	sub := &Subscriber{ch: make(chan Event, subscriberBuffer)}
	s.mu.Lock()
	var backlog []Event
	if replay {
		backlog = append([]Event(nil), s.history...)
	}
	s.nextSink++
	id := s.nextSink
	s.sinks[id] = func(event Event) {
		select {
		case sub.ch <- event:
		default: // 订阅者跟不上：丢弃，不拖累其他订阅者
		}
	}
	s.mu.Unlock()
	for _, event := range backlog {
		select {
		case sub.ch <- event:
		default:
		}
	}
	sub.unsub = func() {
		s.mu.Lock()
		delete(s.sinks, id)
		s.mu.Unlock()
	}
	return sub
}

// C 返回事件通道。
func (sub *Subscriber) C() <-chan Event { return sub.ch }

// Close 退订（幂等）。
func (sub *Subscriber) Close() { sub.once.Do(sub.unsub) }

// Attach 登记一个接入的客户端。
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

// AttachCount 返回接入的客户端数。
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

// SetName 设置会话名字（调用方先用 NormalizeName 规整）。
func (s *Session) SetName(name string) {
	s.mu.Lock()
	s.name = name
	s.lastSeen = time.Now()
	s.mu.Unlock()
}

// LastSeen 返回最近活动时间。
func (s *Session) LastSeen() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSeen
}

// IsClosed 返回会话是否已关闭。
func (s *Session) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Done 在会话关闭时关闭。
func (s *Session) Done() <-chan struct{} { return s.done }

func (s *Session) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.sinks = map[int64]func(Event){}
	s.mu.Unlock()
	close(s.done)
}
