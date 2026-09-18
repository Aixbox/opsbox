package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"opsbox/internal/api"
	"opsbox/internal/climgr"
	"opsbox/internal/platform/security"
	"opsbox/internal/store"
)

// appVersion 由构建注入（wails build -ldflags "-X main.appVersion=…"），与内嵌 CLI 同版本。
var appVersion = "dev"

// App 是 Wails 绑定对象：生命周期管理 + 给前端暴露少量元信息。
type App struct {
	ctx     context.Context
	server  *api.Server
	dataDir string
}

func NewApp() *App { return &App{} }

// config 是持久化在数据目录里的本地配置（当前只有加密密钥）。
type config struct {
	Secret string `json:"secret"`
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	log := slog.Default()
	dataDir, err := ensureDataDir()
	if err != nil {
		log.Error("prepare data dir", "error", err)
		return
	}
	a.dataDir = dataDir

	cfg, err := loadConfig(filepath.Join(dataDir, "config.json"))
	if err != nil {
		log.Error("load config", "error", err)
		return
	}
	cipher, err := security.NewTokenCipher(cfg.Secret)
	if err != nil {
		log.Error("create cipher", "error", err)
		return
	}
	db, err := store.Open(filepath.Join(dataDir, "opsbox.db"))
	if err != nil {
		log.Error("open database", "error", err)
		return
	}
	server, err := api.New(db, cipher, log, appVersion)
	if err != nil {
		log.Error("create api server", "error", err)
		return
	}
	if err := server.Listen(api.PreferredPort); err != nil {
		log.Error("listen 127.0.0.1", "error", err)
		return
	}
	go func() {
		if err := server.Serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("api server exited", "error", err)
		}
	}()
	server.RunBackground(ctx)
	a.server = server
	a.writeDiscoveryFile(server.Port())
	log.Info("opsbox api ready", "port", server.Port(), "dataDir", dataDir)
}

func (a *App) shutdown(ctx context.Context) {
	if a.server != nil {
		_ = a.server.Shutdown(ctx)
	}
	a.removeDiscoveryFile()
}

// discoveryFile 是 CLI 的快速发现文件：opsbox 实际监听端口写在数据目录里，
// CLI 优先读它（校验 /healthz 身份后使用），避免端口顺延场景下的逐口扫描。
type discoveryFile struct {
	Port      int    `json:"port"`
	PID       int    `json:"pid"`
	Version   string `json:"version"`
	StartedAt string `json:"startedAt"`
}

func (a *App) discoveryPath() string { return filepath.Join(a.dataDir, "server.json") }

func (a *App) writeDiscoveryFile(port int) {
	payload, err := json.Marshal(discoveryFile{
		Port:      port,
		PID:       os.Getpid(),
		Version:   appVersion,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	if err := os.WriteFile(a.discoveryPath(), payload, 0o600); err != nil {
		slog.Default().Warn("write server.json", "error", err)
	}
}

func (a *App) removeDiscoveryFile() {
	if a.dataDir == "" {
		return
	}
	if err := os.Remove(a.discoveryPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Default().Warn("remove server.json", "error", err)
	}
}

// ServerPort 供前端发现 API 端口（默认端口被占用时会同向顺延）。
func (a *App) ServerPort() int {
	if a.server == nil {
		return 0
	}
	return a.server.Port()
}

// DataDir 返回数据目录（数据库、密钥所在），前端「打开数据目录」可用。
func (a *App) DataDir() string { return a.dataDir }

// CLIStatus 返回内嵌运维 CLI 的安装状态（前端「AI CLI」面板展示）。
func (a *App) CLIStatus() climgr.Status { return climgr.Current() }

// InstallCLIs 把内嵌的 sshctl / sqlctl / redisctl 安装到用户目录并写入 PATH（幂等，可作升级）。
func (a *App) InstallCLIs() (climgr.Status, error) { return climgr.Install() }

// UninstallCLIs 移除已安装的 CLI 并清理 PATH 条目。
func (a *App) UninstallCLIs() (climgr.Status, error) { return climgr.Uninstall() }

// ensureDataDir 返回（并创建）数据目录：<UserConfigDir>/opsbox。
func ensureDataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "opsbox")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// loadConfig 读取配置；密钥缺失时生成一次性随机密钥并保存。
// 注意：密钥丢失 = 已存凭证与命令输出无法解密，文件请勿删除。
func loadConfig(path string) (config, error) {
	var cfg config
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return cfg, fmt.Errorf("解析配置文件: %w", err)
		}
	case errors.Is(err, os.ErrNotExist):
		seed := make([]byte, 32)
		if _, err := rand.Read(seed); err != nil {
			return cfg, err
		}
		cfg.Secret = base64.RawURLEncoding.EncodeToString(seed)
		encoded, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return cfg, err
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			return cfg, err
		}
	default:
		return cfg, err
	}
	if cfg.Secret == "" {
		return cfg, errors.New("config.json 中 secret 为空")
	}
	return cfg, nil
}
