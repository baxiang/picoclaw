// Package utils 提供通用工具函数
// 本文件包含技能系统工具
package utils

import (
	"fmt"
	"strings"
)

// ValidateSkillIdentifier 验证技能标识符（slug 或注册表名称）
// 检查非空且不包含路径分隔符（"/"、"\"）或 ".."（防止目录遍历攻击）
//
// 参数：
// - identifier: 技能标识符
//
// 返回：
// - error: 验证错误（如果无效）
func ValidateSkillIdentifier(identifier string) error {
	trimmed := strings.TrimSpace(identifier)
	if trimmed == "" {
		return fmt.Errorf("identifier is required and must be a non-empty string")
	}
	if strings.ContainsAny(trimmed, "/\\") || strings.Contains(trimmed, "..") {
		return fmt.Errorf("identifier must not contain path separators or '..' to prevent directory traversal")
	}
	return nil
}
