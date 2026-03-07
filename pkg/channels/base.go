// Package channels 提供通讯渠道功能
// 本文件包含渠道基础实现
// 支持多种通讯平台：Telegram、Discord、WhatsApp、微信等

package channels

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
)

var (
	uniqueIDCounter uint64 // 唯一 ID 计数器
	uniqueIDPrefix  string // 唯一 ID 前缀（随机生成）
)

func init() {
	// 从 crypto/rand 一次性读取随机数作为前缀（单次系统调用）
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 回退到基于时间的前缀
		binary.BigEndian.PutUint64(b[:], uint64(time.Now().UnixNano()))
	}
	uniqueIDPrefix = hex.EncodeToString(b[:])
}

// uniqueID 生成进程唯一 ID
// 使用随机前缀和原子计数器
// 此 ID 用于内部关联（如媒体作用域键），不是加密安全的
// 不得用于需要不可预测性的场景
func uniqueID() string {
	n := atomic.AddUint64(&uniqueIDCounter, 1)
	return uniqueIDPrefix + strconv.FormatUint(n, 16)
}

// Channel 渠道接口
// 所有通讯渠道必须实现此接口
type Channel interface {
	Name() string                              // 返回渠道名称
	Start(ctx context.Context) error           // 启动渠道
	Stop(ctx context.Context) error            // 停止渠道
	Send(ctx context.Context, msg bus.OutboundMessage) error // 发送消息
	IsRunning() bool                           // 是否正在运行
	IsAllowed(senderID string) bool            // 是否允许给定发送者
	IsAllowedSender(sender bus.SenderInfo) bool // 是否允许给定发送者（结构化）
	ReasoningChannelID() string                // 返回思考频道 ID
}

// BaseChannelOption 基础渠道的功能选项
type BaseChannelOption func(*BaseChannel)

// WithMaxMessageLength 设置渠道的最大消息长度（按字符计算）
// 超过此限制的消息将被 Manager 自动分割
// 值为 0 表示无限制
func WithMaxMessageLength(n int) BaseChannelOption {
	return func(c *BaseChannel) { c.maxMessageLength = n }
}

// WithGroupTrigger 设置渠道的群聊触发配置
func WithGroupTrigger(gt config.GroupTriggerConfig) BaseChannelOption {
	return func(c *BaseChannel) { c.groupTrigger = gt }
}

// WithReasoningChannelID 设置思考频道 ID，思考消息将发送到此频道
func WithReasoningChannelID(id string) BaseChannelOption {
	return func(c *BaseChannel) { c.reasoningChannelID = id }
}

// MessageLengthProvider 消息长度提供者接口
// 渠道可以实现此接口来宣告它们的最大消息长度
// Manager 通过类型断言使用此接口来决定是否分割出站消息
type MessageLengthProvider interface {
	MaxMessageLength() int
}

// BaseChannel 基础渠道结构
// 提供所有渠道共用的基础功能
type BaseChannel struct {
	config              any                   // 渠道配置
	bus                 *bus.MessageBus       // 消息总线
	running             atomic.Bool           // 运行状态
	name                string                // 渠道名称
	allowList           []string              // 允许列表
	maxMessageLength    int                   // 最大消息长度
	groupTrigger        config.GroupTriggerConfig // 群聊触发配置
	mediaStore          media.MediaStore      // 媒体存储
	placeholderRecorder PlaceholderRecorder   // 占位符记录器
	owner               Channel               // 嵌入此基础渠道的具体渠道
	reasoningChannelID  string                // 思考频道 ID
}

// NewBaseChannel 创建基础渠道
//
// 参数：
// - name: 渠道名称
// - config: 渠道配置
// - bus: 消息总线
// - allowList: 允许列表
// - opts: 可选配置选项
//
// 返回：
// - *BaseChannel: 基础渠道指针
func NewBaseChannel(
	name string,
	config any,
	bus *bus.MessageBus,
	allowList []string,
	opts ...BaseChannelOption,
) *BaseChannel {
	bc := &BaseChannel{
		config:    config,
		bus:       bus,
		name:      name,
		allowList: allowList,
	}
	for _, opt := range opts {
		opt(bc)
	}
	return bc
}

// MaxMessageLength 返回最大消息长度（按字符计算）
// 值为 0 表示无限制
func (c *BaseChannel) MaxMessageLength() int {
	return c.maxMessageLength
}

// ShouldRespondInGroup 判断是否应该在群聊中响应
// 每个渠道负责：
//  1. 检测是否被 @（平台特定）
//  2. 从内容中剥离机器人提及（平台特定）
//  3. 调用此方法获取群聊响应决策
//
// 逻辑：
//   - 如果被 @ → 总是响应
//   - 如果配置了 mention_only 且未被提及 → 忽略
//   - 如果配置了前缀 → 如果内容以任何前缀开头则响应（并剥离前缀）
//   - 如果配置了前缀但没有匹配且未被提及 → 忽略
//   - 否则（未配置群聊触发）→ 响应所有（宽松默认）
//
// 参数：
// - isMentioned: 是否被提及
// - content: 消息内容
//
// 返回：
// - bool: 是否应该响应
// - string: 处理后的内容（可能已剥离前缀）
func (c *BaseChannel) ShouldRespondInGroup(isMentioned bool, content string) (bool, string) {
	gt := c.groupTrigger

	// 被提及 → 总是响应
	if isMentioned {
		return true, strings.TrimSpace(content)
	}

	// mention_only → 需要提及
	if gt.MentionOnly {
		return false, content
	}

	// 前缀匹配
	if len(gt.Prefixes) > 0 {
		for _, prefix := range gt.Prefixes {
			if prefix != "" && strings.HasPrefix(content, prefix) {
				return true, strings.TrimSpace(strings.TrimPrefix(content, prefix))
			}
		}
		// 配置了前缀但没有匹配且未被提及 → 忽略
		return false, content
	}

	// 未配置群聊触发 → 宽松（响应所有）
	return true, strings.TrimSpace(content)
}

// Name 返回渠道名称
func (c *BaseChannel) Name() string {
	return c.name
}

// ReasoningChannelID 返回思考频道 ID
func (c *BaseChannel) ReasoningChannelID() string {
	return c.reasoningChannelID
}

// IsRunning 返回渠道是否正在运行
func (c *BaseChannel) IsRunning() bool {
	return c.running.Load()
}

// IsAllowed 检查发送者是否在允许列表中
// 支持复合发送者 ID 格式（如 "123456|username"）
//
// 参数：
// - senderID: 发送者 ID
//
// 返回：
// - bool: 是否允许
func (c *BaseChannel) IsAllowed(senderID string) bool {
	if len(c.allowList) == 0 {
		return true
	}

	// 从复合发送者 ID 中提取部分（如 "123456|username"）
	idPart := senderID
	userPart := ""
	if idx := strings.Index(senderID, "|"); idx > 0 {
		idPart = senderID[:idx]
		userPart = senderID[idx+1:]
	}

	for _, allowed := range c.allowList {
		// 从允许值中剥离前导 "@" 用于用户名匹配
		trimmed := strings.TrimPrefix(allowed, "@")
		allowedID := trimmed
		allowedUser := ""
		if idx := strings.Index(trimmed, "|"); idx > 0 {
			allowedID = trimmed[:idx]
			allowedUser = trimmed[idx+1:]
		}

		// 支持任一方使用 "id|username" 复合形式
		// 这保持了与旧版 Telegram 允许列表条目的向后兼容性
		if senderID == allowed ||
			idPart == allowed ||
			senderID == trimmed ||
			idPart == trimmed ||
			idPart == allowedID ||
			(allowedUser != "" && senderID == allowedUser) ||
			(userPart != "" && (userPart == allowed || userPart == trimmed || userPart == allowedUser)) {
			return true
		}
	}

	return false
}

// IsAllowedSender 检查结构化的 SenderInfo 是否被允许列表允许
// 对每个条目委托给 identity.MatchAllowed，提供统一的匹配
// 跨越所有旧格式和新的规范 "platform:id" 格式
//
// 参数：
// - sender: 发送者信息
//
// 返回：
// - bool: 是否允许
func (c *BaseChannel) IsAllowedSender(sender bus.SenderInfo) bool {
	if len(c.allowList) == 0 {
		return true
	}

	for _, allowed := range c.allowList {
		if identity.MatchAllowed(sender, allowed) {
			return true
		}
	}

	return false
}

// HandleMessage 处理传入消息
// 验证发送者权限并发布到消息总线
//
// 参数：
// - ctx: 上下文用于取消控制
// - peer: 对等体信息
// - messageID: 消息 ID
// - senderID: 发送者 ID
// - chatID: 聊天 ID
// - content: 消息内容
// - media: 媒体 URL 列表
// - metadata: 元数据
// - senderOpts: 可选的发送者信息
func (c *BaseChannel) HandleMessage(
	ctx context.Context,
	peer bus.Peer,
	messageID, senderID, chatID, content string,
	media []string,
	metadata map[string]string,
	senderOpts ...bus.SenderInfo,
) {
	// 当可用时使用基于 SenderInfo 的允许检查，否则回退到字符串
	var sender bus.SenderInfo
	if len(senderOpts) > 0 {
		sender = senderOpts[0]
	}
	if sender.CanonicalID != "" || sender.PlatformID != "" {
		if !c.IsAllowedSender(sender) {
			return
		}
	} else {
		if !c.IsAllowed(senderID) {
			return
		}
	}

	// 设置 SenderID 为规范 ID（如果可用），否则保留原始 senderID
	resolvedSenderID := senderID
	if sender.CanonicalID != "" {
		resolvedSenderID = sender.CanonicalID
	}

	scope := BuildMediaScope(c.name, chatID, messageID)

	msg := bus.InboundMessage{
		Channel:    c.name,
		SenderID:   resolvedSenderID,
		Sender:     sender,
		ChatID:     chatID,
		Content:    content,
		Media:      media,
		Peer:       peer,
		MessageID:  messageID,
		MediaScope: scope,
		Metadata:   metadata,
	}

	// 在发布前自动触发输入指示器、消息反应和占位符
	// 每个能力是独立的 — 所有三个可能为同一消息触发
	if c.owner != nil && c.placeholderRecorder != nil {
		// 输入指示器 — 独立流水线
		if tc, ok := c.owner.(TypingCapable); ok {
			if stop, err := tc.StartTyping(ctx, chatID); err == nil {
				c.placeholderRecorder.RecordTypingStop(c.name, chatID, stop)
			}
		}
		// 消息反应 — 独立流水线
		if rc, ok := c.owner.(ReactionCapable); ok && messageID != "" {
			if undo, err := rc.ReactToMessage(ctx, chatID, messageID); err == nil {
				c.placeholderRecorder.RecordReactionUndo(c.name, chatID, undo)
			}
		}
		// 占位符 — 独立流水线
		if pc, ok := c.owner.(PlaceholderCapable); ok {
			if phID, err := pc.SendPlaceholder(ctx, chatID); err == nil && phID != "" {
				c.placeholderRecorder.RecordPlaceholder(c.name, chatID, phID)
			}
		}
	}

	if err := c.bus.PublishInbound(ctx, msg); err != nil {
		logger.ErrorCF("channels", "Failed to publish inbound message", map[string]any{
			"channel": c.name,
			"chat_id": chatID,
			"error":   err.Error(),
		})
	}
}

// SetRunning 设置运行状态
func (c *BaseChannel) SetRunning(running bool) {
	c.running.Store(running)
}

// SetMediaStore 注入媒体存储到渠道
func (c *BaseChannel) SetMediaStore(s media.MediaStore) { c.mediaStore = s }

// GetMediaStore 返回注入的媒体存储（可能为 nil）
func (c *BaseChannel) GetMediaStore() media.MediaStore { return c.mediaStore }

// SetPlaceholderRecorder 注入占位符记录器到渠道
func (c *BaseChannel) SetPlaceholderRecorder(r PlaceholderRecorder) {
	c.placeholderRecorder = r
}

// GetPlaceholderRecorder 返回注入的占位符记录器（可能为 nil）
func (c *BaseChannel) GetPlaceholderRecorder() PlaceholderRecorder {
	return c.placeholderRecorder
}

// SetOwner 注入嵌入此基础渠道的具体渠道
// 这允许 HandleMessage 自动触发 TypingCapable / ReactionCapable / PlaceholderCapable
func (c *BaseChannel) SetOwner(ch Channel) {
	c.owner = ch
}

// BuildMediaScope 构建媒体生命周期跟踪的作用域键
//
// 参数：
// - channel: 渠道名称
// - chatID: 聊天 ID
// - messageID: 消息 ID
//
// 返回：
// - string: 作用域键（格式：channel:chatID:messageID）
func BuildMediaScope(channel, chatID, messageID string) string {
	id := messageID
	if id == "" {
		id = uniqueID()
	}
	return channel + ":" + chatID + ":" + id
}
