// Package auth 提供 OAuth 认证和凭证管理功能（本文件实现 Anthropic 使用量查询）
package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	anthropicBetaHeader = "oauth-2025-04-20" // Anthropic OAuth Beta 版本头
	anthropicAPIVersion = "2023-06-01"       // Anthropic API 版本
)

// anthropicUsageURL Anthropic OAuth 使用量统计端点
// 定义为变量而非常量，方便测试时覆盖
var anthropicUsageURL = "https://api.anthropic.com/api/oauth/usage"

// setAnthropicUsageURL 设置使用量端点 URL（仅用于测试）
func setAnthropicUsageURL(url string) { anthropicUsageURL = url }

// AnthropicUsage Anthropic OAuth 使用量统计
// 包含两个时间窗口的利用率数据
type AnthropicUsage struct {
	FiveHourUtilization  float64 // 5 小时利用率（0.0-1.0）
	SevenDayUtilization  float64 // 7 天利用率（0.0-1.0）
}

// FetchAnthropicUsage 获取 Anthropic OAuth 使用量统计
// 需要有效的 OAuth 访问令牌和相应的权限范围
//
// 参数：
// - token: OAuth 访问令牌
//
// 返回：
// - *AnthropicUsage: 使用量统计
// - error: 查询错误
func FetchAnthropicUsage(token string) (*AnthropicUsage, error) {
	// 创建 HTTP 请求
	req, err := http.NewRequest("GET", anthropicUsageURL, nil)
	if err != nil {
		return nil, err
	}
	// 设置请求头
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Anthropic-Version", anthropicAPIVersion)
	req.Header.Set("Anthropic-Beta", anthropicBetaHeader)

	// 发送请求
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading usage response: %w", err)
	}

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("insufficient scope: usage endpoint requires oauth scope")
		}
		return nil, fmt.Errorf("usage request failed (%d): %s", resp.StatusCode, string(body))
	}

	// 解析 JSON 响应
	var result struct {
		FiveHour struct {
			Utilization float64 `json:"utilization"`
		} `json:"five_hour"`
		SevenDay struct {
			Utilization float64 `json:"utilization"`
		} `json:"seven_day"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing usage response: %w", err)
	}

	return &AnthropicUsage{
		FiveHourUtilization:  result.FiveHour.Utilization,
		SevenDayUtilization:  result.SevenDay.Utilization,
	}, nil
}
