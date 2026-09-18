package sqlx

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "run.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRunReadRowsAndLimit(t *testing.T) {
	h := testDB(t)
	ctx := context.Background()
	setup := Split(EngineMySQL, "CREATE TABLE t (id INTEGER); INSERT INTO t VALUES (1),(2),(3),(4),(5)")
	if _, err := Run(ctx, h, setup, Options{Tx: true}); err != nil {
		t.Fatal(err)
	}
	result, err := Run(ctx, h, Split(EngineMySQL, "SELECT id FROM t ORDER BY id"), Options{MaxRows: 3})
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsReturned != 3 || !result.Truncated {
		t.Fatalf("限幅失败: %+v", result)
	}
	if len(result.Columns) != 1 || result.Columns[0] != "id" {
		t.Fatalf("列名错误: %v", result.Columns)
	}
	if result.Rows[0][0] != int64(1) {
		t.Fatalf("首行应为 1，得到 %v (%T)", result.Rows[0][0], result.Rows[0][0])
	}
}

func TestRunWritesAndAffected(t *testing.T) {
	h := testDB(t)
	ctx := context.Background()
	setup := Split(EngineMySQL, "CREATE TABLE t (id INTEGER); INSERT INTO t VALUES (1),(2)")
	if _, err := Run(ctx, h, setup, Options{Tx: true}); err != nil {
		t.Fatal(err)
	}
	result, err := Run(ctx, h, Split(EngineMySQL, "UPDATE t SET id = id + 10 WHERE id > 0; DELETE FROM t WHERE id = 11"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsAffected != 3 {
		t.Fatalf("影响行数应为 3，得到 %d", result.RowsAffected)
	}
	if result.RowsReturned != 0 || result.Columns != nil {
		t.Fatalf("写语句不应有行: %+v", result)
	}
}

func TestRunTxRollback(t *testing.T) {
	h := testDB(t)
	ctx := context.Background()
	setup := Split(EngineMySQL, "CREATE TABLE t (id INTEGER UNIQUE); INSERT INTO t VALUES (1)")
	if _, err := Run(ctx, h, setup, Options{Tx: true}); err != nil {
		t.Fatal(err)
	}
	// 第二条违反唯一约束，第一条必须被回滚
	batch := Split(EngineMySQL, "INSERT INTO t VALUES (2); INSERT INTO t VALUES (1)")
	if _, err := Run(ctx, h, batch, Options{Tx: true}); err == nil {
		t.Fatal("事务应失败")
	}
	result, err := Run(ctx, h, Split(EngineMySQL, "SELECT COUNT(*) FROM t"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if count, _ := result.Rows[0][0].(int64); count != 1 {
		t.Fatalf("回滚失败，行数 = %v", result.Rows[0][0])
	}
}

func TestRunTimeout(t *testing.T) {
	h := testDB(t)
	ctx := context.Background()
	setup := Split(EngineMySQL, "CREATE TABLE t (id INTEGER); INSERT INTO t VALUES (1)")
	if _, err := Run(ctx, h, setup, Options{Tx: true}); err != nil {
		t.Fatal(err)
	}
	_, err := Run(ctx, h, Split(EngineMySQL, "SELECT id FROM t"), Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("正常语句不应超时: %v", err)
	}
	// 用一段必然耗时的递归 CTE 验证超时映射（生成海量行做聚合）
	heavy := Split(EngineMySQL, "WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM seq) SELECT SUM(x) FROM seq")
	_, err = Run(ctx, h, heavy, Options{Timeout: 300 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("应返回 ErrTimeout，得到 %v", err)
	}
}
