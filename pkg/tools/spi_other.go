//go:build !linux

// Package tools 提供 AI 工具的实现
// 本文件是 SPI 工具的非 Linux 平台存根实现
package tools

// transfer SPI 传输（非 Linux 平台存根）
func (t *SPITool) transfer(args map[string]any) *ToolResult {
	return ErrorResult("SPI is only supported on Linux")
}

// readDevice is a stub for non-Linux platforms.
func (t *SPITool) readDevice(args map[string]any) *ToolResult {
	return ErrorResult("SPI is only supported on Linux")
}
