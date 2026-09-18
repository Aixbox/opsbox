package redisx

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ClientConfig 是建立连接需要的最小配置（凭证已由上层解密）。
type ClientConfig struct {
	Host     string
	Port     int
	Password string
	DB       int
	TLS      bool
}

func (c ClientConfig) addr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// fingerprint 标识「连接到同一个实例的同一份配置」；变更后池里的旧客户端整体失效重建。
func (c ClientConfig) fingerprint() string {
	return fmt.Sprintf("%s|%d|%d|%t|%x", c.addr(), c.DB, len(c.Password), c.TLS, c.Password)
}

// Options 构建 go-redis 客户端选项（v1 仅 standalone；哨兵 / 集群后续按需加）。
// TLS v1 简化为不校验服务端证书（内网自签证书为主），后续版本再引入指纹校验。
func (c ClientConfig) Options() *redis.Options {
	options := &redis.Options{
		Network:      "tcp",
		Addr:         c.addr(),
		Password:     c.Password,
		DB:           c.DB,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 30 * time.Second,
		PoolSize:     8,
		PoolTimeout:  10 * time.Second,
		MaxIdleConns: 2,
	}
	if c.TLS {
		options.TLSConfig = &tls.Config{ServerName: c.Host, InsecureSkipVerify: true} //nolint:gosec
	}
	return options
}

// Handle 是从池里取出的一个实例客户端：client 可直接执行命令，classifier 缓存该实例的命令元数据。
type Handle struct {
	client     redis.UniversalClient
	classifier *Classifier
}

// Client 返回 go-redis 客户端。
func (h *Handle) Client() redis.UniversalClient { return h.client }

// Classifier 返回该实例的命令分类器（含运行时元数据缓存）。
func (h *Handle) Classifier() *Classifier { return h.classifier }

// poolEntry 是池里的一个槽位：客户端 + 建立时的配置指纹 + 元数据装载状态。
type poolEntry struct {
	client      redis.UniversalClient
	classifier  *Classifier
	fingerprint string
	loaded      bool // 是否已完成全量 COMMAND INFO 预热
	lastUsed    time.Time
}

// Pool 管理按连接 id 索引的 go-redis 客户端：懒建、配置变更失效、空闲回收。
// 一个连接对应一个池槽位（client 内部自带连接池），并发执行共享同一个 client。
type Pool struct {
	mu      sync.Mutex
	entries map[int64]*poolEntry
	now     func() time.Time
	idleTTL time.Duration
	log     *slog.Logger
}

// NewPool 创建池。
func NewPool() *Pool {
	return &Pool{entries: map[int64]*poolEntry{}, now: time.Now, idleTTL: time.Hour, log: slog.Default()}
}

// SetLogger 注入日志器。
func (p *Pool) SetLogger(log *slog.Logger) {
	if log != nil {
		p.log = log
	}
}

// Acquire 取出（或懒建）连接 id 对应的客户端；配置指纹变化时旧客户端整体关闭重建。
// warmup 为 true 时同步做一次全量命令元数据预热（超时容忍，失败不影响返回）。
func (p *Pool) Acquire(ctx context.Context, connectionID int64, config ClientConfig, warmup bool) (*Handle, error) {
	p.mu.Lock()
	entry, ok := p.entries[connectionID]
	if ok && entry.fingerprint != config.fingerprint() {
		_ = entry.client.Close()
		ok = false
	}
	if !ok {
		entry = &poolEntry{
			client:      redis.NewClient(config.Options()),
			classifier:  NewClassifier(),
			fingerprint: config.fingerprint(),
		}
		p.entries[connectionID] = entry
	}
	entry.lastUsed = p.now()
	client, classifier := entry.client, entry.classifier
	loaded := entry.loaded
	p.mu.Unlock()

	if warmup && !loaded {
		p.warmup(ctx, connectionID, client, classifier)
	}
	return &Handle{client: client, classifier: classifier}, nil
}

// warmup 预热实例的命令元数据（COMMAND INFO 全量）。失败只记日志：分类退回静态表，下次执行再试。
func (p *Pool) warmup(ctx context.Context, connectionID int64, client redis.UniversalClient, classifier *Classifier) {
	warmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	result, err := client.Command(warmCtx).Result()
	if err != nil {
		p.log.Debug("load redis command info", "connection", connectionID, "error", err)
		return
	}
	flags := make(map[string][]string, len(result))
	for name, info := range result {
		flags[name] = info.Flags
	}
	classifier.Load(flags)
	p.mu.Lock()
	if entry, ok := p.entries[connectionID]; ok && entry.classifier == classifier {
		entry.loaded = true
	}
	p.mu.Unlock()
}

// CloseIdle 回收空闲超过 idleTTL 的客户端。
func (p *Pool) CloseIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	deadline := p.now().Add(-p.idleTTL)
	for id, entry := range p.entries {
		if entry.lastUsed.After(deadline) {
			continue
		}
		_ = entry.client.Close()
		delete(p.entries, id)
	}
}

// Close 关闭指定连接的客户端（连接被删除或禁用时调用）。
func (p *Pool) Close(connectionID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if entry, ok := p.entries[connectionID]; ok {
		_ = entry.client.Close()
		delete(p.entries, connectionID)
	}
}

// CloseAll 关闭全部客户端（进程退出 / 连接删除时调用）。
func (p *Pool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, entry := range p.entries {
		_ = entry.client.Close()
		delete(p.entries, id)
	}
}

// Run 周期回收空闲客户端，直到 ctx 结束后关闭全部。
func (p *Pool) Run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			p.CloseAll()
			return
		case <-ticker.C:
			p.CloseIdle()
		}
	}
}
