package redisx

import (
	"fmt"
	"sort"
	"strings"
)

// 回复总量的固定上限（可配置的是单个 string 的截断阈值，见 redis_settings.value_truncate_bytes）。
const TotalReplyLimitBytes = 256 << 10

// truncatedMarker 插在被截断的字符串后面，提示 AI / 人「后面还有内容没显示」。
const truncatedMarker = "…[已截断，原始长度 %d 字节]"

// Reply 是限幅后的执行结果：JSONValue 可直接 json.Marshal（给 CLI --json 与 Web），
// Text 是面向人的单行 / 多行渲染，两者内容一致、格式不同。
type Reply struct {
	JSONValue any    `json:"json,omitempty"`
	Text      string `json:"text,omitempty"`
	Bytes     int    `json:"-"` // 原始回复（限幅前）的估算字节数
	Truncated bool   `json:"-"`
}

// FormatReply 把 go-redis 通用回复（nil / int64 / string / []interface{} / map…）
// 规整为 JSON 安全结构并按限额截断：
//   - 单个 bulk string 超过 valueLimit 截断并加标记；
//   - 全部 string 内容加起来超过 totalLimit 后停止装填，整体截断。
//
// valueLimit <= 0 用 4096；totalLimit <= 0 用 TotalReplyLimitBytes。
func FormatReply(value any, valueLimit, totalLimit int) Reply {
	if valueLimit <= 0 {
		valueLimit = 4096
	}
	if totalLimit <= 0 {
		totalLimit = TotalReplyLimitBytes
	}
	w := &replyWriter{valueLimit: valueLimit, totalLimit: totalLimit}
	jsonValue := w.convert(value)
	return Reply{JSONValue: jsonValue, Text: render(jsonValue, w.budgetExceeded()), Bytes: w.written, Truncated: w.truncated}
}

// replyWriter 在转换回复的同时统计与限制字节数。
type replyWriter struct {
	valueLimit  int
	totalLimit  int
	written     int
	truncated   bool
	budgetSpent bool // 总量超限后，后续 string 一律丢弃
}

func (w *replyWriter) budgetExceeded() bool { return w.budgetSpent }

// writeString 计入并截断一个 bulk string；返回给上层装填的值。
func (w *replyWriter) writeString(value string) string {
	w.written += len(value)
	if w.budgetSpent {
		w.truncated = true
		return ""
	}
	if w.written > w.totalLimit {
		w.budgetSpent = true
		w.truncated = true
		return value[:max(0, len(value)-(w.written-w.totalLimit))] + fmt.Sprintf(truncatedMarker, len(value))
	}
	if len(value) > w.valueLimit {
		w.truncated = true
		return value[:w.valueLimit] + fmt.Sprintf(truncatedMarker, len(value))
	}
	return value
}

// convert 递归把 go-redis 回复转成 JSON 安全结构（RESP3 的 map 键可能是任意类型）。
func (w *replyWriter) convert(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return w.writeString(typed)
	case []byte:
		return w.writeString(string(typed))
	case int64, int, float64, bool:
		w.written += 8
		return typed
	case []interface{}:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, w.convert(item))
		}
		return items
	case map[string]interface{}:
		converted := make(map[string]any, len(typed))
		for key, item := range typed {
			converted[key] = w.convert(item)
		}
		return converted
	case map[interface{}]interface{}:
		converted := make(map[string]any, len(typed))
		for key, item := range typed {
			converted[fmt.Sprint(key)] = w.convert(item)
		}
		return converted
	default:
		// 其他类型（如 redis.StatusCmd 已被 Result() 拆包）兜底转文本
		return w.writeString(fmt.Sprint(typed))
	}
}

// render 把 JSON 安全结构渲染成人类可读文本：顶层元素一行一个，嵌套结构用单行 JSON。
func render(value any, budgetExceeded bool) string {
	var b strings.Builder
	renderTop(&b, value)
	if budgetExceeded {
		b.WriteString("\n…[回复总量超过上限，后续内容已省略]")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderTop(b *strings.Builder, value any) {
	switch typed := value.(type) {
	case nil:
		b.WriteString("(nil)\n")
	case []any:
		if len(typed) == 0 {
			b.WriteString("(empty array)\n")
			return
		}
		// 顶层一行一个元素；更深层的嵌套交给 renderInline（单行 JSON 风格）
		for _, item := range typed {
			b.WriteString(renderInline(item))
			b.WriteByte('\n')
		}
	case map[string]any:
		if len(typed) == 0 {
			b.WriteString("(empty map)\n")
			return
		}
		for _, key := range sortedMapKeys(typed) {
			b.WriteString(key)
			b.WriteString(": ")
			b.WriteString(renderInline(typed[key]))
			b.WriteByte('\n')
		}
	default:
		b.WriteString(renderInline(value))
		b.WriteByte('\n')
	}
}

// renderInline 单行渲染：简单值原样，嵌套数组 / map 用 JSON 风格。
func renderInline(value any) string {
	switch typed := value.(type) {
	case nil:
		return "(nil)"
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, quoteIfComplex(renderInline(item)))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		parts := make([]string, 0, len(typed))
		for _, key := range sortedMapKeys(typed) {
			parts = append(parts, fmt.Sprintf("%q: %s", key, quoteIfComplex(renderInline(typed[key]))))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return fmt.Sprint(value)
	}
}

func quoteIfComplex(text string) string {
	if strings.ContainsAny(text, " \t\n,\"{}[]") {
		return fmt.Sprintf("%q", text)
	}
	return text
}

func sortedMapKeys(typed map[string]any) []string {
	keys := make([]string, 0, len(typed))
	for key := range typed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
