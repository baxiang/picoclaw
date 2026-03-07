// Package bus 提供消息总线功能
// 用于在 Agent、Channels 和 Tools 之间传递消息
// 支持入站消息（Inbound）和出站消息（Outbound）的异步处理
package bus

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// ErrBusClosed 当向已关闭的 MessageBus 发布消息时返回此错误
var ErrBusClosed = errors.New("message bus closed")

// defaultBusBufferSize 默认消息总线缓冲区大小（64 条消息）
const defaultBusBufferSize = 64

// MessageBus 消息总线
// 提供异步消息传递机制，用于解耦消息生产者和消费者
// 支持三种消息类型：
// - InboundMessage: 入站消息（从渠道到 Agent）
// - OutboundMessage: 出站消息（从 Agent 到渠道）
// - OutboundMediaMessage: 出站媒体消息（图片、音频、视频等）
//
// 字段说明：
// - inbound: 入站消息通道
// - outbound: 出站消息通道
// - outboundMedia: 出站媒体消息通道
// - done: 完成信号通道，用于优雅关闭
// - closed: 原子布尔值，标记总线是否已关闭
type MessageBus struct {
	inbound       chan InboundMessage
	outbound      chan OutboundMessage
	outboundMedia chan OutboundMediaMessage
	done          chan struct{}
	closed        atomic.Bool
}

// NewMessageBus 创建新的消息总线
// 使用默认缓冲区大小（64 条消息）
//
// 返回：
// - 初始化好的 MessageBus 指针
func NewMessageBus() *MessageBus {
	return &MessageBus{
		inbound:       make(chan InboundMessage, defaultBusBufferSize),
		outbound:      make(chan OutboundMessage, defaultBusBufferSize),
		outboundMedia: make(chan OutboundMediaMessage, defaultBusBufferSize),
		done:          make(chan struct{}),
	}
}

// PublishInbound 发布入站消息
// 将消息发送到 inbound 通道，供 Agent 消费
//
// 参数：
// - ctx: 上下文用于取消控制
// - msg: 入站消息
//
// 返回：
// - error: 发送错误（总线关闭或上下文取消）
func (mb *MessageBus) PublishInbound(ctx context.Context, msg InboundMessage) error {
	if mb.closed.Load() {
		return ErrBusClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case mb.inbound <- msg:
		return nil
	case <-mb.done:
		return ErrBusClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ConsumeInbound 消费入站消息
// 从 inbound 通道接收消息，阻塞直到有消息或通道关闭
//
// 参数：
// - ctx: 上下文用于取消控制
//
// 返回：
// - InboundMessage: 入站消息
// - bool: 是否成功接收（false 表示通道关闭或上下文取消）
func (mb *MessageBus) ConsumeInbound(ctx context.Context) (InboundMessage, bool) {
	select {
	case msg, ok := <-mb.inbound:
		return msg, ok
	case <-mb.done:
		return InboundMessage{}, false
	case <-ctx.Done():
		return InboundMessage{}, false
	}
}

// PublishOutbound 发布出站消息
// 将消息发送到 outbound 通道，供渠道发送给用户
//
// 参数：
// - ctx: 上下文用于取消控制
// - msg: 出站消息
//
// 返回：
// - error: 发送错误（总线关闭或上下文取消）
func (mb *MessageBus) PublishOutbound(ctx context.Context, msg OutboundMessage) error {
	if mb.closed.Load() {
		return ErrBusClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case mb.outbound <- msg:
		return nil
	case <-mb.done:
		return ErrBusClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// PublishOutboundMedia 发布出站媒体消息
// 将媒体消息发送到 outboundMedia 通道，供渠道发送给用户
//
// 参数：
// - ctx: 上下文用于取消控制
// - msg: 出站媒体消息
//
// 返回：
// - error: 发送错误（总线关闭或上下文取消）
func (mb *MessageBus) PublishOutboundMedia(ctx context.Context, msg OutboundMediaMessage) error {
	if mb.closed.Load() {
		return ErrBusClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case mb.outboundMedia <- msg:
		return nil
	case <-mb.done:
		return ErrBusClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SubscribeOutboundMedia 订阅出站媒体消息
// 从 outboundMedia 通道接收媒体消息
//
// 参数：
// - ctx: 上下文用于取消控制
//
// 返回：
// - OutboundMediaMessage: 出站媒体消息
// - bool: 是否成功接收
func (mb *MessageBus) SubscribeOutboundMedia(ctx context.Context) (OutboundMediaMessage, bool) {
	select {
	case msg, ok := <-mb.outboundMedia:
		return msg, ok
	case <-mb.done:
		return OutboundMediaMessage{}, false
	case <-ctx.Done():
		return OutboundMediaMessage{}, false
	}
}

// Close 关闭消息总线
// 优雅地关闭总线，排空缓冲区中的消息
// 注意：通道不会被关闭，以避免并发发布时的 panic
func (mb *MessageBus) Close() {
	if mb.closed.CompareAndSwap(false, true) {
		close(mb.done)

		// 排空缓冲通道，避免消息静默丢失
		// 通道不会被关闭，以避免并发发布时的 send-on-closed panic
		drained := 0
		for {
			select {
			case <-mb.inbound:
				drained++
			default:
				goto doneInbound
			}
		}
	doneInbound:
		for {
			select {
			case <-mb.outbound:
				drained++
			default:
				goto doneOutbound
			}
		}
	doneOutbound:
		for {
			select {
			case <-mb.outboundMedia:
				drained++
			default:
				goto doneMedia
			}
		}
	doneMedia:
		if drained > 0 {
			logger.DebugCF("bus", "Drained buffered messages during close", map[string]any{
				"count": drained,
			})
		}
	}
}
