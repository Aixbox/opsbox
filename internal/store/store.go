// Package store 负责打开（必要时创建）SQLite 数据库并执行内嵌迁移。
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动，无 CGO，单二进制可交叉编译
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open 打开数据库、应用 Pragma 并跑完未执行的迁移。
func Open(path string) (*sql.DB, error) {
	dsn := path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite 写并发差，连接池收敛到 1 写 + 少量读
	db.SetMaxOpenConns(4)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		base := strings.TrimSuffix(filepath.Base(name), ".sql")
		version, err := strconv.ParseInt(base, 10, 64)
		if err != nil {
			// 允许 "00001_描述" 命名：取下划线前的纯数字前缀作为版本号
			if prefix, _, found := strings.Cut(base, "_"); found {
				version, err = strconv.ParseInt(prefix, 10, 64)
			}
		}
		if err != nil {
			return fmt.Errorf("迁移文件名必须以数字版本号开头: %s", name)
		}
		var applied int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, version).Scan(&applied); err != nil {
			return err
		}
		if applied > 0 {
			continue
		}
		if err := apply(db, version, name); err != nil {
			return fmt.Errorf("应用迁移 %s: %w", name, err)
		}
	}
	return nil
}

func apply(db *sql.DB, version int64, name string) error {
	raw, err := migrationsFS.ReadFile(name)
	if err != nil {
		return err
	}
	script := upSection(string(raw))
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(script); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES(?,?)`, version, time.Now().UTC().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// upSection 截取 "-- +goose Up" 到 "-- +goose Down" 之间的脚本（沿用原项目的 goose 文件格式）。
func upSection(raw string) string {
	script := raw
	if index := strings.Index(script, "-- +goose Down"); index >= 0 {
		script = script[:index]
	}
	if index := strings.Index(script, "-- +goose Up"); index >= 0 {
		script = script[index+len("-- +goose Up"):]
	}
	return script
}
