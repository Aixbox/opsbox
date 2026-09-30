package sshx

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/pkg/sftp"
)

// UploadAt 把 reader 的内容流式写到远端 remotePath 的 offset 处，父目录不存在时自动创建。
// offset==0 时截断重写（新上传），offset>0 时定位续写（分片 / 断点续传）。返回本次写入的字节数。
func UploadAt(ctx context.Context, client *Client, remotePath string, offset int64, reader io.Reader) (int64, error) {
	sftpClient, err := sftp.NewClient(client.Client)
	if err != nil {
		return 0, fmt.Errorf("打开 SFTP 通道失败: %w", err)
	}
	defer sftpClient.Close()
	if dir := path.Dir(remotePath); dir != "." && dir != "/" {
		_ = sftpClient.MkdirAll(dir)
	}
	// pkg/sftp 会把 os.O_* 翻译成 SFTP 标志，直接用标准常量即可
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if offset > 0 {
		flags = os.O_WRONLY | os.O_CREATE
	}
	file, err := sftpClient.OpenFile(remotePath, flags)
	if err != nil {
		return 0, fmt.Errorf("打开远端文件失败: %w", err)
	}
	defer file.Close()
	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return 0, fmt.Errorf("定位远端文件偏移 %d 失败: %w", offset, err)
		}
	}
	written, err := io.Copy(file, &contextReader{ctx: ctx, r: reader})
	if err != nil {
		return written, fmt.Errorf("写入远端文件失败: %w", err)
	}
	// 续传时写完需要截断：防止新文件比旧文件短时，旧尾巴残留
	if offset > 0 {
		if err := file.Truncate(offset + written); err != nil {
			return written, fmt.Errorf("截断远端文件失败: %w", err)
		}
	}
	return written, nil
}

// DownloadAt 从远端 remotePath 的 offset 处流式读到 writer，返回读取字节数。
func DownloadAt(ctx context.Context, client *Client, remotePath string, offset int64, writer io.Writer) (int64, error) {
	sftpClient, err := sftp.NewClient(client.Client)
	if err != nil {
		return 0, fmt.Errorf("打开 SFTP 通道失败: %w", err)
	}
	defer sftpClient.Close()
	file, err := sftpClient.Open(remotePath)
	if err != nil {
		return 0, fmt.Errorf("打开远端文件失败: %w", err)
	}
	defer file.Close()
	if info, statErr := file.Stat(); statErr == nil && info.IsDir() {
		return 0, fmt.Errorf("远端路径 %s 是目录", remotePath)
	}
	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return 0, fmt.Errorf("定位远端文件偏移 %d 失败: %w", offset, err)
		}
	}
	copied, err := io.Copy(writer, &contextReader{ctx: ctx, r: file})
	if err != nil {
		return copied, fmt.Errorf("读取远端文件失败: %w", err)
	}
	return copied, nil
}

// FileStat 是远端文件的元信息。
type FileStat struct {
	Size    int64
	ModTime int64 // Unix 毫秒时间戳
}

// StatSize 返回远端文件大小；不存在返回 (0, false, nil)，是目录则报错。
// 续传前用它确认远端已有多少字节。
func StatSize(client *Client, remotePath string) (int64, bool, error) {
	stat, exists, err := Stat(client, remotePath)
	if err != nil || !exists {
		return 0, exists, err
	}
	return stat.Size, true, nil
}

// Stat 返回远端文件的完整元信息（大小与修改时间）；不存在返回 (FileStat{}, false, nil)，是目录则报错。
func Stat(client *Client, remotePath string) (FileStat, bool, error) {
	sftpClient, err := sftp.NewClient(client.Client)
	if err != nil {
		return FileStat{}, false, fmt.Errorf("打开 SFTP 通道失败: %w", err)
	}
	defer sftpClient.Close()
	info, err := sftpClient.Stat(remotePath)
	if err != nil {
		if os.IsNotExist(err) {
			return FileStat{}, false, nil
		}
		return FileStat{}, false, fmt.Errorf("读取远端文件信息失败: %w", err)
	}
	if info.IsDir() {
		return FileStat{}, false, fmt.Errorf("远端路径 %s 是目录", remotePath)
	}
	return FileStat{Size: info.Size(), ModTime: info.ModTime().UnixMilli()}, true, nil
}

// contextReader 让 io.Copy 能被 ctx 取消（sftp 本身不接受 ctx）。
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
