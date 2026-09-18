package redis

import (
	"context"
	"time"

	"opsbox/internal/platform/console"
)

// SessionInfo 是开控制台会话的响应。
type SessionInfo struct {
	SessionID string `json:"sessionId"`
	Ticket    string `json:"ticket"`
}

// SessionSummary 是已打开控制台会话的列表项。
type SessionSummary struct {
	SessionID string `json:"sessionId"`
	Seq       int64  `json:"seq"`
	// Name 是用户起的名字；空串时界面显示「会话 #Seq」
	Name           string    `json:"name"`
	ConnectionID   int64     `json:"connectionId"`
	ConnectionName string    `json:"connectionName"`
	Attached       int       `json:"attached"`
	CreatedAt      time.Time `json:"createdAt"`
	LastSeenAt     time.Time `json:"lastSeenAt"`
}

// OpenSession 打开一条控制台会话：会话即授权，此后 CLI / Web 才能在该连接上执行命令。
func (s *Service) OpenSession(ctx context.Context, userID, connectionID int64) (SessionInfo, error) {
	conn, err := s.GetConnection(ctx, connectionID)
	if err != nil {
		return SessionInfo{}, err
	}
	if !conn.Enabled {
		return SessionInfo{}, ErrDisabled
	}
	session := s.sessions.Create(connectionID, userID)
	ticket, err := s.tickets.Issue(session.ID, userID)
	if err != nil {
		_ = s.sessions.Close(session.ID)
		return SessionInfo{}, err
	}
	return SessionInfo{SessionID: session.ID, Ticket: ticket}, nil
}

// IssueTicket 为已有会话再发一张接入票据（断线重连）。
func (s *Service) IssueTicket(userID int64, sessionID string) (string, error) {
	session, err := s.sessions.Get(sessionID)
	if err != nil {
		return "", err
	}
	if session.UserID != userID {
		return "", ErrForbidden
	}
	return s.tickets.Issue(sessionID, userID)
}

// CloseSession 主动结束会话（用户在面板上点「结束会话」）。关掉面板本身不会走到这里：
// 会话是持久的，面板关闭只是断开接入，回来还能接上并回放历史。
func (s *Service) CloseSession(userID int64, sessionID string) error {
	session, err := s.sessions.Get(sessionID)
	if err != nil {
		return err
	}
	if session.UserID != userID {
		return ErrForbidden
	}
	return s.sessions.Close(sessionID)
}

// RenameSession 给会话起名 / 改名；空名字恢复默认的「会话 #N」。名字只在内存里，随会话一起消失。
func (s *Service) RenameSession(userID int64, sessionID, name string) (string, error) {
	session, err := s.sessions.Get(sessionID)
	if err != nil {
		return "", err
	}
	if session.UserID != userID {
		return "", ErrForbidden
	}
	normalized, err := console.NormalizeName(name)
	if err != nil {
		return "", invalid(err.Error())
	}
	session.SetName(normalized)
	return normalized, nil
}

// AttachSession 用一次性票据换取会话（WebSocket 握手阶段调用）。
func (s *Service) AttachSession(ticketValue string) (*console.Session, error) {
	item, ok := s.tickets.Consume(ticketValue)
	if !ok {
		return nil, ErrForbidden
	}
	session, err := s.sessions.Get(item.SessionID)
	if err != nil {
		return nil, err
	}
	session.Attach()
	return session, nil
}

// ListSessions 列出该用户打开的控制台会话；connectionID>0 时只列该连接的。
func (s *Service) ListSessions(ctx context.Context, userID, connectionID int64) ([]SessionSummary, error) {
	var sessions []*console.Session
	if connectionID > 0 {
		sessions = s.sessions.FindByConnection(connectionID, userID)
	} else {
		sessions = s.sessions.List(userID)
	}
	items := make([]SessionSummary, 0, len(sessions))
	names := map[int64]string{}
	for _, session := range sessions {
		name, ok := names[session.ConnectionID]
		if !ok {
			if conn, err := s.GetConnection(ctx, session.ConnectionID); err == nil {
				name = conn.Name
			}
			names[session.ConnectionID] = name
		}
		items = append(items, SessionSummary{
			SessionID:      session.ID,
			Seq:            session.Seq,
			Name:           session.Name(),
			ConnectionID:   session.ConnectionID,
			ConnectionName: name,
			Attached:       session.AttachCount(),
			CreatedAt:      session.CreatedAt.UTC(),
			LastSeenAt:     session.LastSeen().UTC(),
		})
	}
	return items, nil
}

// requireOpenSession 是门禁：该用户在该连接上必须有已打开的控制台会话。
func (s *Service) requireOpenSession(connectionID, userID int64) error {
	if len(s.sessions.FindByConnection(connectionID, userID)) == 0 {
		return ErrNoOpenSession
	}
	return nil
}

// broadcast 把一条控制台事件广播给该连接上所有会话，让正在看的 Web 面板即时看到 CLI 的操作。
func (s *Service) broadcast(connectionID, userID int64, event console.Event) {
	s.sessions.Broadcast(connectionID, userID, event)
}
