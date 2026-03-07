// Package tools 提供 AI 工具的实现
// 本文件包含工具注册表（ToolRegistry）的实现

package tools

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"   // 日志系统
	"github.com/sipeed/picoclaw/pkg/providers" // LLM 提供商接口
)

// ToolRegistry 工具注册表
// 管理所有可用工具的注册、查询和执行
//
// 字段说明：
// - tools: 工具映射表（name -> Tool）
// - mu: 读写锁，保护并发访问
type ToolRegistry struct {
	tools map[string]Tool
	mu    sync.RWMutex
}

// NewToolRegistry 创建一个新的工具注册表
//
// 返回：
// - 初始化好的 ToolRegistry 指针
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]Tool),
	}
}

// Register 注册一个工具
// 如果同名的工具已存在，会记录警告并覆盖
//
// 参数：
// - tool: 要实现的工具接口
func (r *ToolRegistry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		logger.WarnCF("tools", "Tool registration overwrites existing tool",
			map[string]any{"name": name})
	}
	r.tools[name] = tool
}

// Get 根据名称获取工具
//
// 参数：
// - name: 工具名称
//
// 返回：
// - Tool: 工具接口
// - bool: 是否找到
func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

// Execute 执行工具
// 不带上下文信息的简化版本
//
// 参数：
// - ctx: 上下文用于取消控制
// - name: 工具名称
// - args: 工具参数
//
// 返回：
// - ToolResult: 工具执行结果
func (r *ToolRegistry) Execute(ctx context.Context, name string, args map[string]any) *ToolResult {
	return r.ExecuteWithContext(ctx, name, args, "", "", nil)
}

// ExecuteWithContext 执行工具（带上下文信息和异步回调）
// 如果工具实现了 AsyncExecutor 接口且提供了回调，使用 ExecuteAsync
// 否则使用同步的 Execute
//
// 参数：
// - ctx: 上下文用于取消控制
// - name: 工具名称
// - args: 工具参数
// - channel: 渠道名称（如 telegram, discord）
// - chatID: 聊天标识符
// - asyncCallback: 异步回调函数（可选）
//
// 返回：
// - ToolResult: 工具执行结果
func (r *ToolRegistry) ExecuteWithContext(
	ctx context.Context,
	name string,
	args map[string]any,
	channel, chatID string,
	asyncCallback AsyncCallback,
) *ToolResult {
	logger.InfoCF("tool", "Tool execution started",
		map[string]any{
			"tool": name,
			"args": args,
		})

	// 获取工具
	tool, ok := r.Get(name)
	if !ok {
		logger.ErrorCF("tool", "Tool not found",
			map[string]any{
				"tool": name,
			})
		return ErrorResult(fmt.Sprintf("tool %q not found", name)).WithError(fmt.Errorf("tool not found"))
	}

	// 注入渠道和聊天 ID 到上下文
	// 工具通过 ToolChannel(ctx)/ToolChatID(ctx) 读取这些信息
	ctx = WithToolContext(ctx, channel, chatID)

	// 如果工具实现了 AsyncExecutor 且提供了回调，使用异步执行
	var result *ToolResult
	start := time.Now()
	if asyncExec, ok := tool.(AsyncExecutor); ok && asyncCallback != nil {
		logger.DebugCF("tool", "Executing async tool via ExecuteAsync",
			map[string]any{
				"tool": name,
			})
		result = asyncExec.ExecuteAsync(ctx, args, asyncCallback)
	} else {
		// 同步执行
		result = tool.Execute(ctx, args)
	}
	duration := time.Since(start)

	// 根据结果类型记录日志
	if result.IsError {
		logger.ErrorCF("tool", "Tool execution failed",
			map[string]any{
				"tool":     name,
				"duration": duration.Milliseconds(),
				"error":    result.ForLLM,
			})
	} else if result.Async {
		logger.InfoCF("tool", "Tool started (async)",
			map[string]any{
				"tool":     name,
				"duration": duration.Milliseconds(),
			})
	} else {
		logger.InfoCF("tool", "Tool execution completed",
			map[string]any{
				"tool":          name,
				"duration_ms":   duration.Milliseconds(),
				"result_length": len(result.ForLLM),
			})
	}

	return result
}

// sortedToolNames 返回排序后的工具名称列表
// 这对于 KV 缓存稳定性至关重要：非确定性的 map 遍历
// 会在每次调用时产生不同的系统提示和工具定义，
// 即使工具没有变化也会使 LLM 的前缀缓存失效
//
// 返回：
// - 排序后的工具名称列表
func (r *ToolRegistry) sortedToolNames() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GetDefinitions 获取所有工具的定义（JSON Schema 格式）
// 按工具名称排序，确保确定性输出
//
// 返回：
// - 工具定义列表（map 格式）
func (r *ToolRegistry) GetDefinitions() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sorted := r.sortedToolNames()
	definitions := make([]map[string]any, 0, len(sorted))
	for _, name := range sorted {
		definitions = append(definitions, ToolToSchema(r.tools[name]))
	}
	return definitions
}

// ToProviderDefs 将工具定义转换为提供商兼容的格式
// 这是 LLM 提供商 API 期望的格式
//
// 返回：
// - providers.ToolDefinition 列表
func (r *ToolRegistry) ToProviderDefs() []providers.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sorted := r.sortedToolNames()
	definitions := make([]providers.ToolDefinition, 0, len(sorted))
	for _, name := range sorted {
		tool := r.tools[name]
		schema := ToolToSchema(tool)

		// 安全地提取嵌套值（带类型检查）
		fn, ok := schema["function"].(map[string]any)
		if !ok {
			continue
		}

		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		params, _ := fn["parameters"].(map[string]any)

		definitions = append(definitions, providers.ToolDefinition{
			Type: "function",
			Function: providers.ToolFunctionDefinition{
				Name:        name,
				Description: desc,
				Parameters:  params,
			},
		})
	}
	return definitions
}

// List 返回所有已注册工具的有序名称列表
//
// 返回：
// - 工具名称列表
func (r *ToolRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.sortedToolNames()
}

// Count 返回已注册工具的数量
//
// 返回：
// - 工具数量
func (r *ToolRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// GetSummaries 返回所有已注册工具的人类可读摘要
// 格式："- `name` - description"
//
// 返回：
// - 工具摘要列表
func (r *ToolRegistry) GetSummaries() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sorted := r.sortedToolNames()
	summaries := make([]string, 0, len(sorted))
	for _, name := range sorted {
		tool := r.tools[name]
		summaries = append(summaries, fmt.Sprintf("- `%s` - %s", tool.Name(), tool.Description()))
	}
	return summaries
}
