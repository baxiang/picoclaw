// Package utils 提供通用工具函数
// 本文件包含 HTTP 请求重试工具
package utils

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const (
	maxRetries     = 3          // 最大重试次数
	retryDelayUnit = time.Second // 重试延迟单位
)

// shouldRetry 判断是否应该重试
// 对 429（速率限制）和 5xx 服务器错误进行重试
//
// 参数：
// - statusCode: HTTP 响应状态码
//
// 返回：
// - bool: true 表示应该重试
func shouldRetry(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests ||
		statusCode >= 500
}

// DoRequestWithRetry 带重试的 HTTP 请求
// 最大重试 3 次，延迟递增（1s, 2s, 3s）
//
// 参数：
// - client: HTTP 客户端
// - req: HTTP 请求
//
// 返回：
// - *http.Response: HTTP 响应
// - error: 请求错误
func DoRequestWithRetry(client *http.Client, req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error

	for i := range maxRetries {
		// 关闭前一次响应的 Body（如果有）
		if i > 0 && resp != nil {
			resp.Body.Close()
		}

		resp, err = client.Do(req)
		if err == nil {
			// 成功响应（200）则跳出
			if resp.StatusCode == http.StatusOK {
				break
			}
			// 不应该重试的状态码则直接跳出
			if !shouldRetry(resp.StatusCode) {
				break
			}
		}

		// 重试前等待递增延迟
		if i < maxRetries-1 {
			if err = sleepWithCtx(req.Context(), retryDelayUnit*time.Duration(i+1)); err != nil {
				if resp != nil {
					resp.Body.Close()
				}
				return nil, fmt.Errorf("failed to sleep: %w", err)
			}
		}
	}
	return resp, err
}

// sleepWithCtx 带上下文控制的睡眠
// 如果上下文取消，立即返回错误
//
// 参数：
// - ctx: 上下文
// - d: 睡眠时长
//
// 返回：
// - error: 上下文错误（如果取消）
func sleepWithCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
