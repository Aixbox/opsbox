package sqlx

import (
	"strings"
)

// Kind 是语句的读写类别；分类只用于写策略路由，启发式的边界情况由 confirm/approve 模式兜底。
type Kind string

const (
	KindRead  Kind = "read"
	KindWrite Kind = "write"
	// KindSchema 是自省接口的审计类别，不参与分类器。
	KindSchema Kind = "schema"
)

// Statement 是切分后的一条语句：Raw 原文（供执行与审计），Masked 把字符串字面量与注释
// 内部替换成了空格（供关键字分类与黑名单扫描，长度与 Raw 一致）。
type Statement struct {
	Raw    string
	Masked string
}

// readKeywords 是首关键字为这些词时判定为读操作的集合（设计稿 §3.2）。
var readKeywords = map[string]bool{
	"SELECT": true, "SHOW": true, "DESCRIBE": true, "DESC": true,
	"EXPLAIN": true, "USE": true, "SET": true, "VALUES": true,
}

// writeKeywords 首关键字命中即写；不在两个集合里的按写处理（保守）。
var writeKeywords = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "REPLACE": true, "MERGE": true,
	"CREATE": true, "ALTER": true, "DROP": true, "TRUNCATE": true, "RENAME": true,
	"GRANT": true, "REVOKE": true, "COMMENT": true, "LOCK": true, "CALL": true,
	"VACUUM": true, "REINDEX": true, "CLUSTER": true, "LOAD": true, "IMPORT": true,
	"PREPARE": true, "EXECUTE": true, "DEALLOCATE": true, "REFRESH": true, "LISTEN": true,
	"NOTIFY": true, "DO": true, "HANDLER": true, "RESET": true, "KILL": true, "SAVEPOINT": true,
}

// Split 把可能含多条语句的 SQL 按分号切分（跳过字符串、注释、引号标识符与 PG 美元引用里的分号）。
// 末尾多余分号产生空语句会被丢弃。启发式切分：分类结果只用于策略路由，执行与审批才是兜底。
func Split(engine Engine, input string) []Statement {
	type writer struct{ raw, masked strings.Builder }
	var statements []Statement
	current := &writer{}
	flush := func() {
		raw := strings.TrimSpace(current.raw.String())
		if raw != "" {
			statements = append(statements, Statement{Raw: raw, Masked: strings.TrimSpace(current.masked.String())})
		}
		current = &writer{}
	}
	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == ';':
			flush()
		case r == '\'' || r == '"' || (r == '`' && engine == EngineMySQL):
			i = consumeQuoted(runes, i, r, engine, &current.raw, &current.masked)
		case r == '-' && i+1 < len(runes) && runes[i+1] == '-':
			i = consumeLineComment(runes, i, &current.raw, &current.masked)
		case r == '#' && engine == EngineMySQL:
			i = consumeLineComment(runes, i, &current.raw, &current.masked)
		case r == '/' && i+1 < len(runes) && runes[i+1] == '*':
			i = consumeBlockComment(runes, i, engine, &current.raw, &current.masked)
		case r == '$' && engine == EnginePostgres:
			if tag, ok := dollarTag(runes, i); ok {
				i = consumeDollar(runes, i, tag, &current.raw, &current.masked)
			} else {
				current.raw.WriteRune(r)
				current.masked.WriteRune(' ')
			}
		default:
			current.raw.WriteRune(r)
			current.masked.WriteRune(r)
		}
	}
	flush()
	return statements
}

// Classify 把多条语句归为最严类别：任何一条写 → write。
func Classify(statements []Statement) Kind {
	for _, statement := range statements {
		if ClassifyStatement(statement) == KindWrite {
			return KindWrite
		}
	}
	return KindRead
}

// ClassifyStatement 按首个关键字分类；未知关键字按写处理。
// EXPLAIN / EXPLAIN ANALYZE 特判：ANALYZE 会真实执行目标语句（MySQL 8 与 PG 都是），必须按写对待。
func ClassifyStatement(statement Statement) Kind {
	allWords := allWords(statement.Masked)
	if len(allWords) == 0 {
		return KindWrite // 空语句交给执行层报错
	}
	first := allWords[0]
	switch {
	case first == "EXPLAIN":
		for _, word := range allWords[1:] {
			if word == "ANALYZE" {
				return KindWrite
			}
		}
		return KindRead
	case first == "WITH":
		// CTE 的主体在括号内，PG 的数据修改型 CTE（WITH del AS (DELETE…) SELECT…）会真实写库：
		// 全文扫描，出现任何写动词即按写处理
		for _, word := range allWords[1:] {
			if writeKeywords[word] {
				return KindWrite
			}
		}
		return KindRead
	case first == "SET":
		// SET GLOBAL / SET PERSIST 改的是服务器全局状态（改配置），按写处理
		if len(allWords) > 1 && (allWords[1] == "GLOBAL" || allWords[1] == "PERSIST" || allWords[1] == "PERSIST_ONLY") {
			return KindWrite
		}
		return KindRead
	case readKeywords[first]:
		return KindRead
	case writeKeywords[first]:
		return KindWrite
	default:
		return KindWrite
	}
}

// topLevelWords 扫描 masked 文本，返回括号深度为 0 的大写词（子查询 / 函数参数里的词不算）。
func topLevelWords(masked string) []string { return scanWords(masked, true) }

// allWords 返回 masked 文本里全部大写词，不限括号深度。
func allWords(masked string) []string { return scanWords(masked, false) }

func scanWords(masked string, topOnly bool) []string {
	var words []string
	depth := 0
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		if depth == 0 || !topOnly {
			words = append(words, strings.ToUpper(masked[start:end]))
		}
		start = -1
	}
	for i := 0; i < len(masked); i++ {
		r := masked[i]
		switch {
		case isWordChar(r):
			if start < 0 {
				start = i
			}
		default:
			flush(i)
			switch r {
			case '(':
				depth++
			case ')':
				if depth > 0 {
					depth--
				}
			}
		}
	}
	flush(len(masked))
	return words
}

func isWordChar(r byte) bool { return isWordRune(rune(r)) }

func isWordRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '$'
}

// consumeQuoted 消费一段引号字面量，返回结束位置。MySQL 风格里反斜杠转义、双写引号转义；
// PostgreSQL 标准字符串只有双写转义，E'…' 字符串里反斜杠是转义。
// 字面量内部在 masked 里替换为空格。
func consumeQuoted(runes []rune, start int, quote rune, engine Engine, raw, masked *strings.Builder) int {
	escaped := engine == EngineMySQL
	if engine == EnginePostgres && quote == '\'' && start > 0 && (runes[start-1] == 'E' || runes[start-1] == 'e') {
		// E 前缀：仅当它本身是标识符一部分时才算转义字符串（id'E' 不是；id 后面的引号前无 E 语义）
		escaped = start == 1 || !isWordRune(runes[start-2])
	}
	raw.WriteRune(quote)
	masked.WriteRune(' ')
	i := start + 1
	for ; i < len(runes); i++ {
		r := runes[i]
		switch {
		case escaped && r == '\\':
			// 转义：跳过下一个字符
			raw.WriteRune(r)
			masked.WriteRune(' ')
			if i+1 < len(runes) {
				raw.WriteRune(runes[i+1])
				masked.WriteRune(' ')
				i++
			}
		case r == quote:
			if i+1 < len(runes) && runes[i+1] == quote {
				raw.WriteRune(r)
				raw.WriteRune(quote)
				masked.WriteRune(' ')
				masked.WriteRune(' ')
				i++
				continue
			}
			raw.WriteRune(quote)
			masked.WriteRune(' ')
			return i
		default:
			raw.WriteRune(r)
			masked.WriteRune(' ')
		}
	}
	return i - 1
}

func consumeLineComment(runes []rune, start int, raw, masked *strings.Builder) int {
	for i := start; i < len(runes); i++ {
		if runes[i] == '\n' {
			raw.WriteRune(runes[i])
			masked.WriteRune(runes[i])
			return i
		}
		raw.WriteRune(runes[i])
		masked.WriteRune(' ')
	}
	return len(runes) - 1
}

func consumeBlockComment(runes []rune, start int, engine Engine, raw, masked *strings.Builder) int {
	depth := 1
	i := start + 2
	raw.WriteString("/*")
	masked.WriteString("  ")
	for ; i < len(runes); i++ {
		if engine == EnginePostgres && i+1 < len(runes) && runes[i] == '/' && runes[i+1] == '*' {
			depth++
			raw.WriteString("/*")
			masked.WriteString("  ")
			i++
			continue
		}
		if i+1 < len(runes) && runes[i] == '*' && runes[i+1] == '/' {
			depth--
			raw.WriteString("*/")
			masked.WriteString("  ")
			i++
			if depth == 0 {
				return i
			}
			continue
		}
		raw.WriteRune(runes[i])
		if runes[i] == '\n' {
			masked.WriteRune('\n')
		} else {
			masked.WriteRune(' ')
		}
	}
	return len(runes) - 1
}

// dollarTag 判断位置 i 的 $…$ 是否为合法的美元引用开头，返回标签（可为空串）。
func dollarTag(runes []rune, start int) (string, bool) {
	i := start + 1
	var tag strings.Builder
	for ; i < len(runes); i++ {
		r := runes[i]
		if r == '$' {
			return tag.String(), true
		}
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' || len(tag.String()) > 0 && r >= '0' && r <= '9') {
			return "", false
		}
		tag.WriteRune(r)
	}
	return "", false
}

func consumeDollar(runes []rune, start int, tag string, raw, masked *strings.Builder) int {
	open := "$" + tag + "$"
	raw.WriteString(open)
	masked.WriteString(strings.Repeat(" ", len(open)))
	for i := start + len(open); i < len(runes); i++ {
		if runes[i] == '$' && i+len(open)-1 < len(runes) && string(runes[i:i+len(open)]) == open {
			raw.WriteString(open)
			masked.WriteString(strings.Repeat(" ", len(open)))
			return i + len(open) - 1
		}
		raw.WriteRune(runes[i])
		if runes[i] == '\n' {
			masked.WriteRune('\n')
		} else {
			masked.WriteRune(' ')
		}
	}
	return len(runes) - 1
}
