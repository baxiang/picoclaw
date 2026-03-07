// Package bus 提供消息总线功能（本文件定义消息类型）
package bus

// Peer 标识消息的路由对等方
// 用于区分消息来源是直接消息、群聊还是频道
type Peer struct {
	Kind string `json:"kind"` // "direct"（直接消息）| "group"（群聊）| "channel"（频道）| ""（未知）
	ID   string `json:"id"`   // 对等方标识符
}

// SenderInfo 提供结构化的发送者身份信息
// 用于跨平台统一标识用户
type SenderInfo struct {
	Platform    string `json:"platform,omitempty"`     // 平台名称："telegram"、"discord"、"slack" 等
	PlatformID  string `json:"platform_id,omitempty"`  // 平台原始 ID，如 "123456"
	CanonicalID string `json:"canonical_id,omitempty"` // 规范格式："platform:id"
	Username    string `json:"username,omitempty"`     // 用户名（如 @alice）
	DisplayName string `json:"display_name,omitempty"` // 显示名称
}

// InboundMessage 入站消息
// 从渠道发送到 Agent 的消息结构
//
// 字段说明：
// - Channel: 渠道名称（telegram、discord 等）
// - SenderID: 发送者 ID
// - Sender: 发送者详细信息（结构化）
// - ChatID: 聊天标识符
// - Content: 消息内容
// - Media: 媒体引用列表（media:// 格式）
// - Peer: 路由对等方（直接消息/群聊等）
// - MessageID: 平台消息 ID
// - MediaScope: 媒体生命周期作用域
// - SessionKey: 会话标识符，用于加载/保存对话历史
// - Metadata: 元数据映射（平台特定信息）
type InboundMessage struct {
	Channel    string            `json:"channel"`
	SenderID   string            `json:"sender_id"`
	Sender     SenderInfo        `json:"sender"`
	ChatID     string            `json:"chat_id"`
	Content    string            `json:"content"`
	Media      []string          `json:"media,omitempty"`
	Peer       Peer              `json:"peer"`                  // 路由对等方
	MessageID  string            `json:"message_id,omitempty"`  // 平台消息 ID
	MediaScope string            `json:"media_scope,omitempty"` // 媒体生命周期作用域
	SessionKey string            `json:"session_key"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// OutboundMessage 出站消息
// 从 Agent 发送到渠道的消息结构
//
// 字段说明：
// - Channel: 目标渠道名称
// - ChatID: 目标聊天标识符
// - Content: 消息内容
type OutboundMessage struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
	Content string `json:"content"`
}

// MediaPart 描述单个媒体附件
// 用于出站媒体消息的多部分上传
type MediaPart struct {
	Type        string `json:"type"`                   // 媒体类型："image"（图片）| "audio"（音频）| "video"（视频）| "file"（文件）
	Ref         string `json:"ref"`                    // 媒体存储引用，如 "media://abc123"
	Caption     string `json:"caption,omitempty"`      // 可选的说明文字
	Filename    string `json:"filename,omitempty"`     // 原始文件名提示
	ContentType string `json:"content_type,omitempty"` // MIME 类型提示
}

// OutboundMediaMessage 出站媒体消息
// 通过总线从 Agent 传递到渠道的媒体附件
//
// 字段说明：
// - Channel: 目标渠道名称
// - ChatID: 目标聊天标识符
// - Parts: 媒体部分列表（支持多附件）
type OutboundMediaMessage struct {
	Channel string      `json:"channel"`
	ChatID  string      `json:"chat_id"`
	Parts   []MediaPart `json:"parts"`
}
