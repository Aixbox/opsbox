package sqlx

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

// 每个托管连接的池上限：AI 的查询是低频短连接场景，不需要大池。
const (
	maxOpenConns    = 4
	maxIdleConns    = 2
	connMaxIdleTime = 5 * time.Minute
	connMaxLifetime = time.Hour
	idleCloseAfter  = 15 * time.Minute
	idleSweepEvery  = 10 * time.Minute
)

type poolEntry struct {
	db       *sql.DB
	driver   string
	dsn      string
	lastUsed time.Time
}

// Pool 按连接 id 懒建 *sql.DB；目标配置（DSN）变化时自动重建，空闲池由 Run 协程回收。
type Pool struct {
	mu      sync.Mutex
	entries map[int64]*poolEntry
	now     func() time.Time
}

func NewPool() *Pool {
	return &Pool{entries: map[int64]*poolEntry{}, now: time.Now}
}

// DB 返回该连接可用的 *sql.DB。DSN 与缓存不一致（连接配置被修改）时关闭旧池重建。
func (p *Pool) DB(ctx context.Context, id int64, target Target) (*sql.DB, error) {
	driver, dsn, err := target.DSN()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	entry, ok := p.entries[id]
	if ok && (entry.driver != driver || entry.dsn != dsn) {
		_ = entry.db.Close()
		ok = false
	}
	if !ok {
		db, err := sql.Open(driver, dsn)
		if err != nil {
			p.mu.Unlock()
			return nil, err
		}
		db.SetMaxOpenConns(maxOpenConns)
		db.SetMaxIdleConns(maxIdleConns)
		db.SetConnMaxIdleTime(connMaxIdleTime)
		db.SetConnMaxLifetime(connMaxLifetime)
		entry = &poolEntry{db: db, driver: driver, dsn: dsn}
		p.entries[id] = entry
	}
	entry.lastUsed = p.now()
	db := entry.db
	p.mu.Unlock()
	// Ping 验证凭证 / 网络 / 目标实例存活，避免把失败延迟到具体语句
	pingCtx, cancel := context.WithTimeout(ctx, DialTimeout+5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return nil, err
	}
	return db, nil
}

// Invalidate 在连接被删除时关闭并移除其池。
func (p *Pool) Invalidate(id int64) {
	p.mu.Lock()
	entry, ok := p.entries[id]
	delete(p.entries, id)
	p.mu.Unlock()
	if ok {
		_ = entry.db.Close()
	}
}

// Run 周期回收空闲池，直到 ctx 结束。
func (p *Pool) Run(ctx context.Context) {
	ticker := time.NewTicker(idleSweepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			p.CloseAll()
			return
		case <-ticker.C:
			p.closeIdle()
		}
	}
}

func (p *Pool) closeIdle() {
	cutoff := p.now().Add(-idleCloseAfter)
	p.mu.Lock()
	var stale []*sql.DB
	for id, entry := range p.entries {
		if entry.lastUsed.Before(cutoff) {
			stale = append(stale, entry.db)
			delete(p.entries, id)
		}
	}
	p.mu.Unlock()
	for _, db := range stale {
		_ = db.Close()
	}
}

func (p *Pool) CloseAll() {
	p.mu.Lock()
	entries := p.entries
	p.entries = map[int64]*poolEntry{}
	p.mu.Unlock()
	for _, entry := range entries {
		_ = entry.db.Close()
	}
}

// ErrTimeout 表示查询超过时限被终止。
var ErrTimeout = errors.New("查询超时")

// Options 是一次执行的限制参数。
type Options struct {
	Timeout time.Duration
	MaxRows int
	// Tx 为 true 时把全部语句包进一个事务，任一失败整体回滚。
	Tx bool
}

// Result 是执行结果：读语句返回 Columns/Rows（最多 MaxRows 行），写语句累计 RowsAffected。
// 多语句时 Columns/Rows 只保留最后一段产生行的结果（写批处理不会读数据）。
type Result struct {
	Columns      []string `json:"columns,omitempty"`
	Rows         [][]any  `json:"rows,omitempty"`
	RowsReturned int      `json:"rowsReturned"`
	RowsAffected int64    `json:"rowsAffected"`
	Truncated    bool     `json:"truncated"`
}

// Run 在目标库上顺序执行切分后的语句。ctx 只控制调用方语义，超时由 Options.Timeout 负责
// （调用方应传 context.WithoutCancel 脱离 HTTP 2s 死线）。
func Run(ctx context.Context, db *sql.DB, statements []Statement, opts Options) (Result, error) {
	var result Result
	if len(statements) == 0 {
		return result, errors.New("没有可执行的语句")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.MaxRows <= 0 {
		opts.MaxRows = 500
	}
	execCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	if opts.Tx {
		tx, err := db.BeginTx(execCtx, nil)
		if err != nil {
			return result, wrapTimeout(execCtx, err)
		}
		for _, statement := range statements {
			if err := execute(execCtx, tx, statement, &result, opts.MaxRows); err != nil {
				_ = tx.Rollback()
				return result, err
			}
		}
		if err := tx.Commit(); err != nil {
			return result, wrapTimeout(execCtx, err)
		}
		return result, nil
	}
	conn, err := db.Conn(execCtx)
	if err != nil {
		return result, wrapTimeout(execCtx, err)
	}
	defer conn.Close()
	// 同一连接顺序执行：SET / USE 等会话语句对后续语句保持生效
	for _, statement := range statements {
		if err := execute(execCtx, conn, statement, &result, opts.MaxRows); err != nil {
			return result, err
		}
	}
	return result, nil
}

// queryer 是 *sql.Conn 与 *sql.Tx 共同满足的执行接口。
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// execute 执行单条语句：读语句走 Query 收集行，写语句走 Exec 统计影响行数。
func execute(ctx context.Context, q queryer, statement Statement, result *Result, maxRows int) error {
	if ClassifyStatement(statement) == KindRead {
		return runRows(ctx, q, statement.Raw, result, maxRows)
	}
	sqlResult, err := q.ExecContext(ctx, statement.Raw)
	if err != nil {
		return wrapTimeout(ctx, err)
	}
	if affected, err := sqlResult.RowsAffected(); err == nil {
		result.RowsAffected += affected
	}
	return nil
}

// runRows 执行查询并按 maxRows 截断；新查询的结果覆盖上一段（多语句批处理只回传最后一段行）。
func runRows(ctx context.Context, q queryer, raw string, result *Result, maxRows int) error {
	rows, err := q.QueryContext(ctx, raw)
	if err != nil {
		return wrapTimeout(ctx, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return wrapTimeout(ctx, err)
	}
	holders := make([]any, len(columns))
	values := make([]any, len(columns))
	for i := range values {
		holders[i] = &values[i]
	}
	var collected [][]any
	for rows.Next() {
		if len(collected) >= maxRows {
			result.Truncated = true
			break
		}
		if err := rows.Scan(holders...); err != nil {
			return wrapTimeout(ctx, err)
		}
		row := make([]any, len(values))
		for i, value := range values {
			switch typed := value.(type) {
			case []byte:
				row[i] = string(typed)
			default:
				row[i] = value
			}
		}
		collected = append(collected, row)
	}
	if err := rows.Err(); err != nil {
		return wrapTimeout(ctx, err)
	}
	result.Columns, result.Rows, result.RowsReturned = columns, collected, len(collected)
	return nil
}

func wrapTimeout(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ErrTimeout
	}
	return err
}
