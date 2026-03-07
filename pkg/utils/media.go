// Package utils 提供通用工具函数
// 本文件包含媒体文件处理工具
package utils

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// IsAudioFile 检查文件是否为音频文件
// 基于文件扩展名和 MIME 类型判断
//
// 参数：
// - filename: 文件名
// - contentType: MIME 内容类型
//
// 返回：
// - bool: true 表示是音频文件
func IsAudioFile(filename, contentType string) bool {
	audioExtensions := []string{".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac", ".wma"}
	audioTypes := []string{"audio/", "application/ogg", "application/x-ogg"}

	for _, ext := range audioExtensions {
		if strings.HasSuffix(strings.ToLower(filename), ext) {
			return true
		}
	}

	for _, audioType := range audioTypes {
		if strings.HasPrefix(strings.ToLower(contentType), audioType) {
			return true
		}
	}

	return false
}

// SanitizeFilename 清理文件名中的危险字符
// 返回安全的本地文件系统存储版本
// 移除路径遍历攻击字符（..、/、\）
//
// 参数：
// - filename: 原始文件名
//
// 返回：
// - string: 清理后的安全文件名
func SanitizeFilename(filename string) string {
	// 获取基本文件名（不含路径）
	base := filepath.Base(filename)

	// 移除任何目录遍历尝试
	base = strings.ReplaceAll(base, "..", "")
	base = strings.ReplaceAll(base, "/", "_")
	base = strings.ReplaceAll(base, "\\", "_")

	return base
}

// DownloadOptions 下载选项结构
type DownloadOptions struct {
	Timeout      time.Duration   // 超时时间
	ExtraHeaders map[string]string // 额外头部
	LoggerPrefix string          // 日志前缀
	ProxyURL     string          // 代理 URL
}

// DownloadFile 从 URL 下载文件到本地临时目录
// 返回本地文件路径，错误时返回空字符串
//
// 参数：
// - urlStr: 下载 URL
// - filename: 目标文件名
// - opts: 下载选项
//
// 返回：
// - string: 本地文件路径（空表示错误）
func DownloadFile(urlStr, filename string, opts DownloadOptions) string {
	// 设置默认值
	if opts.Timeout == 0 {
		opts.Timeout = 60 * time.Second
	}
	if opts.LoggerPrefix == "" {
		opts.LoggerPrefix = "utils"
	}

	mediaDir := filepath.Join(os.TempDir(), "picoclaw_media")
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		logger.ErrorCF(opts.LoggerPrefix, "Failed to create media directory", map[string]any{
			"error": err.Error(),
		})
		return ""
	}

	// 生成带 UUID 前缀的唯一文件名，防止冲突
	safeName := SanitizeFilename(filename)
	localPath := filepath.Join(mediaDir, uuid.New().String()[:8]+"_"+safeName)

	// 创建 HTTP 请求
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		logger.ErrorCF(opts.LoggerPrefix, "Failed to create download request", map[string]any{
			"error": err.Error(),
		})
		return ""
	}

	// 添加额外头部（如 Slack 的 Authorization）
	for key, value := range opts.ExtraHeaders {
		req.Header.Set(key, value)
	}

	client := &http.Client{Timeout: opts.Timeout}
	if opts.ProxyURL != "" {
		proxyURL, parseErr := url.Parse(opts.ProxyURL)
		if parseErr != nil {
			logger.ErrorCF(opts.LoggerPrefix, "Invalid proxy URL for download", map[string]any{
				"error": parseErr.Error(),
				"proxy": opts.ProxyURL,
			})
			return ""
		}
		client.Transport = &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		logger.ErrorCF(opts.LoggerPrefix, "Failed to download file", map[string]any{
			"error": err.Error(),
			"url":   urlStr,
		})
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.ErrorCF(opts.LoggerPrefix, "File download returned non-200 status", map[string]any{
			"status": resp.StatusCode,
			"url":    urlStr,
		})
		return ""
	}

	out, err := os.Create(localPath)
	if err != nil {
		logger.ErrorCF(opts.LoggerPrefix, "Failed to create local file", map[string]any{
			"error": err.Error(),
		})
		return ""
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		os.Remove(localPath)
		logger.ErrorCF(opts.LoggerPrefix, "Failed to write file", map[string]any{
			"error": err.Error(),
		})
		return ""
	}

	logger.DebugCF(opts.LoggerPrefix, "File downloaded successfully", map[string]any{
		"path": localPath,
	})

	return localPath
}

// DownloadFileSimple 简化版文件下载（无选项）
//
// 参数：
// - url: 下载 URL
// - filename: 目标文件名
//
// 返回：
// - string: 本地文件路径
func DownloadFileSimple(url, filename string) string {
	return DownloadFile(url, filename, DownloadOptions{
		LoggerPrefix: "media",
	})
}
