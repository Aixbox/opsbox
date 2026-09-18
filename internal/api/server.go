// Package api 组装本地 HTTP 服务：gin 引擎、CORS、路由与健康探针。
//
// 服务只监听 127.0.0.1：本机 UI（Wails WebView 与浏览器 dev 模式）和未来的 CLI 都走它。
// 无登录、无权限码——安全边界是「本机进程」+ 操作级闸门（黑名单 / 审批队列 / 一次性 WS 票据）。
package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"opsbox/internal/platform/security"
	redisops "opsbox/internal/redis"
	sqlops "opsbox/internal/sql"
	"opsbox/internal/ssh"
)

const (
	// PreferredPort 是默认端口；被占用时向上顺延，直到找到可用端口。
	PreferredPort = 37421
	// LogRetentionDays 是执行审计日志的保留天数。
	LogRetentionDays = 90
)

// Server 是本地 API 服务。
type Server struct {
	engine       *gin.Engine
	listener     net.Listener
	http         *http.Server
	sshService   *ssh.Service
	sqlService   *sqlops.Service
	redisService *redisops.Service
	log          *slog.Logger
	version      string
	// showUI 由宿主注册：二启进程经 POST /ui/show 请求唤出本实例的窗口。
	showUI func()
}

// SetShowUI 注册「唤出主窗口」回调（宿主在 startup 时设置）。
func (s *Server) SetShowUI(fn func()) { s.showUI = fn }

// New 创建服务并装配路由。version 用于 /healthz 身份标识（CLI 靠它确认连的是 opsbox 而不是别的本地服务）。
func New(db *sql.DB, cipher *security.TokenCipher, log *slog.Logger, version string) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	s := &Server{log: log, version: version}
	// originGuard 必须先于 cors：恶意网页的跨站请求在进业务前就被拒，
	// 而不是只靠 CORS 响应头让浏览器拦响应（简单请求服务器侧照样会执行）。
	engine.Use(gin.Recovery(), originGuard(), cors())
	engine.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "app": "opsbox", "version": version})
	})
	// 二启进程进来发现已有实例时，调这个接口唤出主窗口然后自己退出（Docker Desktop 行为）。
	// 只暴露「显示窗口」这一无副作用动作，本机回环可达即可。
	engine.POST("/ui/show", func(c *gin.Context) {
		if s.showUI == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ui hook 未注册"})
			return
		}
		s.showUI()
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	group := engine.Group("/api/v1")

	sshService := ssh.NewService(db, cipher, nil)
	sshService.SetLogger(log)
	ssh.NewHandler(sshService).RegisterRoutes(group)

	sqlService := sqlops.NewService(db, cipher, nil)
	sqlService.SetLogger(log)
	sqlops.NewHandler(sqlService).RegisterRoutes(group)

	redisService := redisops.NewService(db, cipher)
	redisService.SetLogger(log)
	redisops.NewHandler(redisService).RegisterRoutes(group)

	s.engine = engine
	s.sshService, s.sqlService, s.redisService = sshService, sqlService, redisService
	return s, nil
}

// Listen 绑定 127.0.0.1 上从 preferred 开始的第一个可用端口。
func (s *Server) Listen(preferred int) error {
	var lastErr error
	for port := preferred; port < preferred+25; port++ {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			s.listener = listener
			break
		}
		lastErr = err
	}
	if s.listener == nil {
		return lastErr
	}
	s.http = &http.Server{Handler: s.engine, ReadHeaderTimeout: 10 * time.Second}
	return nil
}

// Port 返回实际监听端口；未监听返回 0。
func (s *Server) Port() int {
	if s.listener == nil {
		return 0
	}
	return s.listener.Addr().(*net.TCPAddr).Port
}

// Serve 阻塞提供服务；在独立协程里调用。
func (s *Server) Serve() error {
	return s.http.Serve(s.listener)
}

// RunBackground 启动后台协程（会话回收、票据清理、日志清理），ctx 结束即退出。
func (s *Server) RunBackground(ctx context.Context) {
	s.sshService.Run(ctx)
	s.sqlService.Run(ctx)
	s.redisService.Run(ctx)
	go func() {
		s.cleanup()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.cleanup()
			}
		}
	}()
}

func (s *Server) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	retention := LogRetentionDays * 24 * time.Hour
	if err := s.sshService.Cleanup(ctx, retention); err != nil {
		s.log.Warn("ssh log cleanup", "error", err)
	}
	if err := s.sqlService.Cleanup(ctx, retention); err != nil {
		s.log.Warn("sql log cleanup", "error", err)
	}
	if err := s.redisService.Cleanup(ctx, retention); err != nil {
		s.log.Warn("redis log cleanup", "error", err)
	}
}

// Shutdown 优雅停机。
func (s *Server) Shutdown(ctx context.Context) error {
	if s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}

// cors 允许任意来源：本地应用不跨权限边界，UI 在 Wails WebView 与浏览器 dev 模式下来源不同。
// 全部请求不带凭证，"*" 语义足够。
func cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// originGuard 拒绝一切来自非本地 UI 的浏览器请求（有 Origin 头但不是本机来源）。
// CLI / 脚本没有 Origin 头，不受影响；恶意网页借用户浏览器发的跨站请求——无论预检与否——
// 都会在服务器侧被拒，防止远程页面操控本机运维工具。
func originGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && !isLocalUIOrigin(origin) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": "FORBIDDEN_ORIGIN", "message": "拒绝来自非本地来源的请求"})
			return
		}
		c.Next()
	}
}

// isLocalUIOrigin 判断 Origin 是否为本地 UI（Wails WebView / 本机 dev server / file://）。
func isLocalUIOrigin(origin string) bool {
	if origin == "null" {
		return true // file:// 等非标准来源
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	switch parsed.Hostname() {
	case "wails.localhost", "wails", "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}
