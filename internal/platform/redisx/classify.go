// Package redisx 是 Redis/Valkey 平台托管连接的底层设施：客户端池、命令分类器、回复限幅与安全 SCAN。
// 上层 modules/redis 负责策略、审批与审计；本包只做与业务无关的连接与分类。
package redisx

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Class 是命令分类结论。
type Class int

const (
	ClassRead   Class = iota // 只读，confirm / readonly 策略下都可直接执行
	ClassWrite               // 写命令，readonly 拒、confirm 入待批队列、allow 放行
	ClassDenied              // 无状态 exec 模型不支持：阻塞 / 订阅 / 事务 / 连接级命令
)

// Verdict 是对一条命令的分类结论。
type Verdict struct {
	Class   Class
	Reason  string // 结论说明（未知命令按写、阻塞命令等），供审计与审批面板展示
	Unknown bool   // 静态表和运行时缓存都不认识该命令，按写处理
}

// deniedCommands 无状态 exec 模型不支持的命令：每次执行都是从池里取的独立连接，
// 订阅态 / 事务态 / 连接级状态会污染连接池或依赖会话，v1 一律拒绝并给出原因。
var deniedCommands = map[string]string{
	"SUBSCRIBE":    "订阅会话需要长连接（无状态 exec 不支持），可改用轮询读",
	"PSUBSCRIBE":   "订阅会话需要长连接（无状态 exec 不支持），可改用轮询读",
	"SSUBSCRIBE":   "订阅会话需要长连接（无状态 exec 不支持），可改用轮询读",
	"UNSUBSCRIBE":  "订阅状态属于单条连接（无状态 exec 不支持）",
	"PUNSUBSCRIBE": "订阅状态属于单条连接（无状态 exec 不支持）",
	"SUNSUBSCRIBE": "订阅状态属于单条连接（无状态 exec 不支持）",
	"MONITOR":      "实时命令流需要长连接（无状态 exec 不支持）",
	"BLPOP":        "阻塞命令会占死池中连接，请用非阻塞 LPOP 轮询",
	"BRPOP":        "阻塞命令会占死池中连接，请用非阻塞 RPOP 轮询",
	"BRPOPLPUSH":   "阻塞命令会占死池中连接，请用 RPOPLPUSH",
	"BLMOVE":       "阻塞命令会占死池中连接，请用 LMOVE",
	"BLMPOP":       "阻塞命令会占死池中连接，请用 LMPOP",
	"BZPOPMIN":     "阻塞命令会占死池中连接，请用 ZPOPMIN 轮询",
	"BZPOPMAX":     "阻塞命令会占死池中连接，请用 ZPOPMAX 轮询",
	"BZMPOP":       "阻塞命令会占死池中连接，请用 ZMPOP",
	"WAIT":         "阻塞等待副本确认（无状态 exec 不支持）",
	"WAITAOF":      "阻塞等待 AOF 落盘（无状态 exec 不支持）",
	"MULTI":        "事务状态属于单条连接（无状态 exec 不支持），需要原子性请用 EVAL Lua 脚本",
	"EXEC":         "事务状态属于单条连接（无状态 exec 不支持），需要原子性请用 EVAL Lua 脚本",
	"DISCARD":      "事务状态属于单条连接（无状态 exec 不支持）",
	"WATCH":        "WATCH/MULTI 事务状态属于单条连接（无状态 exec 不支持）",
	"UNWATCH":      "WATCH/MULTI 事务状态属于单条连接（无状态 exec 不支持）",
	"SELECT":       "db 由连接配置固定，切换会污染连接池；请为不同 db 建不同连接",
	"RESET":        "会重置连接的 auth/db/订阅状态，污染连接池",
	"CLIENT":       "连接级 / 管理命令（SETNAME、KILL 等），无状态 exec 不支持",
	"AUTH":         "连接由平台配置认证，exec 中切换凭证没有意义",
	"HELLO":        "协议切换属于单条连接状态（无状态 exec 不支持）",
	"QUIT":         "会从连接池里抽走并关闭连接",
	"READONLY":     "集群副本读状态属于单条连接（v1 不支持集群）",
	"READWRITE":    "集群副本读状态属于单条连接（v1 不支持集群）",
	"ASKING":       "集群重定向辅助命令（v1 不支持集群）",
}

// subcommandRead 子命令级分类：父命令整体是写/管理类，但这些子命令只读。
// 不在表内的子命令一律按写处理（保守）。
var subcommandRead = map[string]map[string]bool{
	"CONFIG":  {"GET": true},
	"MEMORY":  {"USAGE": true, "DOCTOR": true, "STATS": true, "KEYS": true, "MALLOC-STATS": true},
	"ACL":     {"CAT": true, "LIST": true, "WHOAMI": true, "GETUSER": true, "LOG": true, "DRYRUN": true},
	"SCRIPT":  {"EXISTS": true},
	"COMMAND": {"INFO": true, "DOCS": true, "COUNT": true, "LIST": true, "GETKEYS": true},
	"SLOWLOG": {"GET": true, "LEN": true, "HELP": true},
	"LATENCY": {"HISTORY": true, "LATEST": true, "GRAPH": true, "DOCTOR": true},
	"OBJECT":  {"ENCODING": true, "FREQ": true, "IDLETIME": true, "REFCOUNT": true, "HELP": true},
	"XINFO":   {"STREAM": true, "GROUPS": true, "CONSUMERS": true},
}

// luaWriteReason EVAL / FUNCTION 系的统一说明（Lua 副作用不可静态判断，按设计稿保守处理）。
const luaWriteReason = "Lua / 函数副作用无法静态判断，按写处理；需要原子批量变更时用 EVAL 并先向用户展示脚本内容"

// luaCommands EVAL / FCALL / FUNCTION 全家桶，无论参数一律按写。
var luaCommands = map[string]bool{
	"EVAL": true, "EVALSHA": true, "EVAL_RO": true, "EVALSHA_RO": true,
	"FCALL": true, "FCALL_RO": true,
	"FUNCTION": true,
}

// readCommands 静态只读表（运行时元数据不可用时的兜底，覆盖常用命令）。
var readCommands = map[string]bool{
	"PING": true, "ECHO": true, "TIME": true, "INFO": true, "DBSIZE": true, "LASTSAVE": true, "LOLWUT": true,
	"RANDOMKEY": true, "KEYS": true, "SCAN": true, "TYPE": true, "EXISTS": true, "TOUCH": true,
	"STRLEN": true, "TTL": true, "PTTL": true, "EXPIRETIME": true, "PEXPIRETIME": true, "LCS": true, "SORT": true,
	"GET": true, "MGET": true, "GETRANGE": true, "SUBSTR": true,
	"LLEN": true, "LINDEX": true, "LRANGE": true, "LPOS": true,
	"SCARD": true, "SISMEMBER": true, "SMISMEMBER": true, "SMEMBERS": true, "SINTER": true, "SUNION": true, "SDIFF": true,
	"SRANDMEMBER": true, "SSCAN": true, "SINTERCARD": true,
	"ZCARD": true, "ZCOUNT": true, "ZLEXCOUNT": true, "ZRANGE": true, "ZRANGEBYLEX": true, "ZRANGEBYSCORE": true,
	"ZRANK": true, "ZREVRANK": true, "ZREVRANGE": true, "ZREVRANGEBYLEX": true, "ZREVRANGEBYSCORE": true,
	"ZSCORE": true, "ZMSCORE": true, "ZRANDMEMBER": true, "ZSCAN": true, "ZUNION": true, "ZINTER": true,
	"ZDIFF": true, "ZINTERCARD": true,
	"HGET": true, "HMGET": true, "HGETALL": true, "HKEYS": true, "HVALS": true, "HLEN": true,
	"HSTRLEN": true, "HEXISTS": true, "HRANDFIELD": true, "HSCAN": true,
	"PFCOUNT":  true,
	"BITCOUNT": true, "BITPOS": true, "BITFIELD_RO": true, "GETBIT": true,
	"GEOPOS": true, "GEODIST": true, "GEOHASH": true, "GEOSEARCH": true,
	"GEORADIUS": true, "GEORADIUSBYMEMBER": true,
	"GEORADIUS_RO": true, "GEORADIUSBYMEMBER_RO": true,
	"PUBSUB": true, "XLEN": true, "XRANGE": true, "XREVRANGE": true, "XREAD": true, "XPENDING": true,
	"DUMP": true, "COMMAND": true,
}

// writeCommands 静态写命令表（兜底用；未知命令一律按写处理，所以此表只需覆盖常见值）。
var writeCommands = map[string]bool{
	"SET": true, "SETNX": true, "SETEX": true, "PSETEX": true, "SETRANGE": true, "SETBIT": true,
	"GETSET": true, "GETDEL": true, "GETEX": true, "APPEND": true,
	"INCR": true, "INCRBY": true, "INCRBYFLOAT": true, "DECR": true, "DECRBY": true,
	"MSET": true, "MSETNX": true,
	"DEL": true, "UNLINK": true, "COPY": true, "MOVE": true, "RENAME": true, "RENAMENX": true,
	"EXPIRE": true, "PEXPIRE": true, "EXPIREAT": true, "PEXPIREAT": true, "PERSIST": true,
	"HSET": true, "HSETNX": true, "HDEL": true, "HINCRBY": true, "HINCRBYFLOAT": true,
	"HGETDEL": true, "HGETEX": true, "HSETEX": true,
	"LPUSH": true, "LPUSHX": true, "RPUSH": true, "RPUSHX": true, "LPOP": true, "RPOP": true,
	"LREM": true, "LINSERT": true, "LSET": true, "LTRIM": true, "LMOVE": true, "RPOPLPUSH": true, "LMPOP": true,
	"SADD": true, "SREM": true, "SPOP": true, "SMOVE": true,
	"SINTERSTORE": true, "SUNIONSTORE": true, "SDIFFSTORE": true,
	"ZADD": true, "ZINCRBY": true, "ZREM": true, "ZPOPMIN": true, "ZPOPMAX": true, "ZMPOP": true,
	"ZREMRANGEBYRANK": true, "ZREMRANGEBYSCORE": true, "ZREMRANGEBYLEX": true,
	"ZUNIONSTORE": true, "ZINTERSTORE": true, "ZDIFFSTORE": true, "ZRANGESTORE": true,
	"PFADD": true, "PFMERGE": true,
	"BITOP": true, "BITFIELD": true,
	"GEOADD": true, "GEOSEARCHSTORE": true,
	"PUBLISH": true, "SPUBLISH": true, "XREADGROUP": true,
	"RESTORE": true, "RESTORE-ASKING": true, "MIGRATE": true,
	"XADD": true, "XDEL": true, "XTRIM": true, "XSETID": true, "XGROUP": true,
	"XACK": true, "XCLAIM": true, "XAUTOCLAIM": true,
	"FLUSHALL": true, "FLUSHDB": true, "SHUTDOWN": true, "DEBUG": true, "MODULE": true,
	"REPLICAOF": true, "SLAVEOF": true, "SWAPDB": true, "FAILOVER": true,
	"BGSAVE": true, "SAVE": true, "BGREWRITEAOF": true,
}

// Classify 静态分类（无运行时元数据时使用）。name 大小写不敏感，args 不含命令名。
func Classify(name string, args []string) Verdict {
	return classify(strings.ToUpper(name), args, nil)
}

func classify(upper string, args []string, runtimeFlags []string) Verdict {
	// 1. 拒绝表：无状态模型不支持，任何元数据都不能推翻
	if reason, ok := deniedCommands[upper]; ok {
		return Verdict{Class: ClassDenied, Reason: reason}
	}
	// 2. 参数级判定（带 STORE / BLOCK 的读命令会变成写或阻塞）
	switch upper {
	case "XREAD", "XREADGROUP":
		for _, arg := range args {
			if strings.EqualFold(arg, "BLOCK") {
				return Verdict{Class: ClassDenied, Reason: "XREAD/XREADGROUP 带 BLOCK 会阻塞连接，请去掉 BLOCK 后轮询"}
			}
		}
	case "SORT":
		for _, arg := range args {
			if strings.EqualFold(arg, "STORE") {
				return Verdict{Class: ClassWrite, Reason: "SORT 带 STORE 会写结果集"}
			}
		}
	case "GEORADIUS", "GEORADIUSBYMEMBER":
		for _, arg := range args {
			if strings.EqualFold(arg, "STORE") || strings.EqualFold(arg, "STOREDIST") {
				return Verdict{Class: ClassWrite, Reason: "GEO 查询带 STORE 会写结果集"}
			}
		}
	}
	// 3. 子命令级判定：父命令在运行时没有 readonly flag，需要在这里放行只读子命令
	if subs, ok := subcommandRead[upper]; ok {
		if len(args) == 0 {
			if upper == "COMMAND" {
				return Verdict{Class: ClassRead} // COMMAND 裸命令是只读元数据
			}
			return Verdict{Class: ClassWrite, Reason: "管理命令按写处理"}
		}
		sub := strings.ToUpper(args[0])
		if subs[sub] {
			return Verdict{Class: ClassRead}
		}
		if upper == "OBJECT" {
			return Verdict{Class: ClassRead} // OBJECT 全部子命令只读
		}
		return Verdict{Class: ClassWrite, Reason: fmt.Sprintf("%s %s 按写处理", upper, sub)}
	}
	if luaCommands[upper] {
		return Verdict{Class: ClassWrite, Reason: luaWriteReason}
	}
	// 4. 运行时 COMMAND INFO 的 readonly flag 是最权威的「确认是读」依据
	for _, flag := range runtimeFlags {
		if strings.EqualFold(flag, "readonly") {
			return Verdict{Class: ClassRead}
		}
	}
	// 5. 静态读表兜底：PING/INFO/TIME 这类服务端命令官方元数据里没有 readonly flag，
	//    只靠运行时会误判成写，所以静态读表要在运行时写结论之前
	if readCommands[upper] {
		return Verdict{Class: ClassRead}
	}
	if writeCommands[upper] {
		return Verdict{Class: ClassWrite}
	}
	// 6. 实例认识但静态表没收录：按运行时结论走写；实例也不认识（查无此命令）：未知按写
	if len(runtimeFlags) > 0 {
		return Verdict{Class: ClassWrite, Reason: "运行时元数据标记为写 / 管理命令"}
	}
	return Verdict{Class: ClassWrite, Unknown: true, Reason: "静态分类表未收录该命令，按写处理"}
}

// Classifier 持有某台实例的运行时命令 flags 缓存（COMMAND INFO 结果）。
// 零值不可用，用 NewClassifier 创建；并发安全。
type Classifier struct {
	mu    sync.RWMutex
	flags map[string][]string
}

// NewClassifier 创建空分类器（只有静态表可用）。
func NewClassifier() *Classifier {
	return &Classifier{flags: map[string][]string{}}
}

// Load 批量装载 COMMAND INFO 结果（name → flags），预热时用；重复装载是幂等的。
func (c *Classifier) Load(flags map[string][]string) {
	if len(flags) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for name, item := range flags {
		c.flags[strings.ToUpper(name)] = item
	}
}

// RuntimeFlags 返回运行时 flags；第二位表示缓存里是否有这台实例的记录。
func (c *Classifier) RuntimeFlags(name string) ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	flags, ok := c.flags[strings.ToUpper(name)]
	return flags, ok
}

// Classify 对命令做最终分类；name 大小写不敏感。运行时元数据优先，静态表兜底。
func (c *Classifier) Classify(name string, args []string) Verdict {
	upper := strings.ToUpper(name)
	flags, _ := c.RuntimeFlags(upper)
	return classify(upper, args, flags)
}

// FormatCommand 把 args 渲染成展示用命令文本：含空格 / 控制字符的参数加引号，整体截断到 maxLen。
func FormatCommand(args []string, maxLen int) string {
	var b strings.Builder
	for i, arg := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(quoteArg(arg))
	}
	text := b.String()
	if maxLen > 0 && len(text) > maxLen {
		text = text[:maxLen] + "…"
	}
	return text
}

func quoteArg(arg string) string {
	if arg == "" {
		return `""`
	}
	if strings.ContainsAny(arg, " \t\n\r\"\\") {
		return strconv.Quote(arg)
	}
	return arg
}

// MatchBlocked 匹配平台黑名单：条目形如 "FLUSHALL" 或 "CONFIG SET"，
// 大小写不敏感，条目 token 依序作为 args 前缀匹配。返回命中的条目原文。
func MatchBlocked(args []string, blocked []string) string {
	for _, entry := range blocked {
		fields := strings.Fields(strings.ToUpper(strings.TrimSpace(entry)))
		if len(fields) == 0 || len(fields) > len(args) {
			continue
		}
		matched := true
		for i, field := range fields {
			if strings.ToUpper(args[i]) != field {
				matched = false
				break
			}
		}
		if matched {
			return entry
		}
	}
	return ""
}
