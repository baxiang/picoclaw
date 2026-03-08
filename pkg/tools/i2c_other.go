//go:build !linux

// Package tools 提供 AI 工具的实现
// 本文件是 I2C 工具的非 Linux 平台存根实现
package tools

// scan 扫描 I2C 设备（非 Linux 平台存根）
func (t *I2CTool) scan(args map[string]any) *ToolResult {
	return ErrorResult("I2C is only supported on Linux")
}

// readDevice is a stub for non-Linux platforms.
func (t *I2CTool) readDevice(args map[string]any) *ToolResult {
	return ErrorResult("I2C is only supported on Linux")
}

// writeDevice is a stub for non-Linux platforms.
func (t *I2CTool) writeDevice(args map[string]any) *ToolResult {
	return ErrorResult("I2C is only supported on Linux")
}
