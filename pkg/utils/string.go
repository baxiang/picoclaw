// Package utils 提供通用工具函数
// 本文件包含字符串处理工具
package utils

import (
	"strings"
	"unicode"
)

// SanitizeMessageContent 清理消息内容中的 Unicode 控制字符
// 移除可能混淆 LLM 或导致显示问题的字符：
// - 控制字符（Cc）
// - 格式字符（Cf，如 RTL 覆盖、零宽字符）
// - 其他非图形字符
//
// 参数：
// - input: 输入字符串
//
// 返回：
// - string: 清理后的字符串
func SanitizeMessageContent(input string) string {
	var sb strings.Builder
	// 预分配内存以避免多次分配
	sb.Grow(len(input))

	for _, r := range input {
		// unicode.IsGraphic 返回 true 如果 rune 是 Unicode 图形字符
		// 包括字母、标记、数字、标点、符号
		// 不包括控制字符（Cc）、格式字符（Cf）、代理（Cs）、私有区（Co）
		if unicode.IsGraphic(r) || r == '\n' || r == '\r' || r == '\t' {
			sb.WriteRune(r)
		}
	}

	return sb.String()
}

// Truncate 截断字符串到最大 rune 数
// 正确处理多字节 Unicode 字符
// 如果字符串被截断，追加 "..." 表示
//
// 参数：
// - s: 输入字符串
// - maxLen: 最大长度（rune 数）
//
// 返回：
// - string: 截断后的字符串
func Truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	// 保留 3 个字符用于 "..."
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

// DerefStr 解引用字符串指针
// 如果指针为 nil，返回 fallback 值
//
// 参数：
// - s: 字符串指针（可能为 nil）
// - fallback: 回退值（当指针为 nil 时返回）
//
// 返回：
// - string: 解引用后的值或 fallback
func DerefStr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}
