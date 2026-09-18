package redisx

import (
	"strings"
	"testing"
)

func verdictOf(t *testing.T, classifier *Classifier, name string, args ...string) Verdict {
	t.Helper()
	if classifier == nil {
		return Classify(name, args)
	}
	return classifier.Classify(name, args)
}

func TestClassifyReadCommands(t *testing.T) {
	readCases := [][]string{
		{"GET", "key"},
		{"GET"},
		{"MGET", "a", "b"},
		{"HGETALL", "user:1"},
		{"HGET", "user:1", "name"},
		{"LRANGE", "queue", "0", "-1"},
		{"SMEMBERS", "tags"},
		{"ZSCORE", "board", "alice"},
		{"ZREVRANGE", "board", "0", "9", "WITHSCORES"},
		{"TYPE", "key"},
		{"TTL", "key"},
		{"EXISTS", "key"},
		{"STRLEN", "key"},
		{"SCAN", "0", "MATCH", "user:*"},
		{"KEYS", "user:*"},
		{"DBSIZE"},
		{"INFO"},
		{"INFO", "server"},
		{"PING"},
		{"TIME"},
		{"CONFIG", "GET", "maxmemory"},
		{"MEMORY", "USAGE", "big:key"},
		{"OBJECT", "ENCODING", "key"},
		{"ACL", "WHOAMI"},
		{"SLOWLOG", "GET"},
		{"LATENCY", "HISTORY", "event"},
		{"COMMAND", "INFO", "GET"},
		{"COMMAND"},
		{"SCRIPT", "EXISTS", "abc"},
		{"XRANGE", "stream", "-", "+"},
		{"XPENDING", "stream", "group"},
		{"PUBSUB", "CHANNELS"},
		{"BITCOUNT", "bits"},
		{"GEOSEARCH", "cities", "FROMMEMBER", "beijing", "BYRADIUS", "100", "km"},
		{"GEORADIUS", "cities", "1", "2", "100", "km"},
		{"SORT", "list", "ALPHA"},
		{"XREAD", "COUNT", "10", "STREAMS", "s", "0"},
	}
	for _, args := range readCases {
		verdict := verdictOf(t, nil, args[0], args[1:]...)
		if verdict.Class != ClassRead {
			t.Errorf("%v 应为读，got class=%d reason=%q", args, verdict.Class, verdict.Reason)
		}
	}
}

func TestClassifyWriteCommands(t *testing.T) {
	writeCases := [][]string{
		{"SET", "key", "value"},
		{"SET", "key", "value", "EX", "60"},
		{"DEL", "key"},
		{"UNLINK", "key"},
		{"INCR", "counter"},
		{"HSET", "user:1", "name", "alice"},
		{"LPUSH", "queue", "job"},
		{"SADD", "tags", "a"},
		{"ZADD", "board", "10", "alice"},
		{"EXPIRE", "key", "60"},
		{"RENAME", "a", "b"},
		{"APPEND", "key", "tail"},
		{"GETDEL", "key"},
		{"GETEX", "key", "EX", "60"},
		{"SORT", "list", "STORE", "out"},
		{"GEORADIUS", "cities", "1", "2", "100", "km", "STORE", "out"},
		{"XADD", "stream", "*", "f", "v"},
		{"XGROUP", "CREATE", "stream", "g", "0"},
		{"XREADGROUP", "GROUP", "g", "c", "STREAMS", "s", ">"},
		{"PUBLISH", "chan", "msg"},
		{"EVAL", "return 1", "0"},
		{"EVALSHA", "sha", "0"},
		{"FCALL", "fn", "0"},
		{"FUNCTION", "LOAD", "..."},
		{"FLUSHALL"},
		{"FLUSHDB"},
		{"SWAPDB", "0", "1"},
		{"CONFIG", "SET", "maxmemory", "1gb"},
		{"CONFIG", "REWRITE"},
		{"MEMORY", "PURGE"},
		{"ACL", "SETUSER", "alice", "on"},
		{"SLOWLOG", "RESET"},
		{"LATENCY", "RESET"},
		{"SCRIPT", "LOAD", "return 1"},
		{"DEBUG", "SLEEP", "1"},
		{"MODULE", "LIST"},
		{"BGSAVE"},
		{"MIGRATE", "host", "6379", "key", "0", "1000"},
		{"UNKNOWNCOMMAND", "x"},
	}
	for _, args := range writeCases {
		verdict := verdictOf(t, nil, args[0], args[1:]...)
		if verdict.Class != ClassWrite {
			t.Errorf("%v 应为写（保守），got class=%d reason=%q", args, verdict.Class, verdict.Reason)
		}
	}
}

func TestClassifyDeniedCommands(t *testing.T) {
	deniedCases := [][]string{
		{"SUBSCRIBE", "chan"},
		{"PSUBSCRIBE", "chan*"},
		{"MONITOR"},
		{"BLPOP", "queue", "0"},
		{"BRPOP", "queue", "5"},
		{"BRPOPLPUSH", "a", "b", "0"},
		{"BLMOVE", "a", "b", "LEFT", "RIGHT", "0"},
		{"BZPOPMIN", "board", "0"},
		{"WATCH", "key"},
		{"MULTI"},
		{"EXEC"},
		{"SELECT", "3"},
		{"RESET"},
		{"CLIENT", "SETNAME", "x"},
		{"CLIENT", "KILL"},
		{"AUTH", "password"},
		{"HELLO", "3"},
		{"QUIT"},
		{"WAIT", "1", "1000"},
		{"XREAD", "BLOCK", "0", "STREAMS", "s", "0"},
		{"XREADGROUP", "GROUP", "g", "c", "BLOCK", "0", "STREAMS", "s", ">"},
	}
	for _, args := range deniedCases {
		verdict := verdictOf(t, nil, args[0], args[1:]...)
		if verdict.Class != ClassDenied {
			t.Errorf("%v 应直接拒绝，got class=%d reason=%q", args, verdict.Class, verdict.Reason)
		}
	}
}

func TestClassifyRuntimeFlagsPrecedeStaticWrite(t *testing.T) {
	// 运行时 readonly flag：静态表没收录的模块命令也能确认是读
	classifier := NewClassifier()
	classifier.Load(map[string][]string{
		"JSON.GET":  {"readonly", "fast"},
		"JSON.SET":  {"write", "denyoom"},
		"FT.SEARCH": {"readonly"},
	})
	if verdict := classifier.Classify("json.get", []string{"key"}); verdict.Class != ClassRead {
		t.Errorf("运行时 readonly 应判读: %+v", verdict)
	}
	if verdict := classifier.Classify("JSON.SET", []string{"key", "$", "1"}); verdict.Class != ClassWrite {
		t.Errorf("运行时无 readonly 应判写: %+v", verdict)
	}
	if verdict := classifier.Classify("FT.SEARCH", []string{"idx", "*"}); verdict.Class != ClassRead {
		t.Errorf("FT.SEARCH 应判读: %+v", verdict)
	}
	// 拒绝表不受运行时元数据影响
	classifier.Load(map[string][]string{"SELECT": {"fast"}})
	if verdict := classifier.Classify("SELECT", []string{"2"}); verdict.Class != ClassDenied {
		t.Errorf("SELECT 必须保持拒绝: %+v", verdict)
	}
	// 参数级判定也不受运行时影响（SORT 在官方元数据里带 readonly）
	classifier.Load(map[string][]string{"SORT": {"readonly", "denyoom"}})
	if verdict := classifier.Classify("SORT", []string{"l", "STORE", "out"}); verdict.Class != ClassWrite {
		t.Errorf("SORT STORE 应为写: %+v", verdict)
	}
}

func TestMatchBlocked(t *testing.T) {
	blocked := []string{"FLUSHALL", "CONFIG SET", "debug"}
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"flushall"}, "FLUSHALL"},
		{[]string{"FLUSHALL", "ASYNC"}, "FLUSHALL"},
		{[]string{"config", "set", "maxmemory", "0"}, "CONFIG SET"},
		{[]string{"DEBUG", "SLEEP", "1"}, "debug"},
		{[]string{"config", "get", "maxmemory"}, ""},
		{[]string{"CONFIG", "SETMAXMEMORY"}, ""},
		{[]string{"GET", "key"}, ""},
	}
	for _, item := range cases {
		if got := MatchBlocked(item.args, blocked); got != item.want {
			t.Errorf("MatchBlocked(%v) = %q, want %q", item.args, got, item.want)
		}
	}
}

func TestFormatCommand(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"HGETALL", "user:1"}, "HGETALL user:1"},
		{[]string{"SET", "k", "hello world"}, `SET k "hello world"`},
		{[]string{"GET", ""}, `GET ""`},
	}
	for _, item := range cases {
		if got := FormatCommand(item.args, 0); got != item.want {
			t.Errorf("FormatCommand(%v) = %q, want %q", item.args, got, item.want)
		}
	}
	long := FormatCommand([]string{"GET", strings.Repeat("x", 100)}, 20)
	if !strings.HasSuffix(long, "…") || len(long) > 20+len("…") {
		t.Errorf("超长命令应截断: %q (len=%d)", long, len(long))
	}
}
