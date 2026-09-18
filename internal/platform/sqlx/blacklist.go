package sqlx

import "fmt"

// 黑名单规则名（存 sql_settings.blocked_patterns，全写策略生效，命中即拒）。
const (
	RuleDropDatabase   = "drop_database"    // DROP DATABASE
	RuleDropSchema     = "drop_schema"      // DROP SCHEMA
	RuleFullTableWrite = "full_table_write" // 无 WHERE 的 UPDATE / DELETE
	RuleTruncate       = "truncate"         // TRUNCATE
	RuleDropTable      = "drop_table"       // DROP TABLE
)

// AllRules 是全部合法规则；设置校验据此拒绝未知规则名。
var AllRules = []string{RuleDropDatabase, RuleDropSchema, RuleFullTableWrite, RuleTruncate, RuleDropTable}

func ValidRule(rule string) bool {
	for _, name := range AllRules {
		if rule == name {
			return true
		}
	}
	return false
}

// ruleReasons 是命中黑名单时给用户 / AI 看的原因说明。
var ruleReasons = map[string]string{
	RuleDropDatabase:   "DROP DATABASE 会删除整个数据库（含全部表），必须由人手工操作",
	RuleDropSchema:     "DROP SCHEMA 会删除整个模式（含全部对象），必须由人手工操作",
	RuleFullTableWrite: "不带 WHERE 的 UPDATE / DELETE 会改写整张表（如确需全表操作请由人执行）",
	RuleTruncate:       "TRUNCATE 会清空整张表",
	RuleDropTable:      "DROP TABLE 会删除整张表",
}

// Blacklist 是按连接生效的结构化规则集合。
type Blacklist struct {
	rules map[string]bool
}

// NewBlacklist 由规则名构造；未知规则名被忽略（设置层已校验）。
func NewBlacklist(rules []string) Blacklist {
	set := make(map[string]bool, len(rules))
	for _, rule := range rules {
		if ValidRule(rule) {
			set[rule] = true
		}
	}
	return Blacklist{rules: set}
}

func (b Blacklist) Active(rule string) bool { return b.rules[rule] }

// Check 逐条扫描语句，返回首个命中的规则与原因。扫描基于 masked 语句（字符串与注释已被抹平），
// 因此字符串里出现 "DROP TABLE" 之类的字面量不会误伤。
func (b Blacklist) Check(statements []Statement) (rule string, hit bool) {
	for _, statement := range statements {
		for _, candidate := range b.match(statement) {
			if b.rules[candidate] {
				return candidate, true
			}
		}
	}
	return "", false
}

func (b Blacklist) match(statement Statement) []string {
	words := topLevelWords(statement.Masked)
	if len(words) == 0 {
		return nil
	}
	var hits []string
	// DROP 的目标词可能在任何深度出现（保守起见全文扫，不做深度过滤）
	everything := allWords(statement.Masked)
	for i := 0; i+1 < len(everything); i++ {
		if everything[i] != "DROP" {
			continue
		}
		switch everything[i+1] {
		case "DATABASE":
			hits = append(hits, RuleDropDatabase)
		case "SCHEMA":
			hits = append(hits, RuleDropSchema)
		case "TABLE":
			hits = append(hits, RuleDropTable)
		}
	}
	switch words[0] {
	case "UPDATE", "DELETE":
		if b.rules[RuleFullTableWrite] && !hasTopLevelWhere(words) {
			hits = append(hits, RuleFullTableWrite)
		}
	case "TRUNCATE":
		hits = append(hits, RuleTruncate)
	}
	return hits
}

// hasTopLevelWhere 判断 depth-0 词序列里是否有 WHERE（子查询里的 WHERE 不算外层的过滤条件）。
func hasTopLevelWhere(words []string) bool {
	for _, word := range words {
		if word == "WHERE" {
			return true
		}
	}
	return false
}

// Reason 返回规则的人类可读说明（审计与 CLI 提示用）。
func Reason(rule string) string {
	if text, ok := ruleReasons[rule]; ok {
		return text
	}
	return fmt.Sprintf("命中黑名单规则 %s", rule)
}
