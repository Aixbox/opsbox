package sql

import (
	"context"

	"opsbox/internal/platform/sqlx"
)

// ListTables 返回当前库 / 模式下的表与视图名。自省是安全网的一部分：
// AI 写查询前先摸清结构，显著减少写错表 / 写错列的事故；只读，任何写策略下都放行。
//
// 但仍要求连接上有已打开的控制台会话：会话即授权这条口径对所有连接操作一致，
// 否则「没开控制台也能摸清库结构」就成了门禁的缺口。
func (s *Service) ListTables(ctx context.Context, userID, id int64) ([]string, error) {
	conn, target, err := s.resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.requireOpenSession(id, userID); err != nil {
		return nil, err
	}
	db, err := s.pools.DB(ctx, id, target)
	if err != nil {
		return nil, err
	}
	return sqlx.ListTables(ctx, db, sqlx.Engine(conn.Engine))
}

// DescribeTable 返回表结构（列 / 索引 / 注释，MySQL 另带 DDL）。
func (s *Service) DescribeTable(ctx context.Context, userID, id int64, table string) (sqlx.TableSchema, error) {
	conn, target, err := s.resolve(ctx, id)
	if err != nil {
		return sqlx.TableSchema{}, err
	}
	if err := s.requireOpenSession(id, userID); err != nil {
		return sqlx.TableSchema{}, err
	}
	db, err := s.pools.DB(ctx, id, target)
	if err != nil {
		return sqlx.TableSchema{}, err
	}
	return sqlx.DescribeTable(ctx, db, sqlx.Engine(conn.Engine), table)
}
