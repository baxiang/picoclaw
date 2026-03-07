// Package tools 提供 AI 工具的实现
// 包含各种工具：
// - 文件操作：read_file, write_file, edit_file, list_dir
// - Shell 执行：exec
// - Web 搜索：web_search, web_fetch
// - 技能管理：find_skills, install_skill
// - 消息发送：message, send_file
// - 子代理：spawn, subagent
// - 硬件接口：i2c, spi
// - MCP 工具：通过 MCP 协议扩展
package tools

import "context"

// Message 对话消息结构
// 用于工具与 LLM 之间的消息传递
type Message struct {
	Role       string     `json:"role"`                 // 角色：user/assistant/tool/system
	Content    string     `json:"content"`              // 消息内容
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"` // 工具调用列表（assistant 消息）
	ToolCallID string     `json:"tool_call_id,omitempty"` // 工具调用 ID（tool 消息，关联到具体的 tool_call）
}

// ToolCall 工具调用结构
// LLM 返回的工具调用请求
type ToolCall struct {
	ID        string         `json:"id"`                 // 工具调用唯一标识
	Type      string         `json:"type"`               // 类型：function
	Function  *FunctionCall  `json:"function,omitempty"` // 函数调用详情
	Name      string         `json:"name,omitempty"`     // 工具名称
	Arguments map[string]any `json:"arguments,omitempty"` // 函数参数
}

// FunctionCall 函数调用详情
type FunctionCall struct {
	Name      string `json:"name"`      // 函数名称
	Arguments string `json:"arguments"` // 函数参数（JSON 字符串）
}

// LLMResponse LLM 响应结构
type LLMResponse struct {
	Content      string     `json:"content"`              // 响应内容
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"` // 工具调用列表
	FinishReason string     `json:"finish_reason"`        // 结束原因：stop/tool_calls/length
	Usage        *UsageInfo `json:"usage,omitempty"`      // 令牌使用统计
}

// UsageInfo 令牌使用统计
type UsageInfo struct {
	PromptTokens     int `json:"prompt_tokens"`      // 输入令牌数
	CompletionTokens int `json:"completion_tokens"`  // 输出令牌数
	TotalTokens      int `json:"total_tokens"`       // 总令牌数
}

// LLMProvider LLM 提供商接口
// 工具模块使用此接口调用 LLM（如子代理功能）
type LLMProvider interface {
	// Chat 执行聊天补全
	Chat(
		ctx context.Context,
		messages []Message,
		tools []ToolDefinition,
		model string,
		options map[string]any,
	) (*LLMResponse, error)
	// GetDefaultModel 获取默认模型
	GetDefaultModel() string
}

// ToolDefinition 工具定义结构
// 用于向 LLM 描述可用工具
type ToolDefinition struct {
	Type     string                 `json:"type"`               // 类型：function
	Function ToolFunctionDefinition `json:"function"`           // 函数定义
}

// ToolFunctionDefinition 工具函数定义
type ToolFunctionDefinition struct {
	Name        string         `json:"name"`        // 函数名称
	Description string         `json:"description"` // 函数描述
	Parameters  map[string]any `json:"parameters"`  // 参数 schema（JSON Schema 格式）
}
