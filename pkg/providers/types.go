// Package providers 提供 LLM（大语言模型）提供商的接口定义和实现
// 支持多种 LLM 提供商：
// - HTTP 基础：OpenAI、Anthropic、Gemini、Groq 等
// - CLI 基础：Claude CLI、Codex CLI、GitHub Copilot
//
// 核心功能：
// - 统一的 LLMProvider 接口
// - 降级链（FallbackChain）处理提供商故障
// - 冷却机制（CooldownTracker）处理速率限制
// - 错误分类（Error Classifier）用于降级决策
package providers

import (
	"context"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes" // 协议层类型定义
)

// 类型别名：从 protocoltypes 包导入的核心类型
// 这样可以在不改变外部调用的情况下重构内部包结构
type (
	// ToolCall LLM 返回的工具调用请求
	ToolCall = protocoltypes.ToolCall
	// FunctionCall 工具调用的函数信息
	FunctionCall = protocoltypes.FunctionCall
	// LLMResponse LLM 响应结构
	LLMResponse = protocoltypes.LLMResponse
	// UsageInfo 令牌使用统计
	UsageInfo = protocoltypes.UsageInfo
	// Message 对话消息结构
	Message = protocoltypes.Message
	// ToolDefinition 工具定义（用于注册到 LLM）
	ToolDefinition = protocoltypes.ToolDefinition
	// ToolFunctionDefinition 工具函数定义
	ToolFunctionDefinition = protocoltypes.ToolFunctionDefinition
	// ExtraContent 额外内容块（提供商特定）
	ExtraContent = protocoltypes.ExtraContent
	// GoogleExtra Google 提供商特定内容
	GoogleExtra = protocoltypes.GoogleExtra
	// ContentBlock 内容块（支持文本、图像等）
	ContentBlock = protocoltypes.ContentBlock
	// CacheControl 缓存控制（用于 Anthropic 等提供商的提示缓存）
	CacheControl = protocoltypes.CacheControl
)

// LLMProvider LLM 提供商接口
// 所有 LLM 提供商实现必须实现此接口
//
// 方法：
// - Chat: 执行聊天补全，支持工具调用
// - GetDefaultModel: 获取默认模型名称
type LLMProvider interface {
	// Chat 执行聊天补全请求
	// 参数：
	// - ctx: 上下文用于取消控制
	// - messages: 对话消息列表
	// - tools: 可用工具定义列表
	// - model: 模型名称
	// - options: 额外选项（温度、最大令牌等）
	// 返回：
	// - LLMResponse: LLM 响应
	// - error: 错误信息
	Chat(
		ctx context.Context,
		messages []Message,
		tools []ToolDefinition,
		model string,
		options map[string]any,
	) (*LLMResponse, error)
	// GetDefaultModel 获取此提供商的默认模型
	GetDefaultModel() string
}

// StatefulProvider 有状态的提供商接口
// 扩展 LLMProvider，添加 Close 方法用于资源清理
// 用于需要管理长期连接的提供商（如 gRPC、stdio）
type StatefulProvider interface {
	LLMProvider
	// Close 关闭提供商，释放资源
	Close()
}

// ThinkingCapable 支持扩展思考的提供商接口（可选）
// 用于 Anthropic 等支持"思考"功能的提供商
// agent 循环使用此接口来警告当配置了 thinking_level 但当前提供商不支持时
type ThinkingCapable interface {
	// SupportsThinking 返回是否支持思考功能
	SupportsThinking() bool
}

// FailoverReason 分类 LLM 请求失败的原因，用于降级决策
type FailoverReason string

const (
	// FailoverAuth 认证失败（API key 无效、过期等）
	FailoverAuth FailoverReason = "auth"
	// FailoverRateLimit 速率限制（请求过多）
	FailoverRateLimit FailoverReason = "rate_limit"
	// FailoverBilling 计费问题（余额不足、订阅过期等）
	FailoverBilling FailoverReason = "billing"
	// FailoverTimeout 超时（请求超过时限）
	FailoverTimeout FailoverReason = "timeout"
	// FailoverFormat 格式错误（请求格式不正确、图像尺寸问题等）
	FailoverFormat FailoverReason = "format"
	// FailoverOverloaded 服务器过载
	FailoverOverloaded FailoverReason = "overloaded"
	// FailoverUnknown 未知错误
	FailoverUnknown FailoverReason = "unknown"
)

// FailoverError 封装 LLM 提供商错误，带分类元数据
type FailoverError struct {
	Reason   FailoverReason // 失败原因分类
	Provider string         // 提供商名称
	Model    string         // 模型名称
	Status   int            // HTTP 状态码（如果适用）
	Wrapped  error          // 原始错误
}

// Error 实现 error 接口
func (e *FailoverError) Error() string {
	return fmt.Sprintf("failover(%s): provider=%s model=%s status=%d: %v",
		e.Reason, e.Provider, e.Model, e.Status, e.Wrapped)
}

// Unwrap 实现 errors.Wrapper 接口，支持 errors.Unwrap
func (e *FailoverError) Unwrap() error {
	return e.Wrapped
}

// IsRetriable 返回此错误是否应该触发降级到下一个候选
// 不可降级的错误：格式错误（请求结构问题、图像尺寸/尺寸问题）
func (e *FailoverError) IsRetriable() bool {
	return e.Reason != FailoverFormat
}

// ModelConfig 保存主模型和降级列表
type ModelConfig struct {
	// Primary 主模型名称
	Primary string
	// Fallbacks 降级模型列表
	Fallbacks []string
}
