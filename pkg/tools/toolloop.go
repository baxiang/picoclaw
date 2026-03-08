// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

// Package tools 提供 AI 工具的实现
// 本文件实现工具循环执行逻辑
// 用于 main agent 和 subagent 重用核心 agent 逻辑
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// ToolLoopConfig 工具执行循环配置
type ToolLoopConfig struct {
	Provider      providers.LLMProvider // LLM 提供商
	Model         string                // 模型名称
	Tools         *ToolRegistry         // 工具注册表
	MaxIterations int                   // 最大迭代次数
	LLMOptions    map[string]any        // LLM 选项
}

// ToolLoopResult 工具循环执行结果
type ToolLoopResult struct {
	Content    string // 最终内容
	Iterations int    // 迭代次数
}

// RunToolLoop 执行 LLM + 工具调用迭代循环
// 这是核心 agent 逻辑，可被 main agent 和 subagent 重用
//
// 参数：
// - ctx: 上下文用于取消控制
// - config: 工具循环配置
// - messages: 消息列表
// - channel: 渠道名称
// - chatID: 聊天 ID
//
// 返回：
// - *ToolLoopResult: 循环执行结果
// - error: 执行错误
func RunToolLoop(
	iteration := 0
	var finalContent string

	for iteration < config.MaxIterations {
		iteration++

		logger.DebugCF("toolloop", "LLM iteration",
			map[string]any{
				"iteration": iteration,
				"max":       config.MaxIterations,
			})

		// 1. Build tool definitions
		var providerToolDefs []providers.ToolDefinition
		if config.Tools != nil {
			providerToolDefs = config.Tools.ToProviderDefs()
		}

		// 2. Set default LLM options
		llmOpts := config.LLMOptions
		if llmOpts == nil {
			llmOpts = map[string]any{}
		}
		// 3. Call LLM
		response, err := config.Provider.Chat(ctx, messages, providerToolDefs, config.Model, llmOpts)
		if err != nil {
			logger.ErrorCF("toolloop", "LLM call failed",
				map[string]any{
					"iteration": iteration,
					"error":     err.Error(),
				})
			return nil, fmt.Errorf("LLM call failed: %w", err)
		}

		// 4. If no tool calls, we're done
		if len(response.ToolCalls) == 0 {
			finalContent = response.Content
			logger.InfoCF("toolloop", "LLM response without tool calls (direct answer)",
				map[string]any{
					"iteration":     iteration,
					"content_chars": len(finalContent),
				})
			break
		}

		normalizedToolCalls := make([]providers.ToolCall, 0, len(response.ToolCalls))
		for _, tc := range response.ToolCalls {
			normalizedToolCalls = append(normalizedToolCalls, providers.NormalizeToolCall(tc))
		}

		// 5. Log tool calls
		toolNames := make([]string, 0, len(normalizedToolCalls))
		for _, tc := range normalizedToolCalls {
			toolNames = append(toolNames, tc.Name)
		}
		logger.InfoCF("toolloop", "LLM requested tool calls",
			map[string]any{
				"tools":     toolNames,
				"count":     len(normalizedToolCalls),
				"iteration": iteration,
			})

		// 6. Build assistant message with tool calls
		assistantMsg := providers.Message{
			Role:    "assistant",
			Content: response.Content,
		}
		for _, tc := range normalizedToolCalls {
			argumentsJSON, _ := json.Marshal(tc.Arguments)
			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, providers.ToolCall{
				ID:        tc.ID,
				Type:      "function",
				Name:      tc.Name,
				Arguments: tc.Arguments,
				Function: &providers.FunctionCall{
					Name:      tc.Name,
					Arguments: string(argumentsJSON),
				},
			})
		}
		messages = append(messages, assistantMsg)

		// 7. Execute tool calls in parallel
		type indexedResult struct {
			result *ToolResult
			tc     providers.ToolCall
		}

		results := make([]indexedResult, len(normalizedToolCalls))
		var wg sync.WaitGroup

		for i, tc := range normalizedToolCalls {
			results[i].tc = tc

			wg.Add(1)
			go func(idx int, tc providers.ToolCall) {
				defer wg.Done()

				argsJSON, _ := json.Marshal(tc.Arguments)
				argsPreview := utils.Truncate(string(argsJSON), 200)
				logger.InfoCF("toolloop", fmt.Sprintf("Tool call: %s(%s)", tc.Name, argsPreview),
					map[string]any{
						"tool":      tc.Name,
						"iteration": iteration,
					})

				var toolResult *ToolResult
				if config.Tools != nil {
					toolResult = config.Tools.ExecuteWithContext(ctx, tc.Name, tc.Arguments, channel, chatID, nil)
				} else {
					toolResult = ErrorResult("No tools available")
				}
				results[idx].result = toolResult
			}(i, tc)
		}
		wg.Wait()

		// Append results in original order
		for _, r := range results {
			contentForLLM := r.result.ForLLM
			if contentForLLM == "" && r.result.Err != nil {
				contentForLLM = r.result.Err.Error()
			}

			messages = append(messages, providers.Message{
				Role:       "tool",
				Content:    contentForLLM,
				ToolCallID: r.tc.ID,
			})
		}
	}

	return &ToolLoopResult{
		Content:    finalContent,
		Iterations: iteration,
	}, nil
}
