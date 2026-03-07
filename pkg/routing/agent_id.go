// Package routing 提供消息路由功能
// 本文件实现 Agent ID 和 Account ID 的标准化
package routing

import (
	"regexp"
	"strings"
)

const (
	DefaultAgentID   = "main"                // 默认 Agent ID
	DefaultMainKey   = "main"                // 默认主键
	DefaultAccountID = "default"             // 默认账户 ID
	MaxAgentIDLength = 64                    // 最大 Agent ID 长度
)

var (
	// 有效 ID 正则表达式：[a-z0-9][a-z0-9_-]{0,63}
	validIDRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	// 无效字符替换正则
	invalidCharsRe = regexp.MustCompile(`[^a-z0-9_-]+`)
	// 前导短横线正则
	leadingDashRe  = regexp.MustCompile(`^-+`)
	// 尾部短横线正则
	trailingDashRe = regexp.MustCompile(`-+$`)
)

// NormalizeAgentID 标准化 Agent ID
// 格式要求：[a-z0-9][a-z0-9_-]{0,63}
// 无效字符替换为 "-"，移除前导/尾部短横线
//
// 参数：
// - id: 原始 ID
//
// 返回：
// - string: 标准化后的 ID（空输入返回 "main"）
func NormalizeAgentID(id string) string {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return DefaultAgentID
	}
	lower := strings.ToLower(trimmed)
	if validIDRe.MatchString(lower) {
		return lower
	}
	// 无效字符替换为短横线
	result := invalidCharsRe.ReplaceAllString(lower, "-")
	result = leadingDashRe.ReplaceAllString(result, "")
	result = trailingDashRe.ReplaceAllString(result, "")
	if len(result) > MaxAgentIDLength {
		result = result[:MaxAgentIDLength]
	}
	if result == "" {
		return DefaultAgentID
	}
	return result
}

// NormalizeAccountID 标准化账户 ID
// 空输入返回 DefaultAccountID
//
// 参数：
// - id: 原始账户 ID
//
// 返回：
// - string: 标准化后的账户 ID
func NormalizeAccountID(id string) string {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return DefaultAccountID
	}
	lower := strings.ToLower(trimmed)
	if validIDRe.MatchString(lower) {
		return lower
	}
	result := invalidCharsRe.ReplaceAllString(lower, "-")
	result = leadingDashRe.ReplaceAllString(result, "")
	result = trailingDashRe.ReplaceAllString(result, "")
	if len(result) > MaxAgentIDLength {
		result = result[:MaxAgentIDLength]
	}
	if result == "" {
		return DefaultAccountID
	}
	return result
}
