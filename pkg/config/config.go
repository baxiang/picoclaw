// Package config 提供配置管理功能
// 本文件包含：
// - 配置结构体定义（Config 及所有子配置）
// - 配置加载和保存功能
// - 环境变量解析支持
// - 配置迁移功能
// - 模型配置验证
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"

	"github.com/caarlos0/env/v11"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

// rrCounter 全局原子计数器，用于模型间的轮询负载均衡
// 当多个配置使用相同模型名称时，通过此计数器实现请求的均匀分布
var rrCounter atomic.Uint64

// FlexibleStringSlice 灵活字符串切片类型
// 实现 JSON 解 marshal 接口，支持字符串和数字混合类型
// 例如：allow_from 可以包含 "123" 或 123 两种格式
type FlexibleStringSlice []string

// UnmarshalJSON 实现 json.Unmarshaler 接口
// 支持将 JSON 字符串或混合类型数组解析为字符串切片
func (f *FlexibleStringSlice) UnmarshalJSON(data []byte) error {
	// 首先尝试解析为字符串数组
	var ss []string
	if err := json.Unmarshal(data, &ss); err == nil {
		*f = ss
		return nil
	}

	// 尝试解析为接口数组以处理混合类型
	var raw []any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// 将各种类型转换为字符串
	result := make([]string, 0, len(raw))
	for _, v := range raw {
		switch val := v.(type) {
		case string:
			result = append(result, val)
		case float64:
			result = append(result, fmt.Sprintf("%.0f", val))
		default:
			result = append(result, fmt.Sprintf("%v", val))
		}
	}
	*f = result
	return nil
}

// Config 主配置结构体
// 包含所有子系统和组件的配置信息
//
// 字段说明：
// - Agents: Agent 配置（默认值和列表）
// - Bindings: Agent 绑定规则（将特定渠道/用户绑定到指定 Agent）
// - Session: 会话管理配置
// - Channels: 通讯渠道配置（Telegram、Discord、微信等）
// - Providers: LLM 提供商配置（已废弃，使用 ModelList 替代）
// - ModelList: 模型配置列表（新的以模型为中心的提供商配置）
// - Gateway: HTTP 网关配置
// - Tools: 工具配置
// - Heartbeat: 心跳检测配置
// - Devices: 硬件设备配置（I2C、SPI 等）
type Config struct {
	Agents    AgentsConfig    `json:"agents"`
	Bindings  []AgentBinding  `json:"bindings,omitempty"`
	Session   SessionConfig   `json:"session,omitempty"`
	Channels  ChannelsConfig  `json:"channels"`
	Providers ProvidersConfig `json:"providers,omitempty"`
	ModelList []ModelConfig   `json:"model_list"` // 新的以模型为中心的提供商配置
	Gateway   GatewayConfig   `json:"gateway"`
	Tools     ToolsConfig     `json:"tools"`
	Heartbeat HeartbeatConfig `json:"heartbeat"`
	Devices   DevicesConfig   `json:"devices"`
}

// MarshalJSON 实现自定义 JSON 序列化
// 当 Providers 或 Session 为空时，从输出中省略这些字段
// 这样可以保持配置文件的简洁性
func (c Config) MarshalJSON() ([]byte, error) {
	type Alias Config
	aux := &struct {
		Providers *ProvidersConfig `json:"providers,omitempty"`
		Session   *SessionConfig   `json:"session,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(&c),
	}

	// 仅当 Providers 不为空时才包含
	if !c.Providers.IsEmpty() {
		aux.Providers = &c.Providers
	}

	// 仅当 Session 不为空时才包含
	if c.Session.DMScope != "" || len(c.Session.IdentityLinks) > 0 {
		aux.Session = &c.Session
	}

	return json.Marshal(aux)
}

// AgentsConfig Agent 配置结构
// 包含 Agent 默认配置和 Agent 列表
type AgentsConfig struct {
	Defaults AgentDefaults `json:"defaults"` // 默认 Agent 配置
	List     []AgentConfig `json:"list,omitempty"` // Agent 实例列表
}

// AgentModelConfig Agent 模型配置
// 支持字符串和结构化两种格式：
// - 字符串格式："gpt-4"（仅主模型，无降级）
// - 对象格式：{"primary": "gpt-4", "fallbacks": ["claude-haiku"]}
type AgentModelConfig struct {
	Primary   string   `json:"primary,omitempty"`   // 主模型名称
	Fallbacks []string `json:"fallbacks,omitempty"` // 降级模型列表
}

// UnmarshalJSON 实现自定义 JSON 解 marshal
// 支持字符串和对象两种格式
func (m *AgentModelConfig) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		m.Primary = s
		m.Fallbacks = nil
		return nil
	}
	type raw struct {
		Primary   string   `json:"primary"`
		Fallbacks []string `json:"fallbacks"`
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	m.Primary = r.Primary
	m.Fallbacks = r.Fallbacks
	return nil
}

// MarshalJSON 实现自定义 JSON 序列化
// 如果没有降级模型，输出字符串格式；否则输出对象格式
func (m AgentModelConfig) MarshalJSON() ([]byte, error) {
	if len(m.Fallbacks) == 0 && m.Primary != "" {
		return json.Marshal(m.Primary)
	}
	type raw struct {
		Primary   string   `json:"primary,omitempty"`
		Fallbacks []string `json:"fallbacks,omitempty"`
	}
	return json.Marshal(raw{Primary: m.Primary, Fallbacks: m.Fallbacks})
}

// AgentConfig Agent 实例配置
// 定义单个 Agent 的所有配置参数
type AgentConfig struct {
	ID        string            `json:"id"`        // Agent 唯一标识符
	Default   bool              `json:"default,omitempty"` // 是否为默认 Agent
	Name      string            `json:"name,omitempty"` // Agent 名称（人类可读）
	Workspace string            `json:"workspace,omitempty"` // 工作空间目录
	Model     *AgentModelConfig `json:"model,omitempty"` // 模型配置
	Skills    []string          `json:"skills,omitempty"` // 启用的技能列表
	Subagents *SubagentsConfig  `json:"subagents,omitempty"` // 子代理配置
}

// SubagentsConfig 子代理配置
// 控制 Agent 使用子代理（spawn/subagent 工具）的行为
type SubagentsConfig struct {
	AllowAgents []string          `json:"allow_agents,omitempty"` // 允许使用的子代理 ID 列表
	Model       *AgentModelConfig `json:"model,omitempty"` // 子代理使用的模型配置
}

// PeerMatch 对等体匹配规则
// 用于渠道消息的路由匹配
type PeerMatch struct {
	Kind string `json:"kind"` // 匹配类型（如 "user", "group"）
	ID   string `json:"id"`   // 对等体标识符
}

// BindingMatch 绑定匹配规则
// 定义如何将渠道消息绑定到特定 Agent
type BindingMatch struct {
	Channel   string     `json:"channel"`    // 渠道名称（如 "telegram", "discord"）
	AccountID string     `json:"account_id,omitempty"` // 账号 ID
	Peer      *PeerMatch `json:"peer,omitempty"` // 对等体匹配规则
	GuildID   string     `json:"guild_id,omitempty"` // Discord 服务器 ID
	TeamID    string     `json:"team_id,omitempty"`  // Slack 团队 ID
}

// AgentBinding Agent 绑定配置
// 将特定渠道/用户/群组绑定到指定 Agent
type AgentBinding struct {
	AgentID string       `json:"agent_id"` // 目标 Agent ID
	Match   BindingMatch `json:"match"`    // 匹配规则
}

// SessionConfig 会话管理配置
// 控制会话的持久化和身份链接行为
type SessionConfig struct {
	DMScope       string              `json:"dm_scope,omitempty"` // DM 会话作用域
	IdentityLinks map[string][]string `json:"identity_links,omitempty"` // 跨平台身份链接（将不同渠道的同一用户关联）
}

// RoutingConfig 智能模型路由配置
// 根据消息复杂度自动选择模型，优化成本和延迟
//
// 工作原理：
// - 分析消息的结构特征（长度、代码块、工具调用历史、对话深度、附件等）
// - 计算复杂度评分（0-1 范围）
// - 评分低于阈值的简单消息使用轻量模型
// - 其他消息使用 Agent 的主模型
// - 无需关键词匹配，所有评分与语言无关
type RoutingConfig struct {
	Enabled    bool    `json:"enabled"`                      // 是否启用智能路由
	LightModel string  `json:"light_model"`                  // 简单任务使用的轻量模型（来自 model_list 的 model_name）
	Threshold  float64 `json:"threshold"`                    // 复杂度阈值（0-1）；评分 >= 阈值使用主模型
}

// AgentDefaults Agent 默认配置
// 定义所有 Agent 的默认行为参数
// 支持环境变量覆盖（通过 env 标签指定）
type AgentDefaults struct {
	Workspace                 string         `json:"workspace"                       env:"PICOCLAW_AGENTS_DEFAULTS_WORKSPACE"`
	RestrictToWorkspace       bool           `json:"restrict_to_workspace"           env:"PICOCLAW_AGENTS_DEFAULTS_RESTRICT_TO_WORKSPACE"`
	AllowReadOutsideWorkspace bool           `json:"allow_read_outside_workspace"    env:"PICOCLAW_AGENTS_DEFAULTS_ALLOW_READ_OUTSIDE_WORKSPACE"`
	Provider                  string         `json:"provider"                        env:"PICOCLAW_AGENTS_DEFAULTS_PROVIDER"`
	ModelName                 string         `json:"model_name,omitempty"            env:"PICOCLAW_AGENTS_DEFAULTS_MODEL_NAME"`
	Model                     string         `json:"model"                           env:"PICOCLAW_AGENTS_DEFAULTS_MODEL"` // 已废弃：使用 model_name 替代
	ModelFallbacks            []string       `json:"model_fallbacks,omitempty"`
	ImageModel                string         `json:"image_model,omitempty"           env:"PICOCLAW_AGENTS_DEFAULTS_IMAGE_MODEL"`
	ImageModelFallbacks       []string       `json:"image_model_fallbacks,omitempty"`
	MaxTokens                 int            `json:"max_tokens"                      env:"PICOCLAW_AGENTS_DEFAULTS_MAX_TOKENS"`
	Temperature               *float64       `json:"temperature,omitempty"           env:"PICOCLAW_AGENTS_DEFAULTS_TEMPERATURE"`
	MaxToolIterations         int            `json:"max_tool_iterations"             env:"PICOCLAW_AGENTS_DEFAULTS_MAX_TOOL_ITERATIONS"`
	SummarizeMessageThreshold int            `json:"summarize_message_threshold"     env:"PICOCLAW_AGENTS_DEFAULTS_SUMMARIZE_MESSAGE_THRESHOLD"`
	SummarizeTokenPercent     int            `json:"summarize_token_percent"         env:"PICOCLAW_AGENTS_DEFAULTS_SUMMARIZE_TOKEN_PERCENT"`
	MaxMediaSize              int            `json:"max_media_size,omitempty"        env:"PICOCLAW_AGENTS_DEFAULTS_MAX_MEDIA_SIZE"`
	Routing                   *RoutingConfig `json:"routing,omitempty"`
}

// DefaultMaxMediaSize 默认最大媒体文件大小（20 MB）
const DefaultMaxMediaSize = 20 * 1024 * 1024 // 20 MB

// GetMaxMediaSize 获取有效的最大媒体文件大小
// 如果配置中设置了则使用配置值，否则返回默认值
func (d *AgentDefaults) GetMaxMediaSize() int {
	if d.MaxMediaSize > 0 {
		return d.MaxMediaSize
	}
	return DefaultMaxMediaSize
}

// GetModelName 获取有效的模型名称
// 优先使用新的 model_name 字段，向后兼容 model 字段
func (d *AgentDefaults) GetModelName() string {
	if d.ModelName != "" {
		return d.ModelName
	}
	return d.Model
}

// ChannelsConfig 通讯渠道配置
// 包含所有支持的通讯渠道的配置
type ChannelsConfig struct {
	WhatsApp   WhatsAppConfig   `json:"whatsapp"`
	Telegram   TelegramConfig   `json:"telegram"`
	Feishu     FeishuConfig     `json:"feishu"`
	Discord    DiscordConfig    `json:"discord"`
	MaixCam    MaixCamConfig    `json:"maixcam"`
	QQ         QQConfig         `json:"qq"`
	DingTalk   DingTalkConfig   `json:"dingtalk"`
	Slack      SlackConfig      `json:"slack"`
	LINE       LINEConfig       `json:"line"`
	OneBot     OneBotConfig     `json:"onebot"`
	WeCom      WeComConfig      `json:"wecom"`
	WeComApp   WeComAppConfig   `json:"wecom_app"`
	WeComAIBot WeComAIBotConfig `json:"wecom_aibot"`
	Pico       PicoConfig       `json:"pico"`
}

// GroupTriggerConfig 群聊触发配置
// 控制机器人在群聊中的响应行为
type GroupTriggerConfig struct {
	MentionOnly bool     `json:"mention_only,omitempty"` // 是否仅在被 @ 时响应
	Prefixes    []string `json:"prefixes,omitempty"`     // 触发前缀列表（如 ["!", "/"]）
}

// TypingConfig 输入指示配置
// 控制 LLM 响应时是否显示"正在输入"状态
type TypingConfig struct {
	Enabled bool `json:"enabled,omitempty"` // 是否启用输入指示
}

// PlaceholderConfig 占位消息配置
// 在 LLM 响应期间显示临时占位消息
type PlaceholderConfig struct {
	Enabled bool   `json:"enabled,omitempty"` // 是否启用占位消息
	Text    string `json:"text,omitempty"`    // 占位消息文本
}

// WhatsAppConfig WhatsApp 渠道配置
type WhatsAppConfig struct {
	Enabled            bool                `json:"enabled"              env:"PICOCLAW_CHANNELS_WHATSAPP_ENABLED"`
	BridgeURL          string              `json:"bridge_url"           env:"PICOCLAW_CHANNELS_WHATSAPP_BRIDGE_URL"`
	UseNative          bool                `json:"use_native"           env:"PICOCLAW_CHANNELS_WHATSAPP_USE_NATIVE"`
	SessionStorePath   string              `json:"session_store_path"   env:"PICOCLAW_CHANNELS_WHATSAPP_SESSION_STORE_PATH"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"           env:"PICOCLAW_CHANNELS_WHATSAPP_ALLOW_FROM"`
	ReasoningChannelID string              `json:"reasoning_channel_id" env:"PICOCLAW_CHANNELS_WHATSAPP_REASONING_CHANNEL_ID"`
}

// TelegramConfig Telegram 渠道配置
type TelegramConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_TELEGRAM_ENABLED"`
	Token              string              `json:"token"                   env:"PICOCLAW_CHANNELS_TELEGRAM_TOKEN"`
	BaseURL            string              `json:"base_url"                env:"PICOCLAW_CHANNELS_TELEGRAM_BASE_URL"`
	Proxy              string              `json:"proxy"                   env:"PICOCLAW_CHANNELS_TELEGRAM_PROXY"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_TELEGRAM_ALLOW_FROM"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	Typing             TypingConfig        `json:"typing,omitempty"`
	Placeholder        PlaceholderConfig   `json:"placeholder,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_TELEGRAM_REASONING_CHANNEL_ID"`
}

// FeishuConfig 飞书渠道配置
type FeishuConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_FEISHU_ENABLED"`
	AppID              string              `json:"app_id"                  env:"PICOCLAW_CHANNELS_FEISHU_APP_ID"`
	AppSecret          string              `json:"app_secret"              env:"PICOCLAW_CHANNELS_FEISHU_APP_SECRET"`
	EncryptKey         string              `json:"encrypt_key"             env:"PICOCLAW_CHANNELS_FEISHU_ENCRYPT_KEY"`
	VerificationToken  string              `json:"verification_token"      env:"PICOCLAW_CHANNELS_FEISHU_VERIFICATION_TOKEN"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_FEISHU_ALLOW_FROM"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	Placeholder        PlaceholderConfig   `json:"placeholder,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_FEISHU_REASONING_CHANNEL_ID"`
}

// DiscordConfig Discord 渠道配置
type DiscordConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_DISCORD_ENABLED"`
	Token              string              `json:"token"                   env:"PICOCLAW_CHANNELS_DISCORD_TOKEN"`
	Proxy              string              `json:"proxy"                   env:"PICOCLAW_CHANNELS_DISCORD_PROXY"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_DISCORD_ALLOW_FROM"`
	MentionOnly        bool                `json:"mention_only"            env:"PICOCLAW_CHANNELS_DISCORD_MENTION_ONLY"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	Typing             TypingConfig        `json:"typing,omitempty"`
	Placeholder        PlaceholderConfig   `json:"placeholder,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_DISCORD_REASONING_CHANNEL_ID"`
}

// MaixCamConfig MaixCam 渠道配置
type MaixCamConfig struct {
	Enabled            bool                `json:"enabled"              env:"PICOCLAW_CHANNELS_MAIXCAM_ENABLED"`
	Host               string              `json:"host"                 env:"PICOCLAW_CHANNELS_MAIXCAM_HOST"`
	Port               int                 `json:"port"                 env:"PICOCLAW_CHANNELS_MAIXCAM_PORT"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"           env:"PICOCLAW_CHANNELS_MAIXCAM_ALLOW_FROM"`
	ReasoningChannelID string              `json:"reasoning_channel_id" env:"PICOCLAW_CHANNELS_MAIXCAM_REASONING_CHANNEL_ID"`
}

// QQConfig QQ 渠道配置
type QQConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_QQ_ENABLED"`
	AppID              string              `json:"app_id"                  env:"PICOCLAW_CHANNELS_QQ_APP_ID"`
	AppSecret          string              `json:"app_secret"              env:"PICOCLAW_CHANNELS_QQ_APP_SECRET"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_QQ_ALLOW_FROM"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_QQ_REASONING_CHANNEL_ID"`
}

// DingTalkConfig 钉钉渠道配置
type DingTalkConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_DINGTALK_ENABLED"`
	ClientID           string              `json:"client_id"               env:"PICOCLAW_CHANNELS_DINGTALK_CLIENT_ID"`
	ClientSecret       string              `json:"client_secret"           env:"PICOCLAW_CHANNELS_DINGTALK_CLIENT_SECRET"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_DINGTALK_ALLOW_FROM"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_DINGTALK_REASONING_CHANNEL_ID"`
}

// SlackConfig Slack 渠道配置
type SlackConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_SLACK_ENABLED"`
	BotToken           string              `json:"bot_token"               env:"PICOCLAW_CHANNELS_SLACK_BOT_TOKEN"`
	AppToken           string              `json:"app_token"               env:"PICOCLAW_CHANNELS_SLACK_APP_TOKEN"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_SLACK_ALLOW_FROM"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	Typing             TypingConfig        `json:"typing,omitempty"`
	Placeholder        PlaceholderConfig   `json:"placeholder,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_SLACK_REASONING_CHANNEL_ID"`
}

// LINEConfig LINE 渠道配置
type LINEConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_LINE_ENABLED"`
	ChannelSecret      string              `json:"channel_secret"          env:"PICOCLAW_CHANNELS_LINE_CHANNEL_SECRET"`
	ChannelAccessToken string              `json:"channel_access_token"    env:"PICOCLAW_CHANNELS_LINE_CHANNEL_ACCESS_TOKEN"`
	WebhookHost        string              `json:"webhook_host"            env:"PICOCLAW_CHANNELS_LINE_WEBHOOK_HOST"`
	WebhookPort        int                 `json:"webhook_port"            env:"PICOCLAW_CHANNELS_LINE_WEBHOOK_PORT"`
	WebhookPath        string              `json:"webhook_path"            env:"PICOCLAW_CHANNELS_LINE_WEBHOOK_PATH"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_LINE_ALLOW_FROM"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	Typing             TypingConfig        `json:"typing,omitempty"`
	Placeholder        PlaceholderConfig   `json:"placeholder,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_LINE_REASONING_CHANNEL_ID"`
}

// OneBotConfig OneBot（原 Go-CQHTTP）渠道配置
type OneBotConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_ONEBOT_ENABLED"`
	WSUrl              string              `json:"ws_url"                  env:"PICOCLAW_CHANNELS_ONEBOT_WS_URL"`
	AccessToken        string              `json:"access_token"            env:"PICOCLAW_CHANNELS_ONEBOT_ACCESS_TOKEN"`
	ReconnectInterval  int                 `json:"reconnect_interval"      env:"PICOCLAW_CHANNELS_ONEBOT_RECONNECT_INTERVAL"`
	GroupTriggerPrefix []string            `json:"group_trigger_prefix"    env:"PICOCLAW_CHANNELS_ONEBOT_GROUP_TRIGGER_PREFIX"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_ONEBOT_ALLOW_FROM"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	Typing             TypingConfig        `json:"typing,omitempty"`
	Placeholder        PlaceholderConfig   `json:"placeholder,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_ONEBOT_REASONING_CHANNEL_ID"`
}

// WeComConfig 企业微信客服账号配置
type WeComConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_WECOM_ENABLED"`
	Token              string              `json:"token"                   env:"PICOCLAW_CHANNELS_WECOM_TOKEN"`
	EncodingAESKey     string              `json:"encoding_aes_key"        env:"PICOCLAW_CHANNELS_WECOM_ENCODING_AES_KEY"`
	WebhookURL         string              `json:"webhook_url"             env:"PICOCLAW_CHANNELS_WECOM_WEBHOOK_URL"`
	WebhookHost        string              `json:"webhook_host"            env:"PICOCLAW_CHANNELS_WECOM_WEBHOOK_HOST"`
	WebhookPort        int                 `json:"webhook_port"            env:"PICOCLAW_CHANNELS_WECOM_WEBHOOK_PORT"`
	WebhookPath        string              `json:"webhook_path"            env:"PICOCLAW_CHANNELS_WECOM_WEBHOOK_PATH"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_WECOM_ALLOW_FROM"`
	ReplyTimeout       int                 `json:"reply_timeout"           env:"PICOCLAW_CHANNELS_WECOM_REPLY_TIMEOUT"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_WECOM_REASONING_CHANNEL_ID"`
}

// WeComAppConfig 企业微信自建应用配置
type WeComAppConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_WECOM_APP_ENABLED"`
	CorpID             string              `json:"corp_id"                 env:"PICOCLAW_CHANNELS_WECOM_APP_CORP_ID"`
	CorpSecret         string              `json:"corp_secret"             env:"PICOCLAW_CHANNELS_WECOM_APP_CORP_SECRET"`
	AgentID            int64               `json:"agent_id"                env:"PICOCLAW_CHANNELS_WECOM_APP_AGENT_ID"`
	Token              string              `json:"token"                   env:"PICOCLAW_CHANNELS_WECOM_APP_TOKEN"`
	EncodingAESKey     string              `json:"encoding_aes_key"        env:"PICOCLAW_CHANNELS_WECOM_APP_ENCODING_AES_KEY"`
	WebhookHost        string              `json:"webhook_host"            env:"PICOCLAW_CHANNELS_WECOM_APP_WEBHOOK_HOST"`
	WebhookPort        int                 `json:"webhook_port"            env:"PICOCLAW_CHANNELS_WECOM_APP_WEBHOOK_PORT"`
	WebhookPath        string              `json:"webhook_path"            env:"PICOCLAW_CHANNELS_WECOM_APP_WEBHOOK_PATH"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_WECOM_APP_ALLOW_FROM"`
	ReplyTimeout       int                 `json:"reply_timeout"           env:"PICOCLAW_CHANNELS_WECOM_APP_REPLY_TIMEOUT"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_WECOM_APP_REASONING_CHANNEL_ID"`
}

// WeComAIBotConfig 企业微信 AI Bot 配置
type WeComAIBotConfig struct {
	Enabled            bool                `json:"enabled"              env:"PICOCLAW_CHANNELS_WECOM_AIBOT_ENABLED"`
	Token              string              `json:"token"                env:"PICOCLAW_CHANNELS_WECOM_AIBOT_TOKEN"`
	EncodingAESKey     string              `json:"encoding_aes_key"     env:"PICOCLAW_CHANNELS_WECOM_AIBOT_ENCODING_AES_KEY"`
	WebhookPath        string              `json:"webhook_path"         env:"PICOCLAW_CHANNELS_WECOM_AIBOT_WEBHOOK_PATH"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"           env:"PICOCLAW_CHANNELS_WECOM_AIBOT_ALLOW_FROM"`
	ReplyTimeout       int                 `json:"reply_timeout"        env:"PICOCLAW_CHANNELS_WECOM_AIBOT_REPLY_TIMEOUT"`
	MaxSteps           int                 `json:"max_steps"            env:"PICOCLAW_CHANNELS_WECOM_AIBOT_MAX_STEPS"`       // 最大流式响应步数
	WelcomeMessage     string              `json:"welcome_message"      env:"PICOCLAW_CHANNELS_WECOM_AIBOT_WELCOME_MESSAGE"` // 进入会话时发送的欢迎消息；空表示不发送
	ReasoningChannelID string              `json:"reasoning_channel_id" env:"PICOCLAW_CHANNELS_WECOM_AIBOT_REASONING_CHANNEL_ID"`
}

// PicoConfig Pico 原生渠道配置
type PicoConfig struct {
	Enabled         bool                `json:"enabled"                     env:"PICOCLAW_CHANNELS_PICO_ENABLED"`
	Token           string              `json:"token"                       env:"PICOCLAW_CHANNELS_PICO_TOKEN"`
	AllowTokenQuery bool                `json:"allow_token_query,omitempty"`
	AllowOrigins    []string            `json:"allow_origins,omitempty"`
	PingInterval    int                 `json:"ping_interval,omitempty"`
	ReadTimeout     int                 `json:"read_timeout,omitempty"`
	WriteTimeout    int                 `json:"write_timeout,omitempty"`
	MaxConnections  int                 `json:"max_connections,omitempty"`
	AllowFrom       FlexibleStringSlice `json:"allow_from"                  env:"PICOCLAW_CHANNELS_PICO_ALLOW_FROM"`
	Placeholder     PlaceholderConfig   `json:"placeholder,omitempty"`
}

// HeartbeatConfig 心跳检测配置
type HeartbeatConfig struct {
	Enabled  bool `json:"enabled"  env:"PICOCLAW_HEARTBEAT_ENABLED"`
	Interval int  `json:"interval" env:"PICOCLAW_HEARTBEAT_INTERVAL"` // 心跳间隔（分钟），最小值 5
}

// DevicesConfig 硬件设备配置
type DevicesConfig struct {
	Enabled    bool `json:"enabled"     env:"PICOCLAW_DEVICES_ENABLED"`
	MonitorUSB bool `json:"monitor_usb" env:"PICOCLAW_DEVICES_MONITOR_USB"` // 是否监控 USB 设备插拔
}

// ProvidersConfig LLM 提供商配置（旧版，已逐步迁移到 ModelList）
// 包含所有支持的 LLM 提供商的配置
type ProvidersConfig struct {
	Anthropic     ProviderConfig       `json:"anthropic"`
	OpenAI        OpenAIProviderConfig `json:"openai"`
	LiteLLM       ProviderConfig       `json:"litellm"`
	OpenRouter    ProviderConfig       `json:"openrouter"`
	Groq          ProviderConfig       `json:"groq"`
	Zhipu         ProviderConfig       `json:"zhipu"`
	VLLM          ProviderConfig       `json:"vllm"`
	Gemini        ProviderConfig       `json:"gemini"`
	Nvidia        ProviderConfig       `json:"nvidia"`
	Ollama        ProviderConfig       `json:"ollama"`
	Moonshot      ProviderConfig       `json:"moonshot"`
	ShengSuanYun  ProviderConfig       `json:"shengsuanyun"`
	DeepSeek      ProviderConfig       `json:"deepseek"`
	Cerebras      ProviderConfig       `json:"cerebras"`
	VolcEngine    ProviderConfig       `json:"volcengine"`
	GitHubCopilot ProviderConfig       `json:"github_copilot"`
	Antigravity   ProviderConfig       `json:"antigravity"`
	Qwen          ProviderConfig       `json:"qwen"`
	Mistral       ProviderConfig       `json:"mistral"`
	Avian         ProviderConfig       `json:"avian"`
}

// IsEmpty 检查所有提供商配置是否为空
// 当没有设置任何 API Key 或 API Base 时返回 true
// 注意：WebSearch 仅作为优化选项，不计入"非空"判断
func (p ProvidersConfig) IsEmpty() bool {
	return p.Anthropic.APIKey == "" && p.Anthropic.APIBase == "" &&
		p.OpenAI.APIKey == "" && p.OpenAI.APIBase == "" &&
		p.LiteLLM.APIKey == "" && p.LiteLLM.APIBase == "" &&
		p.OpenRouter.APIKey == "" && p.OpenRouter.APIBase == "" &&
		p.Groq.APIKey == "" && p.Groq.APIBase == "" &&
		p.Zhipu.APIKey == "" && p.Zhipu.APIBase == "" &&
		p.VLLM.APIKey == "" && p.VLLM.APIBase == "" &&
		p.Gemini.APIKey == "" && p.Gemini.APIBase == "" &&
		p.Nvidia.APIKey == "" && p.Nvidia.APIBase == "" &&
		p.Ollama.APIKey == "" && p.Ollama.APIBase == "" &&
		p.Moonshot.APIKey == "" && p.Moonshot.APIBase == "" &&
		p.ShengSuanYun.APIKey == "" && p.ShengSuanYun.APIBase == "" &&
		p.DeepSeek.APIKey == "" && p.DeepSeek.APIBase == "" &&
		p.Cerebras.APIKey == "" && p.Cerebras.APIBase == "" &&
		p.VolcEngine.APIKey == "" && p.VolcEngine.APIBase == "" &&
		p.GitHubCopilot.APIKey == "" && p.GitHubCopilot.APIBase == "" &&
		p.Antigravity.APIKey == "" && p.Antigravity.APIBase == "" &&
		p.Qwen.APIKey == "" && p.Qwen.APIBase == "" &&
		p.Mistral.APIKey == "" && p.Mistral.APIBase == "" &&
		p.Avian.APIKey == "" && p.Avian.APIBase == ""
}

// MarshalJSON 实现自定义 JSON 序列化
// 当配置为空时返回 null，从输出中省略整个 providers 部分
func (p ProvidersConfig) MarshalJSON() ([]byte, error) {
	if p.IsEmpty() {
		return []byte("null"), nil
	}
	type Alias ProvidersConfig
	return json.Marshal((*Alias)(&p))
}

// ProviderConfig 通用提供商配置结构
// 适用于大多数 HTTP 基础的 LLM 提供商
type ProviderConfig struct {
	APIKey         string `json:"api_key"                   env:"PICOCLAW_PROVIDERS_{{.Name}}_API_KEY"`
	APIBase        string `json:"api_base"                  env:"PICOCLAW_PROVIDERS_{{.Name}}_API_BASE"`
	Proxy          string `json:"proxy,omitempty"           env:"PICOCLAW_PROVIDERS_{{.Name}}_PROXY"`
	RequestTimeout int    `json:"request_timeout,omitempty" env:"PICOCLAW_PROVIDERS_{{.Name}}_REQUEST_TIMEOUT"`
	AuthMethod     string `json:"auth_method,omitempty"     env:"PICOCLAW_PROVIDERS_{{.Name}}_AUTH_METHOD"`
	ConnectMode    string `json:"connect_mode,omitempty"    env:"PICOCLAW_PROVIDERS_{{.Name}}_CONNECT_MODE"` // 仅用于 GitHub Copilot：`stdio` 或 `grpc`
}

// OpenAIProviderConfig OpenAI 提供商配置
// 在通用配置基础上增加 Web Search 功能开关
type OpenAIProviderConfig struct {
	ProviderConfig
	WebSearch bool `json:"web_search" env:"PICOCLAW_PROVIDERS_OPENAI_WEB_SEARCH"` // 是否启用 Web Search 功能
}

// ModelConfig 模型配置结构（新的以模型为中心的配置方式）
// 允许仅通过配置添加新的提供商（尤其是 OpenAI 兼容的提供商）
// model 字段使用协议前缀格式：[protocol/]model-identifier
// 支持的协议：openai、anthropic、antigravity、claude-cli、codex-cli、github-copilot
// 默认协议为 "openai"（如果没有指定前缀）
type ModelConfig struct {
	// 必填字段
	ModelName string `json:"model_name"` // 用户可见的模型别名
	Model     string `json:"model"`      // 协议/模型标识符（如 "openai/gpt-4o"、"anthropic/claude-sonnet-4.6"）

	// HTTP 基础提供商配置
	APIBase string `json:"api_base,omitempty"` // API 端点 URL
	APIKey  string `json:"api_key"`            // API 认证密钥
	Proxy   string `json:"proxy,omitempty"`    // HTTP 代理 URL

	// 特殊提供商配置（基于 CLI、OAuth 等）
	AuthMethod  string `json:"auth_method,omitempty"`  // 认证方法：oauth、token
	ConnectMode string `json:"connect_mode,omitempty"` // 连接模式：stdio、grpc
	Workspace   string `json:"workspace,omitempty"`    // CLI 基础提供商的工作空间路径

	// 可选优化配置
	RPM            int    `json:"rpm,omitempty"`              // 每分钟请求数限制
	MaxTokensField string `json:"max_tokens_field,omitempty"` // 最大令牌字段名（如 "max_completion_tokens"）
	RequestTimeout int    `json:"request_timeout,omitempty"`  // 请求超时（秒）
	ThinkingLevel  string `json:"thinking_level,omitempty"`   // 扩展思考级别：off|low|medium|high|xhigh|adaptive
}

// Validate 验证 ModelConfig 是否包含所有必填字段
//
// 返回：
// - error: 如果验证失败，返回错误信息
func (c *ModelConfig) Validate() error {
	if c.ModelName == "" {
		return fmt.Errorf("model_name is required")
	}
	if c.Model == "" {
		return fmt.Errorf("model is required")
	}
	return nil
}

// GatewayConfig HTTP 网关配置
// 控制内置 HTTP API 服务器的监听地址
type GatewayConfig struct {
	Host string `json:"host" env:"PICOCLAW_GATEWAY_HOST"` // 监听主机
	Port int    `json:"port" env:"PICOCLAW_GATEWAY_PORT"` // 监听端口
}

// ToolConfig 通用工具配置结构
// 用于启用/禁用单个工具
type ToolConfig struct {
	Enabled bool `json:"enabled" env:"ENABLED"` // 是否启用该工具
}

// BraveConfig Brave Search 配置
type BraveConfig struct {
	Enabled    bool   `json:"enabled"     env:"PICOCLAW_TOOLS_WEB_BRAVE_ENABLED"`
	APIKey     string `json:"api_key"     env:"PICOCLAW_TOOLS_WEB_BRAVE_API_KEY"`
	MaxResults int    `json:"max_results" env:"PICOCLAW_TOOLS_WEB_BRAVE_MAX_RESULTS"`
}

// TavilyConfig Tavily Search 配置
type TavilyConfig struct {
	Enabled    bool   `json:"enabled"     env:"PICOCLAW_TOOLS_WEB_TAVILY_ENABLED"`
	APIKey     string `json:"api_key"     env:"PICOCLAW_TOOLS_WEB_TAVILY_API_KEY"`
	BaseURL    string `json:"base_url"    env:"PICOCLAW_TOOLS_WEB_TAVILY_BASE_URL"`
	MaxResults int    `json:"max_results" env:"PICOCLAW_TOOLS_WEB_TAVILY_MAX_RESULTS"`
}

// DuckDuckGoConfig DuckDuckGo Search 配置
type DuckDuckGoConfig struct {
	Enabled    bool `json:"enabled"     env:"PICOCLAW_TOOLS_WEB_DUCKDUCKGO_ENABLED"`
	MaxResults int  `json:"max_results" env:"PICOCLAW_TOOLS_WEB_DUCKDUCKGO_MAX_RESULTS"`
}

// PerplexityConfig Perplexity Search 配置
type PerplexityConfig struct {
	Enabled    bool   `json:"enabled"     env:"PICOCLAW_TOOLS_WEB_PERPLEXITY_ENABLED"`
	APIKey     string `json:"api_key"     env:"PICOCLAW_TOOLS_WEB_PERPLEXITY_API_KEY"`
	MaxResults int    `json:"max_results" env:"PICOCLAW_TOOLS_WEB_PERPLEXITY_MAX_RESULTS"`
}

// SearXNGConfig SearXNG Search 配置
type SearXNGConfig struct {
	Enabled    bool   `json:"enabled"     env:"PICOCLAW_TOOLS_WEB_SEARXNG_ENABLED"`
	BaseURL    string `json:"base_url"    env:"PICOCLAW_TOOLS_WEB_SEARXNG_BASE_URL"`
	MaxResults int    `json:"max_results" env:"PICOCLAW_TOOLS_WEB_SEARXNG_MAX_RESULTS"`
}

// GLMSearchConfig GLM Search 配置
// 支持多种搜索引擎后端
type GLMSearchConfig struct {
	Enabled bool   `json:"enabled"  env:"PICOCLAW_TOOLS_WEB_GLM_ENABLED"`
	APIKey  string `json:"api_key"  env:"PICOCLAW_TOOLS_WEB_GLM_API_KEY"`
	BaseURL string `json:"base_url" env:"PICOCLAW_TOOLS_WEB_GLM_BASE_URL"`
	// SearchEngine 指定搜索引擎后端："search_std"（默认）、
	// "search_pro"、"search_pro_sogou" 或 "search_pro_quark"
	SearchEngine string `json:"search_engine" env:"PICOCLAW_TOOLS_WEB_GLM_SEARCH_ENGINE"`
	MaxResults   int    `json:"max_results"   env:"PICOCLAW_TOOLS_WEB_GLM_MAX_RESULTS"`
}

// WebToolsConfig 网络搜索工具配置
// 整合多种网络搜索提供商
type WebToolsConfig struct {
	ToolConfig `                 envPrefix:"PICOCLAW_TOOLS_WEB_"`
	Brave      BraveConfig      `                                json:"brave"`
	Tavily     TavilyConfig     `                                json:"tavily"`
	DuckDuckGo DuckDuckGoConfig `                                json:"duckduckgo"`
	Perplexity PerplexityConfig `                                json:"perplexity"`
	SearXNG    SearXNGConfig    `                                json:"searxng"`
	GLMSearch  GLMSearchConfig  `                                json:"glm_search"`
	// Proxy 可选的代理 URL，用于网络搜索工具
	// 支持 http/https/socks5/socks5h 协议
	// 对于需要认证的代理，优先使用 HTTP_PROXY/HTTPS_PROXY 环境变量而非在配置中嵌入凭据
	Proxy           string `json:"proxy,omitempty"             env:"PICOCLAW_TOOLS_WEB_PROXY"`
	FetchLimitBytes int64  `json:"fetch_limit_bytes,omitempty" env:"PICOCLAW_TOOLS_WEB_FETCH_LIMIT_BYTES"`
}

// CronToolsConfig 定时任务工具配置
type CronToolsConfig struct {
	ToolConfig         `    envPrefix:"PICOCLAW_TOOLS_CRON_"`
	ExecTimeoutMinutes int `                                 env:"PICOCLAW_TOOLS_CRON_EXEC_TIMEOUT_MINUTES" json:"exec_timeout_minutes"` // 执行超时（分钟）；0 表示无超时
}

// ExecConfig Shell 执行工具配置
type ExecConfig struct {
	ToolConfig          `         envPrefix:"PICOCLAW_TOOLS_EXEC_"`
	EnableDenyPatterns  bool     `                                 env:"PICOCLAW_TOOLS_EXEC_ENABLE_DENY_PATTERNS"  json:"enable_deny_patterns"`
	CustomDenyPatterns  []string `                                 env:"PICOCLAW_TOOLS_EXEC_CUSTOM_DENY_PATTERNS"  json:"custom_deny_patterns"`
	CustomAllowPatterns []string `                                 env:"PICOCLAW_TOOLS_EXEC_CUSTOM_ALLOW_PATTERNS" json:"custom_allow_patterns"`
	TimeoutSeconds      int      `                                 env:"PICOCLAW_TOOLS_EXEC_TIMEOUT_SECONDS"       json:"timeout_seconds"` // 超时（秒）；0 表示使用默认值（60 秒）
}

// SkillsToolsConfig 技能管理工具配置
type SkillsToolsConfig struct {
	ToolConfig            `                       envPrefix:"PICOCLAW_TOOLS_SKILLS_"`
	Registries            SkillsRegistriesConfig `                                   json:"registries"`
	MaxConcurrentSearches int                    `                                   json:"max_concurrent_searches" env:"PICOCLAW_TOOLS_SKILLS_MAX_CONCURRENT_SEARCHES"`
	SearchCache           SearchCacheConfig      `                                   json:"search_cache"`
}

// MediaCleanupConfig 媒体清理工具配置
// 定期清理旧的媒体文件以释放存储空间
type MediaCleanupConfig struct {
	ToolConfig `    envPrefix:"PICOCLAW_MEDIA_CLEANUP_"`
	MaxAge     int `                                    env:"PICOCLAW_MEDIA_CLEANUP_MAX_AGE"  json:"max_age_minutes"`    // 媒体文件最大保留时间（分钟）
	Interval   int `                                    env:"PICOCLAW_MEDIA_CLEANUP_INTERVAL" json:"interval_minutes"`   // 清理间隔（分钟）
}

// ToolsConfig 工具配置总结构
// 包含所有工具的启用状态和参数配置
type ToolsConfig struct {
	AllowReadPaths  []string           `json:"allow_read_paths"  env:"PICOCLAW_TOOLS_ALLOW_READ_PATHS"`   // 允许读取的路径白名单
	AllowWritePaths []string           `json:"allow_write_paths" env:"PICOCLAW_TOOLS_ALLOW_WRITE_PATHS"`  // 允许写入的路径白名单
	Web             WebToolsConfig     `json:"web"`
	Cron            CronToolsConfig    `json:"cron"`
	Exec            ExecConfig         `json:"exec"`
	Skills          SkillsToolsConfig  `json:"skills"`
	MediaCleanup    MediaCleanupConfig `json:"media_cleanup"`
	MCP             MCPConfig          `json:"mcp"`
	AppendFile      ToolConfig         `json:"append_file"                                              envPrefix:"PICOCLAW_TOOLS_APPEND_FILE_"`
	EditFile        ToolConfig         `json:"edit_file"                                                envPrefix:"PICOCLAW_TOOLS_EDIT_FILE_"`
	FindSkills      ToolConfig         `json:"find_skills"                                              envPrefix:"PICOCLAW_TOOLS_FIND_SKILLS_"`
	I2C             ToolConfig         `json:"i2c"                                                      envPrefix:"PICOCLAW_TOOLS_I2C_"`
	InstallSkill    ToolConfig         `json:"install_skill"                                            envPrefix:"PICOCLAW_TOOLS_INSTALL_SKILL_"`
	ListDir         ToolConfig         `json:"list_dir"                                                 envPrefix:"PICOCLAW_TOOLS_LIST_DIR_"`
	Message         ToolConfig         `json:"message"                                                  envPrefix:"PICOCLAW_TOOLS_MESSAGE_"`
	ReadFile        ToolConfig         `json:"read_file"                                                envPrefix:"PICOCLAW_TOOLS_READ_FILE_"`
	SendFile        ToolConfig         `json:"send_file"                                                envPrefix:"PICOCLAW_TOOLS_SEND_FILE_"`
	Spawn           ToolConfig         `json:"spawn"                                                    envPrefix:"PICOCLAW_TOOLS_SPAWN_"`
	SPI             ToolConfig         `json:"spi"                                                      envPrefix:"PICOCLAW_TOOLS_SPI_"`
	Subagent        ToolConfig         `json:"subagent"                                                 envPrefix:"PICOCLAW_TOOLS_SUBAGENT_"`
	WebFetch        ToolConfig         `json:"web_fetch"                                                envPrefix:"PICOCLAW_TOOLS_WEB_FETCH_"`
	WriteFile       ToolConfig         `json:"write_file"                                               envPrefix:"PICOCLAW_TOOLS_WRITE_FILE_"`
}

// SearchCacheConfig 搜索缓存配置
// 用于缓存技能搜索结果，减少重复请求
type SearchCacheConfig struct {
	MaxSize    int `json:"max_size"    env:"PICOCLAW_SKILLS_SEARCH_CACHE_MAX_SIZE"`    // 缓存最大条目数
	TTLSeconds int `json:"ttl_seconds" env:"PICOCLAW_SKILLS_SEARCH_CACHE_TTL_SECONDS"` // 缓存过期时间（秒）
}

// SkillsRegistriesConfig 技能注册表配置
// 定义技能来源的注册表
type SkillsRegistriesConfig struct {
	ClawHub ClawHubRegistryConfig `json:"clawhub"` // ClawHub 注册表配置
}

// ClawHubRegistryConfig ClawHub 注册表配置
// ClawHub 是 PicoClaw 的技能共享平台
type ClawHubRegistryConfig struct {
	Enabled         bool   `json:"enabled"           env:"PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_ENABLED"`
	BaseURL         string `json:"base_url"          env:"PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_BASE_URL"`
	AuthToken       string `json:"auth_token"        env:"PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_AUTH_TOKEN"`
	SearchPath      string `json:"search_path"       env:"PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_SEARCH_PATH"`
	SkillsPath      string `json:"skills_path"       env:"PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_SKILLS_PATH"`
	DownloadPath    string `json:"download_path"     env:"PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_DOWNLOAD_PATH"`
	Timeout         int    `json:"timeout"           env:"PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_TIMEOUT"`
	MaxZipSize      int    `json:"max_zip_size"      env:"PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_MAX_ZIP_SIZE"`
	MaxResponseSize int    `json:"max_response_size" env:"PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_MAX_RESPONSE_SIZE"`
}

// MCPServerConfig MCP 服务器配置
// 定义单个 MCP（Model Context Protocol）服务器的连接参数
type MCPServerConfig struct {
	// Enabled 表示此 MCP 服务器是否处于活动状态
	Enabled bool `json:"enabled"`
	// Command 是要运行的可执行文件（如 "npx"、"python"、"/path/to/server"）
	Command string `json:"command"`
	// Args 是传递给命令的参数
	Args []string `json:"args,omitempty"`
	// Env 是为服务器进程设置的环境变量（仅 stdio 传输）
	Env map[string]string `json:"env,omitempty"`
	// EnvFile 是包含环境变量的文件路径（仅 stdio 传输）
	EnvFile string `json:"env_file,omitempty"`
	// Type 是传输类型："stdio"、"sse" 或 "http"（默认：如果设置了 command 则为 stdio，如果设置了 url 则为 sse）
	Type string `json:"type,omitempty"`
	// URL 用于 SSE/HTTP 传输
	URL string `json:"url,omitempty"`
	// Headers 是随请求发送的 HTTP 头（仅 sse/http 传输）
	Headers map[string]string `json:"headers,omitempty"`
}

// MCPConfig MCP 工具配置
// 包含所有 MCP 服务器的配置
type MCPConfig struct {
	ToolConfig `envPrefix:"PICOCLAW_TOOLS_MCP_"`
	// Servers 是服务器名称到服务器配置的映射
	Servers map[string]MCPServerConfig `json:"servers,omitempty"`
}

// LoadConfig 从文件加载配置
// 支持 JSON 格式配置文件和环境变量覆盖
//
// 参数：
// - path: 配置文件路径
//
// 返回：
// - *Config: 配置对象指针
// - error: 加载错误（如果文件不存在则返回默认配置）
//
// 加载流程：
// 1. 加载默认配置
// 2. 读取并解析 JSON 文件
// 3. 解析环境变量覆盖
// 4. 迁移旧版渠道配置字段
// 5. 自动转换旧版 providers 配置为 model_list
// 6. 验证 model_list 唯一性和必填字段
func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}

	// 预扫描 JSON 以检查用户提供了多少 model_list 条目
	// Go 的 JSON 解码器会重用现有切片的底层数组元素而不是零初始化它们
	// 所以如果用户的 JSON 中缺少某些字段（如 api_base）
	// 这些字段会静默继承 DefaultConfig 模板中相同索引位置的值
	// 只有当用户实际提供了条目时我们才重置 cfg.ModelList
	// 当条目数为 0 时，我们保留 DefaultConfig 的内置列表作为后备
	var tmp Config
	if err := json.Unmarshal(data, &tmp); err != nil {
		return nil, err
	}
	if len(tmp.ModelList) > 0 {
		cfg.ModelList = nil
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	if err := env.Parse(cfg); err != nil {
		return nil, err
	}

	// 迁移旧版渠道配置字段到新的统一结构
	cfg.migrateChannelConfigs()

	// 自动迁移：如果仅存在旧版 providers 配置，转换为 model_list
	if len(cfg.ModelList) == 0 && cfg.HasProvidersConfig() {
		cfg.ModelList = ConvertProvidersToModelList(cfg)
	}

	// 验证 model_list 的唯一性和必填字段
	if err := cfg.ValidateModelList(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// migrateChannelConfigs 迁移旧版渠道配置字段
// 将旧版独立字段迁移到新的统一 GroupTriggerConfig 结构
func (c *Config) migrateChannelConfigs() {
	// Discord: mention_only -> group_trigger.mention_only
	if c.Channels.Discord.MentionOnly && !c.Channels.Discord.GroupTrigger.MentionOnly {
		c.Channels.Discord.GroupTrigger.MentionOnly = true
	}

	// OneBot: group_trigger_prefix -> group_trigger.prefixes
	if len(c.Channels.OneBot.GroupTriggerPrefix) > 0 &&
		len(c.Channels.OneBot.GroupTrigger.Prefixes) == 0 {
		c.Channels.OneBot.GroupTrigger.Prefixes = c.Channels.OneBot.GroupTriggerPrefix
	}
}

// SaveConfig 保存配置到文件
// 使用原子写入模式确保数据一致性
//
// 参数：
// - path: 配置文件路径
// - cfg: 配置对象指针
//
// 返回：
// - error: 保存错误
func SaveConfig(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	// 使用统一的原子写入工具，显式 sync 确保闪存存储可靠性
	// 使用 0o600（仅所有者可读写）作为安全默认权限
	return fileutil.WriteFileAtomic(path, data, 0o600)
}

// WorkspacePath 获取工作空间路径
// 展开路径中的 ~ 为家目录
//
// 返回：
// - string: 绝对工作空间路径
func (c *Config) WorkspacePath() string {
	return expandHome(c.Agents.Defaults.Workspace)
}

// GetAPIKey 获取第一个可用的 API Key
// 按优先级检查各提供商的 API Key
//
// 返回：
// - string: API Key（如果没有设置则返回空字符串）
func (c *Config) GetAPIKey() string {
	if c.Providers.OpenRouter.APIKey != "" {
		return c.Providers.OpenRouter.APIKey
	}
	if c.Providers.Anthropic.APIKey != "" {
		return c.Providers.Anthropic.APIKey
	}
	if c.Providers.OpenAI.APIKey != "" {
		return c.Providers.OpenAI.APIKey
	}
	if c.Providers.Gemini.APIKey != "" {
		return c.Providers.Gemini.APIKey
	}
	if c.Providers.Zhipu.APIKey != "" {
		return c.Providers.Zhipu.APIKey
	}
	if c.Providers.Groq.APIKey != "" {
		return c.Providers.Groq.APIKey
	}
	if c.Providers.VLLM.APIKey != "" {
		return c.Providers.VLLM.APIKey
	}
	if c.Providers.ShengSuanYun.APIKey != "" {
		return c.Providers.ShengSuanYun.APIKey
	}
	if c.Providers.Cerebras.APIKey != "" {
		return c.Providers.Cerebras.APIKey
	}
	return ""
}

// GetAPIBase 获取第一个可用的 API Base URL
// 按优先级检查各提供商的 API Base
//
// 返回：
// - string: API Base URL（如果没有设置则返回空字符串）
func (c *Config) GetAPIBase() string {
	if c.Providers.OpenRouter.APIKey != "" {
		if c.Providers.OpenRouter.APIBase != "" {
			return c.Providers.OpenRouter.APIBase
		}
		return "https://openrouter.ai/api/v1"
	}
	if c.Providers.Zhipu.APIKey != "" {
		return c.Providers.Zhipu.APIBase
	}
	if c.Providers.VLLM.APIKey != "" && c.Providers.VLLM.APIBase != "" {
		return c.Providers.VLLM.APIBase
	}
	return ""
}

// expandHome 展开路径中的 ~ 为家目录
//
// 参数：
// - path: 可能包含 ~ 的路径
//
// 返回：
// - string: 展开后的绝对路径
func expandHome(path string) string {
	if path == "" {
		return path
	}
	if path[0] == '~' {
		home, _ := os.UserHomeDir()
		if len(path) > 1 && path[1] == '/' {
			return home + path[1:]
		}
		return home
	}
	return path
}

// GetModelConfig 根据模型名称获取 ModelConfig
// 如果存在多个相同 model_name 的配置，使用轮询（round-robin）方式选择以实现负载均衡
//
// 参数：
// - modelName: 模型名称
//
// 返回：
// - *ModelConfig: 模型配置指针
// - error: 如果模型未找到则返回错误
func (c *Config) GetModelConfig(modelName string) (*ModelConfig, error) {
	matches := c.findMatches(modelName)
	if len(matches) == 0 {
		return nil, fmt.Errorf("model %q not found in model_list or providers", modelName)
	}
	if len(matches) == 1 {
		return &matches[0], nil
	}

	// 多个配置 - 使用轮询实现负载均衡
	idx := rrCounter.Add(1) % uint64(len(matches))
	return &matches[idx], nil
}

// findMatches 查找所有匹配给定模型名称的 ModelConfig 条目
//
// 参数：
// - modelName: 要查找的模型名称
//
// 返回：
// - []ModelConfig: 匹配的配置列表
func (c *Config) findMatches(modelName string) []ModelConfig {
	var matches []ModelConfig
	for i := range c.ModelList {
		if c.ModelList[i].ModelName == modelName {
			matches = append(matches, c.ModelList[i])
		}
	}
	return matches
}

// HasProvidersConfig 检查是否存在旧版 providers 配置
// 当任何提供商配置不为空时返回 true
//
// 返回：
// - bool: 是否存在 providers 配置
func (c *Config) HasProvidersConfig() bool {
	return !c.Providers.IsEmpty()
}

// ValidateModelList 验证 model_list 中的所有 ModelConfig 条目
// 检查每个模型配置是否有效（必填字段是否存在）
// 注意：允许多个条目使用相同的 model_name（用于负载均衡）
//
// 返回：
// - error: 如果验证失败则返回错误
func (c *Config) ValidateModelList() error {
	for i := range c.ModelList {
		if err := c.ModelList[i].Validate(); err != nil {
			return fmt.Errorf("model_list[%d]: %w", i, err)
		}
	}
	return nil
}

// IsToolEnabled 检查指定工具是否启用
//
// 参数：
// - name: 工具名称（如 "web"、"cron"、"exec" 等）
//
// 返回：
// - bool: 工具是否启用
func (t *ToolsConfig) IsToolEnabled(name string) bool {
	switch name {
	case "web":
		return t.Web.Enabled
	case "cron":
		return t.Cron.Enabled
	case "exec":
		return t.Exec.Enabled
	case "skills":
		return t.Skills.Enabled
	case "media_cleanup":
		return t.MediaCleanup.Enabled
	case "append_file":
		return t.AppendFile.Enabled
	case "edit_file":
		return t.EditFile.Enabled
	case "find_skills":
		return t.FindSkills.Enabled
	case "i2c":
		return t.I2C.Enabled
	case "install_skill":
		return t.InstallSkill.Enabled
	case "list_dir":
		return t.ListDir.Enabled
	case "message":
		return t.Message.Enabled
	case "read_file":
		return t.ReadFile.Enabled
	case "spawn":
		return t.Spawn.Enabled
	case "spi":
		return t.SPI.Enabled
	case "subagent":
		return t.Subagent.Enabled
	case "web_fetch":
		return t.WebFetch.Enabled
	case "send_file":
		return t.SendFile.Enabled
	case "write_file":
		return t.WriteFile.Enabled
	case "mcp":
		return t.MCP.Enabled
	default:
		return true
	}
}
