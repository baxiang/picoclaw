// Package memory 提供记忆存储功能
// 本文件定义会话持久化存储的接口
// 支持多种后端实现（如 JSONL 文件）

package memory

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// Store 持久化存储接口
// 定义会话存储的原子操作接口
// 每个方法都是原子操作，不需要单独的 Save() 调用
type Store interface {
	// AddMessage 添加简单文本消息到会话
	// 参数：
	// - ctx: 上下文用于取消控制
	// - sessionKey: 会话标识符
	// - role: 消息角色（user/assistant/tool/system）
	// - content: 消息内容
	// 返回：
	// - error: 添加错误
	AddMessage(ctx context.Context, sessionKey, role, content string) error

	// AddFullMessage 添加完整消息到会话（包括工具调用等）
	// 用于保存完整的对话流程
	// 参数：
	// - ctx: 上下文用于取消控制
	// - sessionKey: 会话标识符
	// - msg: 完整消息对象
	// 返回：
	// - error: 添加错误
	AddFullMessage(ctx context.Context, sessionKey string, msg providers.Message) error

	// GetHistory 返回会话的所有消息（按插入顺序）
	// 如果会话不存在，返回空切片（而非 nil）
	// 参数：
	// - ctx: 上下文用于取消控制
	// - sessionKey: 会话标识符
	// 返回：
	// - []providers.Message: 历史消息列表
	// - error: 获取错误
	GetHistory(ctx context.Context, sessionKey string) ([]providers.Message, error)

	// GetSummary 返回会话的对话摘要
	// 如果没有摘要则返回空字符串
	// 参数：
	// - ctx: 上下文用于取消控制
	// - sessionKey: 会话标识符
	// 返回：
	// - string: 对话摘要
	// - error: 获取错误
	GetSummary(ctx context.Context, sessionKey string) (string, error)

	// SetSummary 更新会话的对话摘要
	// 参数：
	// - ctx: 上下文用于取消控制
	// - sessionKey: 会话标识符
	// - summary: 摘要内容
	// 返回：
	// - error: 设置错误
	SetSummary(ctx context.Context, sessionKey, summary string) error

	// TruncateHistory 删除会话中除最后 keepLast 条消息外的所有消息
	// 如果 keepLast <= 0，删除所有消息
	// 参数：
	// - ctx: 上下文用于取消控制
	// - sessionKey: 会话标识符
	// - keepLast: 要保留的消息数量
	// 返回：
	// - error: 截断错误
	TruncateHistory(ctx context.Context, sessionKey string, keepLast int) error

	// SetHistory 用提供的历史消息替换会话中的所有消息
	// 用于批量更新或恢复会话历史
	// 参数：
	// - ctx: 上下文用于取消控制
	// - sessionKey: 会话标识符
	// - history: 历史消息列表
	// 返回：
	// - error: 设置错误
	SetHistory(ctx context.Context, sessionKey string, history []providers.Message) error

	// Compact 通过物理删除逻辑上已截断的数据来回收存储空间
	// 不累积无效数据的后端可以返回 nil
	// 参数：
	// - ctx: 上下文用于取消控制
	// - sessionKey: 会话标识符
	// 返回：
	// - error: 压缩错误
	Compact(ctx context.Context, sessionKey string) error

	// Close 释放存储持有的任何资源
	// 返回：
	// - error: 关闭错误
	Close() error
}
