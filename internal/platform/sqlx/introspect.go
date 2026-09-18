package sqlx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// TableSchema 是自省接口返回的表结构。
type TableSchema struct {
	Name    string   `json:"name"`
	Comment string   `json:"comment,omitempty"`
	Columns []Column `json:"columns"`
	Indexes []Index  `json:"indexes"`
	// DDL 只有 MySQL 返回（SHOW CREATE TABLE）；PostgreSQL 无等价接口，靠 columns/indexes 拼装。
	DDL string `json:"ddl,omitempty"`
}

type Column struct {
	Name     string `json:"name"`
	DataType string `json:"dataType"`
	Nullable bool   `json:"nullable"`
	Default  any    `json:"default"`
	Key      string `json:"key,omitempty"`
	Extra    string `json:"extra,omitempty"`
}

type Index struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns,omitempty"`
	Unique  bool     `json:"unique"`
	// Definition 是引擎原生的索引定义（PG 的 indexdef；MySQL 由名称+列拼装）。
	Definition string `json:"definition,omitempty"`
}

// identifierPattern 限制自省的表名只能是普通标识符（防注入：标识符无法参数化绑定）。
var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]{0,63}$`)

func ValidIdentifier(name string) bool { return identifierPattern.MatchString(name) }

// ListTables 返回当前库 / 当前模式下的表与视图名（AI 写查询前先摸清结构）。
func ListTables(ctx context.Context, db *sql.DB, engine Engine) ([]string, error) {
	var query string
	switch engine {
	case EngineMySQL:
		query = `SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_type IN ('BASE TABLE','VIEW') ORDER BY table_name`
	case EnginePostgres:
		query = `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_type IN ('BASE TABLE','VIEW') ORDER BY table_name`
	default:
		return nil, errors.New("不支持的数据库引擎")
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// DescribeTable 返回表结构（列 / 索引 / 注释；MySQL 另带 DDL）。
func DescribeTable(ctx context.Context, db *sql.DB, engine Engine, table string) (TableSchema, error) {
	if !ValidIdentifier(table) {
		return TableSchema{}, errors.New("表名必须是普通标识符（字母 / 数字 / 下划线 / $）")
	}
	schema := TableSchema{Name: table, Columns: []Column{}, Indexes: []Index{}}
	var err error
	switch engine {
	case EngineMySQL:
		err = describeMySQL(ctx, db, table, &schema)
	case EnginePostgres:
		err = describePostgres(ctx, db, table, &schema)
	default:
		err = errors.New("不支持的数据库引擎")
	}
	return schema, err
}

func describeMySQL(ctx context.Context, db *sql.DB, table string, schema *TableSchema) error {
	quoted := "`" + strings.ReplaceAll(table, "`", "``") + "`"
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(table_comment,'') FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?`,
		table).Scan(&schema.Comment); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT column_name, column_type, is_nullable, column_default, column_key, extra FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position`,
		table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var column Column
		var nullable, key string
		var columnType string
		if err := rows.Scan(&column.Name, &columnType, &nullable, &column.Default, &key, &column.Extra); err != nil {
			return err
		}
		column.DataType, column.Nullable = columnType, nullable == "YES"
		schema.Columns = append(schema.Columns, column)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	indexRows, err := db.QueryContext(ctx,
		`SELECT index_name, non_unique, column_name FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? ORDER BY index_name, seq_in_index`,
		table)
	if err != nil {
		return err
	}
	defer indexRows.Close()
	byName := map[string]*Index{}
	var order []string
	for indexRows.Next() {
		var name string
		var nonUnique int
		var column string
		if err := indexRows.Scan(&name, &nonUnique, &column); err != nil {
			return err
		}
		index, ok := byName[name]
		if !ok {
			index = &Index{Name: name, Unique: nonUnique == 0, Columns: []string{}}
			byName[name] = index
			order = append(order, name)
		}
		index.Columns = append(index.Columns, column)
	}
	if err := indexRows.Err(); err != nil {
		return err
	}
	for _, name := range order {
		schema.Indexes = append(schema.Indexes, *byName[name])
	}
	// SHOW CREATE TABLE 返回两列（表名 + DDL）
	var name, ddl string
	if err := db.QueryRowContext(ctx, `SHOW CREATE TABLE `+quoted).Scan(&name, &ddl); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("表 %s 不存在", table)
		}
		return err
	}
	schema.DDL = ddl
	return nil
}

func describePostgres(ctx context.Context, db *sql.DB, table string, schema *TableSchema) error {
	rows, err := db.QueryContext(ctx,
		`SELECT column_name, data_type, is_nullable, column_default FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 ORDER BY ordinal_position`,
		table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var column Column
		var nullable string
		if err := rows.Scan(&column.Name, &column.DataType, &nullable, &column.Default); err != nil {
			return err
		}
		column.Nullable = nullable == "YES"
		schema.Columns = append(schema.Columns, column)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(schema.Columns) == 0 {
		return fmt.Errorf("表 %s 不存在（当前模式）", table)
	}
	indexRows, err := db.QueryContext(ctx,
		`SELECT indexname, indexdef FROM pg_indexes WHERE schemaname = current_schema() AND tablename = $1 ORDER BY indexname`,
		table)
	if err != nil {
		return err
	}
	defer indexRows.Close()
	for indexRows.Next() {
		var index Index
		if err := indexRows.Scan(&index.Name, &index.Definition); err != nil {
			return err
		}
		index.Unique = strings.Contains(strings.ToUpper(index.Definition), "CREATE UNIQUE INDEX")
		schema.Indexes = append(schema.Indexes, index)
	}
	return indexRows.Err()
}
