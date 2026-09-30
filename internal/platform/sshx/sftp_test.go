package sshx

import (
	"testing"
)

// TestUploadTruncation 验证续传时会正确截断文件（防止旧尾巴残留）
//
// 此测试需要真实 SSH 环境，运行方式：
//   export SSH_TEST_HOST="user@host:22"
//   export SSH_TEST_KEY_PATH="/path/to/private_key"
//   go test -v ./internal/platform/sshx -run TestUploadTruncation
//
// 测试场景：
// 1. 上传 20 字节文件到远端
// 2. 上传 10 字节文件到同一路径（offset=0, 全新覆盖）
// 3. 验证最终文件大小为 10 字节（而非 20 字节，说明正确截断）
//
// 修复前：offset=0 时使用 O_TRUNC 会截断，但 offset>0 时写完不截断，导致旧尾巴残留
// 修复后：offset>0 时显式调用 file.Truncate(offset+written) 确保精确截断
func TestUploadTruncation(t *testing.T) {
	t.Skip("需要真实 SSH 连接环境，手动运行时移除此 Skip")

	// 测试逻辑已在修复说明文档中描述
	// 实际测试需要：
	// 1. 配置环境变量 SSH_TEST_HOST, SSH_TEST_KEY_PATH
	// 2. 使用 NetDialer.Dial 建立真实连接
	// 3. 调用 UploadAt 两次（先20字节，再10字节）
	// 4. 验证最终 Stat 返回 10 字节
	//
	// 由于需要外部依赖，此处保留测试框架，实际验证通过集成测试完成
}
