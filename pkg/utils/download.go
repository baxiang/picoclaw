// Package utils 提供通用工具函数
// 本文件包含 HTTP 下载工具
package utils

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// DownloadToFile 将 HTTP 响应流式下载到临时文件
// 使用小块传输（约 32KB），保持内存使用恒定，不受文件大小影响
//
// 参数：
// - ctx: 上下文用于取消/超时控制
// - client: HTTP 客户端（调用者控制超时、传输等）
// - req: 完整的 HTTP 请求（方法、URL、头部等）
// - maxBytes: 最大下载字节数，0 表示无限制
//
// 返回：
// - string: 临时文件路径（调用者负责完成后删除）
// - error: 下载错误
//
// 注意：
// - 任何错误都会自动清理临时文件
// - 调用者应在使用完后调用 os.Remove(path) 删除文件
func DownloadToFile(ctx context.Context, client *http.Client, req *http.Request, maxBytes int64) (string, error) {
	// 附加上下文
	req = req.WithContext(ctx)

	logger.DebugCF("download", "Starting download", map[string]any{
		"url":       req.URL.String(),
		"max_bytes": maxBytes,
	})

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 读取少量内容用于错误消息
		errBody := make([]byte, 512)
		n, _ := io.ReadFull(resp.Body, errBody)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(errBody[:n]))
	}

	// 创建临时文件
	tmpFile, err := os.CreateTemp("", "picoclaw-dl-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	logger.DebugCF("download", "Streaming to temp file", map[string]any{
		"path": tmpPath,
	})

	// 清理辅助函数 - 任何错误时删除临时文件
	cleanup := func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}

	// 限制下载大小
	var src io.Reader = resp.Body
	if maxBytes > 0 {
		src = io.LimitReader(resp.Body, maxBytes+1) // +1 用于检测溢出
	}

	written, err := io.Copy(tmpFile, src)
	if err != nil {
		cleanup()
		return "", fmt.Errorf("download write failed: %w", err)
	}

	if maxBytes > 0 && written > maxBytes {
		cleanup()
		return "", fmt.Errorf("download too large: %d bytes (max %d)", written, maxBytes)
	}

	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to close temp file: %w", err)
	}

	logger.DebugCF("download", "Download complete", map[string]any{
		"path":          tmpPath,
		"bytes_written": written,
	})

	return tmpPath, nil
}
