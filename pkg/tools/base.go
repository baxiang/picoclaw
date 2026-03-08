// Package tools 提供 AI 工具的实现
// 本文件定义工具接口和上下文辅助函数
package tools

import "context"

// Tool 工具接口
// 所有工具都必须实现此接口
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	Execute(ctx context.Context, args map[string]any) *ToolResult
}

// --- 请求范围的工具上下文（渠道/chatID）---
//
// 通过 context.Value 传递，因此并发工具调用各自接收
// 不可变的副本 - 单例工具实例上没有可变状态
//
// 键是未导出的指针类型变量 - 保证无冲突，
// 只能通过下面的辅助函数访问

type toolCtxKey struct{ name string }

var (
	ctxKeyChannel = &toolCtxKey{"channel"}
	ctxKeyChatID  = &toolCtxKey{"chatID"}
)

// WithToolContext 返回携带 channel 和 chatID 的子上下文
//
// 参数：
// - ctx: 父上下文
// - channel: 渠道名称
// - chatID: 聊天 ID
//
// 返回：
// - context.Context: 子上下文
func WithToolContext(ctx context.Context, channel, chatID string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyChannel, channel)
	ctx = context.WithValue(ctx, ctxKeyChatID, chatID)
	return ctx
}

// ToolChannel extracts the channel from ctx, or "" if unset.
func ToolChannel(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyChannel).(string)
	return v
}

// ToolChatID extracts the chatID from ctx, or "" if unset.
func ToolChatID(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyChatID).(string)
	return v
}

// AsyncCallback is a function type that async tools use to notify completion.
// When an async tool finishes its work, it calls this callback with the result.
//
// The ctx parameter allows the callback to be canceled if the agent is shutting down.
// The result parameter contains the tool's execution result.
type AsyncCallback func(ctx context.Context, result *ToolResult)

// AsyncExecutor is an optional interface that tools can implement to support
// asynchronous execution with completion callbacks.
//
// Unlike the old AsyncTool pattern (SetCallback + Execute), AsyncExecutor
// receives the callback as a parameter of ExecuteAsync. This eliminates the
// data race where concurrent calls could overwrite each other's callbacks
// on a shared tool instance.
//
// This is useful for:
//   - Long-running operations that shouldn't block the agent loop
//   - Subagent spawns that complete independently
//   - Background tasks that need to report results later
//
// Example:
//
//	func (t *SpawnTool) ExecuteAsync(ctx context.Context, args map[string]any, cb AsyncCallback) *ToolResult {
//	    go func() {
//	        result := t.runSubagent(ctx, args)
//	        if cb != nil { cb(ctx, result) }
//	    }()
//	    return AsyncResult("Subagent spawned, will report back")
//	}
type AsyncExecutor interface {
	Tool
	// ExecuteAsync runs the tool asynchronously. The callback cb will be
	// invoked (possibly from another goroutine) when the async operation
	// completes. cb is guaranteed to be non-nil by the caller (registry).
	ExecuteAsync(ctx context.Context, args map[string]any, cb AsyncCallback) *ToolResult
}

func ToolToSchema(tool Tool) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        tool.Name(),
			"description": tool.Description(),
			"parameters":  tool.Parameters(),
		},
	}
}
