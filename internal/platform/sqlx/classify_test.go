package sqlx

import (
	"strings"
	"testing"
)

func statementsOf(engine Engine, input string) []Statement {
	return Split(engine, input)
}

func TestSplitBasic(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{"单条", "SELECT 1", []string{"SELECT 1"}},
		{"多条", "SELECT 1; SELECT 2 ;", []string{"SELECT 1", "SELECT 2"}},
		{"字符串里的分号", `SELECT 'a;b'; SELECT 2`, []string{`SELECT 'a;b'`, "SELECT 2"}},
		{"字符串里的引号", `SELECT 'it''s;ok', "x""y"; DELETE FROM t`, []string{`SELECT 'it''s;ok', "x""y"`, "DELETE FROM t"}},
		{"反引号标识符", "SELECT `a;b` FROM t; SELECT 2", []string{"SELECT `a;b` FROM t", "SELECT 2"}},
		{"行注释里的分号", "SELECT 1 -- 注释; 不切分\n; SELECT 2", []string{"SELECT 1 -- 注释; 不切分", "SELECT 2"}},
		{"块注释里的分号", "SELECT /* a;b;c */ 1; SELECT 2", []string{"SELECT /* a;b;c */ 1", "SELECT 2"}},
		{"MySQL井号注释", "SELECT 1 # 注释; 不切分\n; SELECT 2", []string{"SELECT 1 # 注释; 不切分", "SELECT 2"}},
		{"空语句丢弃", ";; SELECT 1;;", []string{"SELECT 1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := statementsOf(EngineMySQL, tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("切分出 %d 条 %q，想要 %d 条 %q", len(got), raws(got), len(tc.want), tc.want)
			}
			for i := range got {
				if got[i].Raw != tc.want[i] {
					t.Fatalf("第 %d 条 = %q，想要 %q", i, got[i].Raw, tc.want[i])
				}
			}
		})
	}
}

func TestSplitPostgres(t *testing.T) {
	// 标准字符串里的分号：反斜杠不转义，'a\' 在 PG 里就是完整字符串 a\
	got := Split(EnginePostgres, `SELECT 'a;b' , 1; SELECT 3`)
	if len(got) != 2 || got[0].Raw != `SELECT 'a;b' , 1` {
		t.Fatalf("PG 标准字符串切分错误: %q", raws(got))
	}
	// E'' 字符串里 \' 转义，分号在字符串内
	got = Split(EnginePostgres, `SELECT E'a\'; b' , 1; SELECT 3`)
	if len(got) != 2 || got[0].Raw != `SELECT E'a\'; b' , 1` {
		t.Fatalf("PG E 字符串切分错误: %q", raws(got))
	}
	// 美元引用里的分号
	got = Split(EnginePostgres, `SELECT $$a;b$$, $tag$ c;d $tag$; SELECT 3`)
	if len(got) != 2 || got[0].Raw != `SELECT $$a;b$$, $tag$ c;d $tag$` {
		t.Fatalf("PG 美元引用切分错误: %q", raws(got))
	}
	// $1 参数占位符不是美元引用
	got = Split(EnginePostgres, `SELECT * FROM t WHERE id = $1; SELECT 2`)
	if len(got) != 2 {
		t.Fatalf("$1 被误判为美元引用: %q", raws(got))
	}
	// PG 的 # 不是注释
	got = Split(EnginePostgres, `SELECT a # b; SELECT 2`)
	if len(got) != 2 {
		t.Fatalf("PG # 被误判为注释: %q", raws(got))
	}
}

func TestClassifyStatement(t *testing.T) {
	read := []string{
		"SELECT * FROM users",
		"select id from users where id = 1",
		"SHOW TABLES",
		"DESCRIBE users",
		"DESC users",
		"EXPLAIN SELECT * FROM users",
		"USE app",
		"SET @x = 1",
		"VALUES (1, 2)",
		"WITH top AS (SELECT 1) SELECT * FROM top",
		"WITH t AS (SELECT 1) SELECT * FROM t",
		/* 注释里的 INSERT */ "SELECT /* INSERT */ 1",
		"SELECT 'DELETE FROM users' AS note",
	}
	for _, sqlText := range read {
		if got := ClassifyStatement(Split(EngineMySQL, sqlText)[0]); got != KindRead {
			t.Errorf("%q 应为读，得到 %s", sqlText, got)
		}
	}
	write := []string{
		"INSERT INTO t VALUES (1)",
		"UPDATE t SET a = 1 WHERE id = 1",
		"DELETE FROM t WHERE id = 1",
		"REPLACE INTO t VALUES (1)",
		"MERGE INTO t USING s ON 1=1 WHEN MATCHED THEN UPDATE SET a = 1",
		"CREATE TABLE t (id INT)",
		"ALTER TABLE t ADD COLUMN a INT",
		"DROP TABLE t",
		"TRUNCATE TABLE t",
		"RENAME TABLE a TO b",
		"GRANT SELECT ON t TO u",
		"REVOKE SELECT ON t FROM u",
		"COMMENT ON TABLE t IS 'x'",
		"LOCK TABLES t WRITE",
		"CALL do_something()",
		"VACUUM ANALYZE t",
		"REINDEX TABLE t",
		"EXECUTE stmt",
		"LISTEN channel",
		"KILL 123",
		"REFRESH MATERIALIZED VIEW mv",
		"do_something_weird()",
		"LOAD DATA INFILE 'x' INTO TABLE t",
		"WITH removed AS (DELETE FROM t RETURNING id) SELECT * FROM removed",
		// EXPLAIN ANALYZE 会真实执行目标语句
		"EXPLAIN ANALYZE DELETE FROM t",
		"EXPLAIN (ANALYZE) UPDATE t SET a = 1",
		"explain analyze select 1",
		"SET GLOBAL max_connections = 100",
	}
	for _, sqlText := range write {
		if got := ClassifyStatement(Split(EngineMySQL, sqlText)[0]); got != KindWrite {
			t.Errorf("%q 应为写，得到 %s", sqlText, got)
		}
	}
}

func TestClassifyMultiStatement(t *testing.T) {
	statements := Split(EngineMySQL, "SELECT 1; INSERT INTO t VALUES (1); SELECT 2")
	if got := Classify(statements); got != KindWrite {
		t.Fatalf("混合语句应为写，得到 %s", got)
	}
	statements = Split(EngineMySQL, "SELECT 1; SHOW TABLES")
	if got := Classify(statements); got != KindRead {
		t.Fatalf("纯读批应为读，得到 %s", got)
	}
}

func TestBlacklist(t *testing.T) {
	defaults := NewBlacklist([]string{RuleDropDatabase, RuleDropSchema, RuleFullTableWrite})
	cases := []struct {
		name  string
		rules []string
		sql   string
		want  string
	}{
		{"DROP DATABASE", defaults.rulesList(), "DROP DATABASE app", RuleDropDatabase},
		{"DROP SCHEMA", defaults.rulesList(), "drop schema if exists s", RuleDropSchema},
		{"无 WHERE 的 UPDATE", defaults.rulesList(), "UPDATE users SET banned = 1", RuleFullTableWrite},
		{"无 WHERE 的 DELETE", defaults.rulesList(), "DELETE FROM logs", RuleFullTableWrite},
		{"有 WHERE 放行", defaults.rulesList(), "DELETE FROM logs WHERE created_at < '2020-01-01'", ""},
		{"字符串里出现 DROP TABLE 不误伤", defaults.rulesList(), "SELECT 'DROP TABLE users' AS note", ""},
		{"子查询 WHERE 不算外层", defaults.rulesList(), "UPDATE users SET n = (SELECT MAX(x) FROM logs WHERE a = 1)", RuleFullTableWrite},
		{"默认不拦 TRUNCATE", defaults.rulesList(), "TRUNCATE TABLE logs", ""},
		{"启用 TRUNCATE 规则", []string{RuleTruncate}, "TRUNCATE logs", RuleTruncate},
		{"启用 DROP TABLE 规则", []string{RuleDropTable}, "DROP TABLE users", RuleDropTable},
		{"多语句任一命中即拦", defaults.rulesList(), "SELECT 1; DROP DATABASE app", RuleDropDatabase},
		{"DROP 后跟注释", defaults.rulesList(), "DROP /* x */ DATABASE app", RuleDropDatabase},
		{"无关语句", defaults.rulesList(), "SELECT 1", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blacklist := NewBlacklist(tc.rules)
			rule, hit := blacklist.Check(Split(EngineMySQL, tc.sql))
			if hit != (tc.want != "") || (hit && rule != tc.want) {
				t.Fatalf("Check(%q) = (%q, %v)，想要 %q", tc.sql, rule, hit, tc.want)
			}
		})
	}
}

func TestDSN(t *testing.T) {
	mysqlTarget := Target{Engine: EngineMySQL, Host: "127.0.0.1", Port: 3306, Username: "root", Password: "p@ss:word", Database: "app"}
	driver, dsn, err := mysqlTarget.DSN()
	if err != nil || driver != "mysql" {
		t.Fatalf("DSN() = %q, %q, %v", driver, dsn, err)
	}
	if !strings.Contains(dsn, "charset=utf8mb4") {
		t.Fatalf("MySQL DSN 缺少默认 charset: %q", dsn)
	}
	// go-sql-driver 的解析按最后一个 @ 切分 host，密码含 @ : 等字符原文传递即可
	if !strings.Contains(dsn, "root:p@ss:word@tcp(127.0.0.1:3306)/app") {
		t.Fatalf("MySQL DSN 密码处理错误: %q", dsn)
	}
	if strings.Contains(dsn, "multiStatements=true") {
		t.Fatalf("不应开启 multiStatements（服务端逐条执行）: %q", dsn)
	}
	pgTarget := Target{Engine: EnginePostgres, Host: "db.local", Port: 5432, Username: "app", Password: "p ss", Database: "app", Params: map[string]string{"sslmode": "require"}}
	driver, dsn, err = pgTarget.DSN()
	if err != nil || driver != "pgx" {
		t.Fatalf("DSN() = %q, %q, %v", driver, dsn, err)
	}
	if !strings.Contains(dsn, "sslmode=require") {
		t.Fatalf("PG DSN 缺少 sslmode: %q", dsn)
	}
	if _, _, err := (Target{Engine: EngineMySQL, Params: map[string]string{"multiStatements": "true"}}).DSN(); err == nil {
		t.Fatal("MySQL 白名单外参数应被拒绝")
	}
	if _, _, err := (Target{Engine: EnginePostgres, Params: map[string]string{"hack": "1"}}).DSN(); err == nil {
		t.Fatal("PG 白名单外参数应被拒绝")
	}
}

func TestValidIdentifier(t *testing.T) {
	for _, name := range []string{"users", "_t1", "order$items", strings.Repeat("a", 64)} {
		if !ValidIdentifier(name) {
			t.Errorf("%q 应为合法标识符", name)
		}
	}
	for _, name := range []string{"1abc", "a-b", "a b", "a;b", "", "a`b", `a"b`, strings.Repeat("a", 65)} {
		if ValidIdentifier(name) {
			t.Errorf("%q 应为非法标识符", name)
		}
	}
}

func raws(statements []Statement) []string {
	out := make([]string, len(statements))
	for i, statement := range statements {
		out[i] = statement.Raw
	}
	return out
}

// rulesList 把 map 转回规则名切片（测试辅助）。
func (b Blacklist) rulesList() []string {
	var rules []string
	for _, rule := range AllRules {
		if b.Active(rule) {
			rules = append(rules, rule)
		}
	}
	return rules
}
