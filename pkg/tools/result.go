// Package tools 提供 AI 工具的实现
// 本文件实现工具执行结果结构（ToolResult）
package tools

import "encoding/json"

// ToolResult 工具执行结果结构
// 提供清晰的语义区分不同类型的结果，支持异步操作、用户消息和错误处理
//
// 字段说明：
// - ForLLM: 发送给 LLM 的上下文内容（必需）
// - ForUser: 直接发送给用户的内容（可选）
// - Silent: 是否静默（不发送用户消息）
// - IsError: 是否表示错误
// - Async: 是否异步执行
// - Err: 底层错误（不 JSON 序列化）
// - Media: 工具产生的媒体引用列表
type ToolResult struct {
	// ForLLM 是发送给 LLM 的内容，用于上下文
	// 所有结果都必须有这个字段
	ForLLM string `json:"for_llm"`

	// ForUser 是直接发送给用户的内容
	// 如果为空，不发送用户消息
	// Silent=true 时会忽略这个字段
	ForUser string `json:"for_user,omitempty"`

	// Silent 静默标志，为 true 时不发送任何用户消息
	// 即使用户消息已设置也会被忽略
	Silent bool `json:"silent"`

	// IsError 表示工具执行是否失败
	// 为 true 时应作为错误处理
	IsError bool `json:"is_error"`

	// Async 表示工具是否异步运行
	// 为 true 时工具将稍后完成并通过回调通知
	Async bool `json:"async"`

	// Err 是底层错误（不 JSON 序列化）
	// 用于内部错误处理和日志记录
	Err error `json:"-"`

	// Media 包含此工具产生的媒体存储引用
	// 如果非空，agent 将发布为 OutboundMediaMessage
	Media []string `json:"media,omitempty"`
}

// NewToolResult 创建基本的工具结果
//
// 参数：
// - forLLM: 发送给 LLM 的内容
//
// 返回：
// - *ToolResult: 工具结果
//
// 示例：
// result := NewToolResult("File updated successfully")
func NewToolResult(forLLM string) *ToolResult {
	return &ToolResult{
		ForLLM: forLLM,
	}
}

// SilentResult creates a ToolResult that is silent (no user message).
// The content is only sent to the LLM for context.
//
// Use this for operations that should not spam the user, such as:
// - File reads/writes
// - Status updates
// - Background operations
//
// Example:
//
//	result := SilentResult("Config file saved")
func SilentResult(forLLM string) *ToolResult {
	return &ToolResult{
		ForLLM:  forLLM,
		Silent:  true,
		IsError: false,
		Async:   false,
	}
}

// AsyncResult creates a ToolResult for async operations.
// The task will run in the background and complete later.
//
// Use this for long-running operations like:
// - Subagent spawns
// - Background processing
// - External API calls with callbacks
//
// Example:
//
//	result := AsyncResult("Subagent spawned, will report back")
func AsyncResult(forLLM string) *ToolResult {
	return &ToolResult{
		ForLLM:  forLLM,
		Silent:  false,
		IsError: false,
		Async:   true,
	}
}

// ErrorResult creates a ToolResult representing an error.
// Sets IsError=true and includes the error message.
//
// Example:
//
//	result := ErrorResult("Failed to connect to database: connection refused")
func ErrorResult(message string) *ToolResult {
	return &ToolResult{
		ForLLM:  message,
		Silent:  false,
		IsError: true,
		Async:   false,
	}
}

// UserResult creates a ToolResult with content for both LLM and user.
// Both ForLLM and ForUser are set to the same content.
//
// Use this when the user needs to see the result directly:
// - Command execution output
// - Fetched web content
// - Query results
//
// Example:
//
//	result := UserResult("Total files found: 42")
func UserResult(content string) *ToolResult {
	return &ToolResult{
		ForLLM:  content,
		ForUser: content,
		Silent:  false,
		IsError: false,
		Async:   false,
	}
}

// MediaResult creates a ToolResult with media refs for the user.
// The agent will publish these refs as OutboundMediaMessage.
//
// Example:
//
//	result := MediaResult("Image generated successfully", []string{"media://abc123"})
func MediaResult(forLLM string, mediaRefs []string) *ToolResult {
	return &ToolResult{
		ForLLM: forLLM,
		Media:  mediaRefs,
	}
}

// MarshalJSON implements custom JSON serialization.
// The Err field is excluded from JSON output via the json:"-" tag.
func (tr *ToolResult) MarshalJSON() ([]byte, error) {
	type Alias ToolResult
	return json.Marshal(&struct {
		*Alias
	}{
		Alias: (*Alias)(tr),
	})
}

// WithError sets the Err field and returns the result for chaining.
// This preserves the error for logging while keeping it out of JSON.
//
// Example:
//
//	result := ErrorResult("Operation failed").WithError(err)
func (tr *ToolResult) WithError(err error) *ToolResult {
	tr.Err = err
	return tr
}
