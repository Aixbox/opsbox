package redisx

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatReplySimpleValues(t *testing.T) {
	cases := []struct {
		raw  any
		text string
	}{
		{nil, "(nil)"},
		{int64(42), "42"},
		{"hello", "hello"},
		{"PONG", "PONG"},
	}
	for _, item := range cases {
		reply := FormatReply(item.raw, 0, 0)
		if reply.Text != item.text {
			t.Errorf("FormatReply(%v).Text = %q, want %q", item.raw, reply.Text, item.text)
		}
		if reply.Truncated {
			t.Errorf("FormatReply(%v) 不应截断", item.raw)
		}
		if reply.Bytes <= 0 && item.raw != nil {
			t.Errorf("FormatReply(%v).Bytes 应为正数", item.raw)
		}
	}
}

func TestFormatReplyArraysAndMaps(t *testing.T) {
	reply := FormatReply([]interface{}{"a", "b", "c"}, 0, 0)
	if reply.Text != "a\nb\nc" {
		t.Errorf("数组渲染 = %q", reply.Text)
	}
	// HGETALL 的 RESP2 平铺数组
	reply = FormatReply([]interface{}{"name", "alice", "age", "30"}, 0, 0)
	if reply.Text != "name\nalice\nage\n30" {
		t.Errorf("平铺数组渲染 = %q", reply.Text)
	}
	// RESP3 map：键排序后 key: value
	reply = FormatReply(map[interface{}]interface{}{"b": "2", "a": "1"}, 0, 0)
	if reply.Text != "a: 1\nb: 2" {
		t.Errorf("map 渲染 = %q", reply.Text)
	}
	// 嵌套数组单行 JSON 风格
	reply = FormatReply([]interface{}{"x", []interface{}{"1", "2"}}, 0, 0)
	if reply.Text != "x\n[1, 2]" && reply.Text != "x\n[\"1\", \"2\"]" {
		t.Errorf("嵌套渲染 = %q", reply.Text)
	}
}

func TestFormatReplyValueTruncation(t *testing.T) {
	value := strings.Repeat("x", 100)
	reply := FormatReply(value, 20, 0)
	if !reply.Truncated || len(reply.Text) != 20+len("…[已截断，原始长度 100 字节]") {
		t.Errorf("单值截断异常: truncated=%v len=%d", reply.Truncated, len(reply.Text))
	}
	if reply.Bytes != 100 {
		t.Errorf("Bytes 应记录原始长度: %d", reply.Bytes)
	}
	// JSON 值也要同步截断
	raw, err := json.Marshal(reply.JSONValue)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "已截断") {
		t.Errorf("JSON 值应包含截断标记: %s", raw)
	}
}

func TestFormatReplyTotalBudget(t *testing.T) {
	items := []interface{}{strings.Repeat("a", 100), strings.Repeat("b", 100), strings.Repeat("c", 100)}
	reply := FormatReply(items, 0, 150)
	if !reply.Truncated {
		t.Fatal("总量超限应标记截断")
	}
	if !strings.Contains(reply.Text, "b") || strings.Contains(reply.Text, "cccc") {
		t.Errorf("应保留前段省略后段: %q", reply.Text)
	}
	if reply.Bytes != 300 {
		t.Errorf("Bytes 应为原始总量: %d", reply.Bytes)
	}
}

func TestFormatReplyJSONSafety(t *testing.T) {
	reply := FormatReply([]interface{}{map[interface{}]interface{}{"key": int64(1)}, nil}, 0, 0)
	raw, err := json.Marshal(reply.JSONValue)
	if err != nil {
		t.Fatalf("回复应可 JSON 序列化: %v", err)
	}
	if string(raw) != `[{"key":1},null]` {
		t.Errorf("JSON 结构异常: %s", raw)
	}
}
