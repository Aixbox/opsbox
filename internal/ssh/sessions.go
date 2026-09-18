package ssh

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"opsbox/internal/platform/console"
	"opsbox/internal/platform/sshx"
)

// SessionInfo 是开会话的响应：pending 时只有 LogID；就绪时附带 sessionId 与一次性 wsTicket。
type SessionInfo struct {
	LogID     int64  `json:"logId"`
	Status    string `json:"status"`
	SessionID string `json:"sessionId,omitempty"`
	Ticket    string `json:"ticket,omitempty"`
}

// SessionSummary 是已打开会话的列表项，供 Web 端切换会话、CLI 决定能否接入。
type SessionSummary struct {
	SessionID string `json:"sessionId"`
	Seq       int64  `json:"seq"`
	// Name 是用户起的名字；空串时界面显示「会话 #Seq」
	Name           string    `json:"name"`
	ConnectionID   int64     `json:"connectionId"`
	ConnectionName string    `json:"connectionName"`
	LogID          int64     `json:"logId"`
	Attached       int       `json:"attached"`
	CreatedAt      time.Time `json:"createdAt"`
	LastSeenAt     time.Time `json:"lastSeenAt"`
}

// ListSessions 列出该用户已打开的会话；connectionID>0 时只列该连接的。
func (s *Service) ListSessions(ctx context.Context, userID, connectionID int64) ([]SessionSummary, error) {
	var sessions []*sshx.Session
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
			LogID:          session.LogID,
			Attached:       session.AttachCount(),
			CreatedAt:      session.CreatedAt.UTC(),
			LastSeenAt:     session.LastSeen().UTC(),
		})
	}
	return items, nil
}

// RenameSession 给会话起名 / 改名；空名字恢复默认的「会话 #N」。名字只在内存里，随会话一起消失。
func (s *Service) RenameSession(userID int64, sessionID, name string) (string, error) {
	session, err := s.ownedSession(userID, sessionID)
	if err != nil {
		return "", err
	}
	normalized, err := console.NormalizeName(name)
	if err != nil {
		return "", invalid(err.Error())
	}
	session.SetName(normalized)
	return normalized, nil
}

// AttachExisting 为已打开的会话发一张接入票据，不新建会话。
// CLI 的 shell 命令走这条路：开会话的权力收归 Web 端，CLI 只能接入。
func (s *Service) AttachExisting(userID, connectionID int64, sessionID string) (SessionInfo, error) {
	session, err := s.resolveSession(connectionID, userID, sessionID)
	if err != nil {
		return SessionInfo{}, err
	}
	ticket, err := s.tickets.Issue(session.ID, userID)
	if err != nil {
		return SessionInfo{}, err
	}
	return SessionInfo{LogID: session.LogID, Status: "running", SessionID: session.ID, Ticket: ticket}, nil
}

// OpenSession 开 PTY 会话。commandID=0 时新建：audit 模式直接开；approve 模式登记 pending 由人批准。
// commandID>0 时认领已批准的会话记录并真正建立连接。
func (s *Service) OpenSession(ctx context.Context, userID, connectionID, commandID int64, rows, cols int) (SessionInfo, error) {
	if rows < 0 || rows > 500 || cols < 0 || cols > 1000 {
		return SessionInfo{}, invalid("终端尺寸无效")
	}
	options := LogOptions{Rows: rows, Cols: cols}
	var logID int64
	if commandID == 0 {
		conn, err := s.GetConnection(ctx, connectionID)
		if err != nil {
			return SessionInfo{}, err
		}
		if !conn.Enabled {
			return SessionInfo{}, ErrDisabled
		}
		if conn.ExecPolicy == "approve" {
			id, err := s.insertLog(ctx, connectionID, userID, "session", "interactive shell", "pending", options, "", false)
			if err != nil {
				return SessionInfo{}, err
			}
			return SessionInfo{LogID: id, Status: "pending"}, nil
		}
		logID, err = s.insertLog(ctx, connectionID, userID, "session", "interactive shell", "running", options, "", false)
		if err != nil {
			return SessionInfo{}, err
		}
	} else {
		entry, err := s.GetLog(ctx, commandID)
		if err != nil {
			return SessionInfo{}, err
		}
		if entry.ConnectionID != connectionID || entry.Kind != "session" || (entry.UserID != nil && *entry.UserID != userID) {
			return SessionInfo{}, ErrForbidden
		}
		if entry.Status == "pending" {
			return SessionInfo{LogID: entry.ID, Status: "pending"}, nil
		}
		if entry.Status != "approved" {
			return SessionInfo{}, fmt.Errorf("%w：会话记录状态为 %s", ErrConflict, entry.Status)
		}
		result, err := s.db.ExecContext(ctx, `UPDATE ssh_exec_logs SET status='running' WHERE id=? AND status='approved'`, commandID)
		if err != nil {
			return SessionInfo{}, err
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			return SessionInfo{}, ErrConflict
		}
		logID = commandID
	}
	dialCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	_, client, err := s.dial(dialCtx, connectionID)
	if err != nil {
		s.finishLog(dialCtx, logID, ExecOutcome{Status: "failed", Error: err.Error()})
		return SessionInfo{}, err
	}
	sessionID := uuid.NewString()
	if _, err := s.sessions.Create(sessionID, client, connectionID, userID, logID, rows, cols); err != nil {
		_ = client.Close()
		s.finishLog(dialCtx, logID, ExecOutcome{Status: "failed", Error: err.Error()})
		return SessionInfo{}, err
	}
	ticket, err := s.tickets.Issue(sessionID, userID)
	if err != nil {
		s.sessions.Close(sessionID)
		return SessionInfo{}, err
	}
	return SessionInfo{LogID: logID, Status: "running", SessionID: sessionID, Ticket: ticket}, nil
}

// ownedSession 取会话并校验归属。
func (s *Service) ownedSession(userID int64, sessionID string) (*sshx.Session, error) {
	session, err := s.sessions.Get(sessionID)
	if err != nil {
		return nil, err
	}
	if session.UserID != userID {
		return nil, ErrForbidden
	}
	return session, nil
}

// IssueTicket 为已有会话再发一张接入票据（断线重连）。
func (s *Service) IssueTicket(userID int64, sessionID string) (string, error) {
	if _, err := s.ownedSession(userID, sessionID); err != nil {
		return "", err
	}
	return s.tickets.Issue(sessionID, userID)
}

// ResizeSession 调整终端尺寸（xterm addon-attach 不传 resize，走这条 REST）。
func (s *Service) ResizeSession(userID int64, sessionID string, rows, cols int) error {
	session, err := s.ownedSession(userID, sessionID)
	if err != nil {
		return err
	}
	return session.Resize(rows, cols)
}

// CloseSession 主动结束会话（用户在面板上点「结束会话」）。关掉面板本身不会走到这里：
// 会话是持久的，面板关闭只是断开接入，回来还能接上。
func (s *Service) CloseSession(userID int64, sessionID string) error {
	session, err := s.ownedSession(userID, sessionID)
	if err != nil {
		return err
	}
	session.Close()
	return nil
}

// AttachSession 用一次性票据换取会话（WebSocket 握手阶段调用）。
func (s *Service) AttachSession(ticketValue string) (*sshx.Session, error) {
	item, ok := s.tickets.Consume(ticketValue)
	if !ok {
		return nil, ErrForbidden
	}
	session, err := s.sessions.Get(item.SessionID)
	if err != nil {
		return nil, err
	}
	if !session.Attach() {
		return nil, sshx.ErrSessionAttached
	}
	return session, nil
}

// onSessionClosed 是 SessionManager 的回调：收尾会话审计日志。
func (s *Service) onSessionClosed(session *sshx.Session) {
	outcome := ExecOutcome{Status: "success", DurationMs: time.Since(session.CreatedAt).Milliseconds()}
	if err := session.Err(); err != nil {
		outcome.Status, outcome.Error = "failed", err.Error()
	}
	s.finishLog(context.Background(), session.LogID, outcome)
}
