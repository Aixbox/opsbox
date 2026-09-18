package console

import (
	"context"
	"sync"
	"time"

	"opsbox/internal/platform/security"
)

// TicketStore 发放一次性 WebSocket 接入票据：浏览器握手带不了 Authorization 头，用短时效票据代替。
// SSH 的 PTY 会话与 Redis / SQL 的控制台会话共用这一份实现。
type TicketStore struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]Ticket
}

// Ticket 是一张已发放的票据。
type Ticket struct {
	SessionID string
	UserID    int64
	expires   time.Time
}

// NewTicketStore 创建票据仓库。
func NewTicketStore(ttl time.Duration) *TicketStore {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &TicketStore{ttl: ttl, items: map[string]Ticket{}}
}

// Issue 为会话发一张票据。
func (t *TicketStore) Issue(sessionID string, userID int64) (string, error) {
	value, err := security.NewRandomToken()
	if err != nil {
		return "", err
	}
	t.mu.Lock()
	t.items[value] = Ticket{SessionID: sessionID, UserID: userID, expires: time.Now().Add(t.ttl)}
	t.mu.Unlock()
	return value, nil
}

// Consume 校验并销毁票据。
func (t *TicketStore) Consume(value string) (Ticket, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	item, ok := t.items[value]
	delete(t.items, value)
	if !ok || time.Now().After(item.expires) {
		return Ticket{}, false
	}
	return item, true
}

// Run 周期清理过期票据，直到 ctx 结束。
func (t *TicketStore) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			t.mu.Lock()
			for key, item := range t.items {
				if now.After(item.expires) {
					delete(t.items, key)
				}
			}
			t.mu.Unlock()
		}
	}
}
