package sshx

import (
	"bytes"
	"fmt"
	"sync"
)

// truncationMarker 插在被截断输出的中间，提示 AI/人「这里省略了一段」。
const truncationMarker = "\n... [输出过长，中间已省略 %d 字节] ...\n"

// Truncate 把输出限制在 limit 字节以内：保留头部约 2/3、尾部约 1/3，中间插省略标记。
// 返回 (结果, 是否截断)。limit<=0 表示不限制。
func Truncate(data []byte, limit int) ([]byte, bool) {
	if limit <= 0 || len(data) <= limit {
		return data, false
	}
	return truncateWindow(data, limit, len(data)-limit), true
}

// truncateWindow 对已知总省略量 omitted 的窗口 data 做头尾切分。
func truncateWindow(data []byte, limit, omitted int) []byte {
	marker := []byte(fmt.Sprintf(truncationMarker, omitted))
	budget := limit - len(marker)
	if budget < 64 {
		// 限制小到放不下标记时直接硬截头部
		return append([]byte(nil), data[:limit]...)
	}
	head := budget * 2 / 3
	tail := budget - head
	out := make([]byte, 0, limit)
	out = append(out, data[:head]...)
	out = append(out, marker...)
	out = append(out, data[len(data)-tail:]...)
	return out
}

// LimitedBuffer 在写入时统计总字节数，但内存里只保留前 limit 字节 + 最近 limit 字节，
// 供长输出命令在不撑爆内存的前提下产出「头 + 尾」截断结果。并发安全（stdout/stderr 各一份）。
type LimitedBuffer struct {
	mu    sync.Mutex
	limit int
	head  bytes.Buffer
	tail  []byte
	total int
}

// NewLimitedBuffer 创建限幅缓冲区；limit<=0 表示不限制（全部保留）。
func NewLimitedBuffer(limit int) *LimitedBuffer {
	return &LimitedBuffer{limit: limit}
}

func (b *LimitedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total += written
	if b.limit <= 0 {
		b.head.Write(p)
		return written, nil
	}
	if remaining := b.limit - b.head.Len(); remaining > 0 {
		n := min(remaining, len(p))
		b.head.Write(p[:n])
		p = p[n:]
	}
	if len(p) > 0 {
		b.tail = append(b.tail, p...)
		if len(b.tail) > b.limit {
			b.tail = append([]byte(nil), b.tail[len(b.tail)-b.limit:]...)
		}
	}
	return written, nil
}

// Total 返回写入的总字节数（含被丢弃部分）。
func (b *LimitedBuffer) Total() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

// Bytes 返回按 limit 截断后的输出与截断标记。
func (b *LimitedBuffer) Bytes() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit <= 0 || b.total <= b.limit {
		return append([]byte(nil), b.head.Bytes()...), false
	}
	// head 已满 limit，tail 是最近的 limit 字节；窗口足够做头尾切分，省略量按真实总量计
	combined := make([]byte, 0, b.head.Len()+len(b.tail))
	combined = append(combined, b.head.Bytes()...)
	combined = append(combined, b.tail...)
	return truncateWindow(combined, b.limit, b.total-b.limit), true
}
