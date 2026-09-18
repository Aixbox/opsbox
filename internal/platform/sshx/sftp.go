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
	return written, nil
}

// Upload 覆盖写远端文件（等价于 UploadAt(…, 0, …)）。
func Upload(ctx context.Context, client *Client, remotePath string, reader io.Reader) (int64, error) {
	return UploadAt(ctx, client, remotePath, 0, reader)
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

// Download 从头读取远端文件。
func Download(ctx context.Context, client *Client, remotePath string, writer io.Writer) (int64, error) {
	return DownloadAt(ctx, client, remotePath, 0, writer)
}

// StatSize 返回远端文件大小；不存在返回 (0, false, nil)，是目录则报错。
// 续传前用它确认远端已有多少字节。
func StatSize(client *Client, remotePath string) (int64, bool, error) {
	sftpClient, err := sftp.NewClient(client.Client)
	if err != nil {
		return 0, false, fmt.Errorf("打开 SFTP 通道失败: %w", err)
	}
	defer sftpClient.Close()
	info, err := sftpClient.Stat(remotePath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("读取远端文件信息失败: %w", err)
	}
	if info.IsDir() {
		return 0, false, fmt.Errorf("远端路径 %s 是目录", remotePath)
	}
	return info.Size(), true, nil
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
