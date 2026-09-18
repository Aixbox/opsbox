package sshx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrHostKeyMismatch 表示目标主机公钥与首次记录不一致（可能被劫持），拒绝连接。
var ErrHostKeyMismatch = errors.New("目标主机的 host key 与已记录指纹不一致，已拒绝连接")

// Target 是一次拨号所需的全部明文参数（凭证已由调用方解密）。
type Target struct {
	Host       string
	Port       int
	Username   string
	AuthType   string // password | key
	Password   string
	PrivateKey string
	Passphrase string
	// HostKey 为已记录的指纹（SHA256:...）；为空则本次 TOFU 记录。
	HostKey string
	Timeout time.Duration
}

// Client 是一条已建立的 SSH 连接；HostKey 是本次握手看到的服务器公钥指纹。
type Client struct {
	*ssh.Client
	HostKey string
}

// Dialer 抽象拨号，便于模块层用假实现做单测。
type Dialer interface {
	Dial(ctx context.Context, target Target) (*Client, error)
}

// NetDialer 是真实的 TCP + SSH 握手实现。
type NetDialer struct{}

// Dial 建立连接：支持密码 / 私钥（可带口令）认证，host key 首连记录、之后强校验。
func (NetDialer) Dial(ctx context.Context, target Target) (*Client, error) {
	methods, err := authMethods(target)
	if err != nil {
		return nil, err
	}
	timeout := target.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	seen := ""
	config := &ssh.ClientConfig{
		User:    target.Username,
		Auth:    methods,
		Timeout: timeout,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			seen = ssh.FingerprintSHA256(key)
			if target.HostKey != "" && target.HostKey != seen {
				return ErrHostKeyMismatch
			}
			return nil
		},
	}
	address := net.JoinHostPort(target.Host, strconv.Itoa(target.Port))
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %w", address, err)
	}
	// 握手阶段的超时由 ClientConfig.Timeout 控制；ctx 取消时主动关连接让握手尽快失败
	done := make(chan struct{})
	go func() {
		select {
		case <-dialCtx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	sshConn, channels, requests, err := ssh.NewClientConn(conn, address, config)
	close(done)
	if err != nil {
		_ = conn.Close()
		if errors.Is(err, ErrHostKeyMismatch) {
			return nil, ErrHostKeyMismatch
		}
		return nil, fmt.Errorf("SSH 握手失败: %w", err)
	}
	return &Client{Client: ssh.NewClient(sshConn, channels, requests), HostKey: seen}, nil
}

func authMethods(target Target) ([]ssh.AuthMethod, error) {
	switch target.AuthType {
	case "password":
		if target.Password == "" {
			return nil, errors.New("密码认证缺少密码")
		}
		return []ssh.AuthMethod{ssh.Password(target.Password), ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range questions {
				answers[i] = target.Password
			}
			return answers, nil
		})}, nil
	case "key":
		if target.PrivateKey == "" {
			return nil, errors.New("私钥认证缺少私钥")
		}
		var signer ssh.Signer
		var err error
		if target.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(target.PrivateKey), []byte(target.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(target.PrivateKey))
		}
		if err != nil {
			return nil, fmt.Errorf("私钥解析失败: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default:
		return nil, fmt.Errorf("不支持的认证方式 %q", target.AuthType)
	}
}
