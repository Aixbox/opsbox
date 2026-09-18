// Package sshtest 提供一个进程内的假 SSH 服务器，供 sshx / modules/ssh 做端到端测试。
// 只实现测试需要的子集：密码认证、exec（识别几条固定命令）、pty-req / shell（回显）。
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Server 是测试用 SSH 服务器。
type Server struct {
	Addr     string
	Port     int
	User     string
	Password string
	HostKey  string // SHA256 指纹
	// Root 是 SFTP 子系统的工作目录（绝对路径）
	Root string

	listener net.Listener
	config   *ssh.ServerConfig
	wg       sync.WaitGroup
	mu       sync.Mutex
	commands []string
}

// Start 在随机端口启动服务器；root 为 SFTP 子系统的工作目录（省略则用系统临时目录）。
func Start(user, password string, root ...string) (*Server, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return nil, err
	}
	s := &Server{User: user, Password: password, HostKey: ssh.FingerprintSHA256(signer.PublicKey()), Root: os.TempDir()}
	if len(root) > 0 && root[0] != "" {
		s.Root = root[0]
	}
	s.config = &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if meta.User() == user && string(pass) == password {
				return nil, nil
			}
			return nil, fmt.Errorf("认证失败")
		},
	}
	s.config.AddHostKey(signer)
	s.listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s.Addr = s.listener.Addr().String()
	_, portText, _ := net.SplitHostPort(s.Addr)
	s.Port, _ = strconv.Atoi(portText)
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// Close 停止服务器。
func (s *Server) Close() {
	_ = s.listener.Close()
	s.wg.Wait()
}

// Commands 返回收到过的 exec 命令（按顺序）。
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	serverConn, channels, requests, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		return
	}
	defer serverConn.Close()
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			return
		}
		go s.session(channel, channelRequests)
	}
}

func (s *Server) session(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	for request := range requests {
		switch request.Type {
		case "pty-req", "window-change", "env":
			if request.WantReply {
				_ = request.Reply(true, nil)
			}
		case "subsystem":
			// SFTP 子系统：用 pkg/sftp 的服务端实现，根目录即 s.Root
			name := ""
			if len(request.Payload) > 4 {
				name = string(request.Payload[4:])
			}
			if name != "sftp" {
				if request.WantReply {
					_ = request.Reply(false, nil)
				}
				continue
			}
			if request.WantReply {
				_ = request.Reply(true, nil)
			}
			server, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(s.Root))
			if err != nil {
				return
			}
			_ = server.Serve()
			_ = server.Close()
			return
		case "exec":
			command := string(request.Payload[4:])
			s.mu.Lock()
			s.commands = append(s.commands, command)
			s.mu.Unlock()
			if request.WantReply {
				_ = request.Reply(true, nil)
			}
			exit := s.run(channel, command)
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(exit)}))
			return
		case "shell":
			if request.WantReply {
				_ = request.Reply(true, nil)
			}
			s.shell(channel)
			return
		default:
			if request.WantReply {
				_ = request.Reply(false, nil)
			}
		}
	}
}

// run 解释几条固定命令：echo X → 输出 X；exit N → 退出码 N；sleep N → 睡 N 秒；yes N → 输出 N 行；其他 → 命令不存在 127。
func (s *Server) run(channel ssh.Channel, command string) int {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return 0
	}
	switch fields[0] {
	case "echo":
		_, _ = io.WriteString(channel, strings.Join(fields[1:], " ")+"\n")
		return 0
	case "exit":
		code, _ := strconv.Atoi(fields[1])
		_, _ = io.WriteString(channel.Stderr(), "failing on purpose\n")
		return code
	case "sleep":
		seconds, _ := strconv.ParseFloat(fields[1], 64)
		time.Sleep(time.Duration(seconds * float64(time.Second)))
		return 0
	case "yes":
		n, _ := strconv.Atoi(fields[1])
		for i := 0; i < n; i++ {
			_, _ = fmt.Fprintf(channel, "line %06d\n", i)
		}
		return 0
	case "uname":
		_, _ = io.WriteString(channel, "Linux sshtest 6.0 #1 SMP x86_64 GNU/Linux\n")
		return 0
	}
	_, _ = io.WriteString(channel.Stderr(), "sh: "+fields[0]+": command not found\n")
	return 127
}
