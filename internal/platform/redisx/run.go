package redisx

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// RunOutcome 是一次命令执行的原始结果（尚未落审计）。
type RunOutcome struct {
	Reply    Reply
	Duration time.Duration
	TimedOut bool
	RedisErr error // Redis 返回的错误（WRONGTYPE / MOVED / 未知命令…），nil 表示成功
}

// RunCommand 执行单条命令并格式化回复。context 由调用方设定超时；
// 值截断阈值与总量上限沿用平台设置与固定常量。
func RunCommand(ctx context.Context, client redis.UniversalClient, args []string, valueLimit, totalLimit int) RunOutcome {
	started := time.Now()
	raw, err := client.Do(ctx, toAny(args)...).Result()
	outcome := RunOutcome{
		Duration: time.Since(started),
		Reply:    FormatReply(raw, valueLimit, totalLimit),
	}
	switch {
	case err == nil:
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		outcome.TimedOut = true
	default:
		outcome.RedisErr = err
	}
	return outcome
}

// ScanKeys 安全 SCAN：游标迭代直到取满 limit 个 key 或游标归零。
// 返回 (keys, 是否因上限提前停止, 错误)。pattern 传 "" 等价于 *。
func ScanKeys(ctx context.Context, client redis.UniversalClient, pattern string, limit, count int) ([]string, bool, error) {
	if pattern == "" {
		pattern = "*"
	}
	if count < 10 {
		count = 100
	}
	keys := make([]string, 0, min(limit, 1024))
	var cursor uint64
	for {
		batch, next, err := client.Scan(ctx, cursor, pattern, int64(count)).Result()
		if err != nil {
			return keys, false, err
		}
		for _, key := range batch {
			if len(keys) >= limit {
				return keys, true, nil
			}
			keys = append(keys, key)
		}
		cursor = next
		if cursor == 0 || len(keys) >= limit {
			return keys, len(keys) >= limit && next != 0, nil
		}
	}
}

func toAny(args []string) []any {
	values := make([]any, len(args))
	for i, arg := range args {
		values[i] = arg
	}
	return values
}
