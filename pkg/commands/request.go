package commands

import (
	"context"
	"strings"
)

// Handler 命令处理器函数类型
//
// 参数：
// - ctx: 上下文用于取消控制
// - req: 命令请求
// - rt: 运行时依赖
//
// 返回：
// - error: 执行错误（如果有）
type Handler func(ctx context.Context, req Request, rt *Runtime) error

// Request 命令请求结构
// 包含执行命令所需的所有上下文信息
type Request struct {
	Channel  string                 // 渠道名称
	ChatID   string                 // 聊天标识符
	SenderID string                 // 发送者 ID
	Text     string                 // 原始命令文本
	Reply    func(text string) error // 回复函数
}

const unavailableMsg = "Command unavailable in current context."

// commandPrefixes 支持的命令前缀列表
var commandPrefixes = []string{"/", "!"}

// parseCommandName 解析命令名称
// 接受 "/name"、"!name" 和 Telegram 的 "/name@bot" 格式
// 然后标准化为小写命令名称
//
// 参数：
// - input: 输入文本
//
// 返回：
// - string: 命令名称
// - bool: 是否成功解析
func parseCommandName(input string) (string, bool) {
	token := nthToken(input, 0)
	if token == "" {
		return "", false
	}

	name, ok := trimCommandPrefix(token)
	if !ok {
		return "", false
	}
	if i := strings.Index(name, "@"); i >= 0 {
		name = name[:i]
	}
	name = normalizeCommandName(name)
	if name == "" {
		return "", false
	}
	return name, true
}

func trimCommandPrefix(token string) (string, bool) {
	for _, prefix := range commandPrefixes {
		if strings.HasPrefix(token, prefix) {
			return strings.TrimPrefix(token, prefix), true
		}
	}
	return "", false
}

// HasCommandPrefix returns true if the input starts with a recognized
// command prefix (e.g. "/" or "!").
func HasCommandPrefix(input string) bool {
	token := nthToken(input, 0)
	if token == "" {
		return false
	}
	_, ok := trimCommandPrefix(token)
	return ok
}

// nthToken returns the 0-indexed token from whitespace-split input.
func nthToken(input string, n int) string {
	parts := strings.Fields(strings.TrimSpace(input))
	if n >= len(parts) {
		return ""
	}
	return parts[n]
}

func normalizeCommandName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
