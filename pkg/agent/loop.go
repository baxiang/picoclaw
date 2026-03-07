// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

// Package agent 提供了 PicoClaw AI 代理的核心实现
// 主要功能包括：
// - AgentLoop: 代理主循环，处理消息队列、调用 LLM、执行工具
// - AgentInstance: 单个代理实例，包含工作空间、会话管理、上下文构建等
// - AgentRegistry: 代理注册表，管理多个代理实例和消息路由
//
// PicoClaw 是一个超轻量级个人 AI 助手，设计目标是在低成本硬件上运行
// 核心特点：
// - 支持多种通讯渠道（Telegram、Discord、微信等）
// - 支持多种 LLM 提供商（OpenAI、Anthropic、Gemini 等）
// - 可扩展的技能系统和工具调用机制
// - 会话记忆和自动摘要功能

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/bus"         // 消息总线，处理 inbound/outbound 消息
	"github.com/sipeed/picoclaw/pkg/channels"    // 通讯渠道管理（Telegram、Discord 等）
	"github.com/sipeed/picoclaw/pkg/commands"    // 内置命令系统（/help、/switch 等）
	"github.com/sipeed/picoclaw/pkg/config"      // 配置管理
	"github.com/sipeed/picoclaw/pkg/constants"   // 常量定义（内部频道标识等）
	"github.com/sipeed/picoclaw/pkg/logger"      // 日志系统
	"github.com/sipeed/picoclaw/pkg/mcp"         // MCP (Model Context Protocol) 管理
	"github.com/sipeed/picoclaw/pkg/media"       // 媒体文件存储和管理
	"github.com/sipeed/picoclaw/pkg/providers"   // LLM 提供商接口和实现
	"github.com/sipeed/picoclaw/pkg/routing"     // 消息路由系统
	"github.com/sipeed/picoclaw/pkg/skills"      // 技能系统
	"github.com/sipeed/picoclaw/pkg/state"       // 状态管理（持久化最后频道等）
	"github.com/sipeed/picoclaw/pkg/tools"       // 工具实现（文件操作、Shell、Web 搜索等）
	"github.com/sipeed/picoclaw/pkg/utils"       // 通用工具函数
	"github.com/sipeed/picoclaw/pkg/voice"       // 语音转写功能
)

// AgentLoop 是 AI 代理的主循环控制器
// 负责：
// - 从消息总线消费 inbound 消息
// - 调用 LLM 进行处理
// - 执行工具调用
// - 发布 outbound 响应
//
// 字段说明：
// - bus: 消息总线，用于接收和发送消息
// - cfg: 全局配置
// - registry: 代理注册表，管理多个代理实例
// - state: 状态管理器，持久化运行时状态
// - running: 原子布尔值，控制循环运行状态
// - summarizing: 并发安全的映射，跟踪正在摘要的会话
// - fallback: LLM 降级链，处理主模型失败时的备用方案
// - channelManager: 渠道管理器
// - mediaStore: 媒体文件存储器
// - transcriber: 语音转写器
// - cmdRegistry: 命令注册表（/help、/switch 等）
type AgentLoop struct {
	bus            *bus.MessageBus
	cfg            *config.Config
	registry       *AgentRegistry
	state          *state.Manager
	running        atomic.Bool
	summarizing    sync.Map
	fallback       *providers.FallbackChain
	channelManager *channels.Manager
	mediaStore     media.MediaStore
	transcriber    voice.Transcriber
	cmdRegistry    *commands.Registry
}

// processOptions 配置消息处理的方式
// 用于控制 runAgentLoop 函数的行为
type processOptions struct {
	SessionKey      string   // 会话标识符，用于加载/保存对话历史和上下文
	Channel         string   // 目标渠道名称，用于工具执行（如 telegram、discord）
	ChatID          string   // 目标聊天 ID，用于工具执行
	UserMessage     string   // 用户消息内容（可能包含命令前缀）
	Media           []string // 来自入站消息的媒体文件引用（media:// 格式）
	DefaultResponse string   // 当 LLM 返回空内容时的默认响应
	EnableSummary   bool     // 是否触发会话摘要（用于长对话压缩）
	SendResponse    bool     // 是否通过消息总线发送响应
	NoHistory       bool     // 如果为 true，不加载会话历史（用于心跳请求）
}

// 常量定义

// defaultResponse 是当 LLM 没有返回有效响应时的默认提示
// 通常发生在工具迭代次数用尽但没有产生对话内容时
const (
	defaultResponse           = "I've completed processing but have no response to give. Increase `max_tool_iterations` in config.json."
	sessionKeyAgentPrefix     = "agent:" // 会话键前缀，用于标识代理特定的会话
	metadataKeyAccountID      = "account_id"
	metadataKeyGuildID        = "guild_id"
	metadataKeyTeamID         = "team_id"
	metadataKeyParentPeerKind = "parent_peer_kind"
	metadataKeyParentPeerID   = "parent_peer_id"
)

// NewAgentLoop 创建并初始化一个新的 AgentLoop 实例
// 参数：
// - cfg: 全局配置对象
// - msgBus: 消息总线，用于接收和发送消息
// - provider: LLM 提供商接口实现
//
// 初始化流程：
// 1. 创建代理注册表，从配置加载所有代理实例
// 2. 为所有代理注册共享工具（web 搜索、消息发送、spawn 子代理等）
// 3. 设置降级链，用于 LLM 调用失败时的备用方案
// 4. 创建状态管理器，使用默认代理的工作空间
//
// 返回初始化好的 AgentLoop 指针
func NewAgentLoop(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	provider providers.LLMProvider,
) *AgentLoop {
	registry := NewAgentRegistry(cfg, provider)

	// 为所有代理注册共享工具
	registerSharedTools(cfg, msgBus, registry, provider)

	// 设置共享降级链
	cooldown := providers.NewCooldownTracker()
	fallbackChain := providers.NewFallbackChain(cooldown)

	// 使用默认代理的工作空间创建状态管理器（用于渠道记录）
	defaultAgent := registry.GetDefaultAgent()
	var stateManager *state.Manager
	if defaultAgent != nil {
		stateManager = state.NewManager(defaultAgent.Workspace)
	}

	al := &AgentLoop{
		bus:         msgBus,
		cfg:         cfg,
		registry:    registry,
		state:       stateManager,
		summarizing: sync.Map{},
		fallback:    fallbackChain,
		cmdRegistry: commands.NewRegistry(commands.BuiltinDefinitions()),
	}

	return al
}

// registerSharedTools 为所有代理注册共享工具
// 这些工具在所有代理实例之间共享，包括：
// - Web 搜索工具（支持多个搜索引擎：Brave、Tavily、DuckDuckGo、Perplexity、SearXNG、GLM）
// - Web 抓取工具（获取网页内容）
// - 硬件工具（I2C、SPI 总线操作，仅 Linux 可用）
// - 消息工具（发送消息到指定渠道）
// - 文件发送工具（发送媒体文件）
// - 技能发现和安装工具
// - Spawn 工具（创建子代理执行任务）
//
// 参数：
// - cfg: 全局配置
// - msgBus: 消息总线，用于消息工具的回调
// - registry: 代理注册表
// - provider: LLM 提供商
func registerSharedTools(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	registry *AgentRegistry,
	provider providers.LLMProvider,
) {
	// 遍历所有已注册的代理
	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok {
			continue
		}

		// --- Web 搜索工具 ---
		// 支持多个搜索引擎，可以同时配置多个，按优先级使用
		if cfg.Tools.IsToolEnabled("web") {
			searchTool, err := tools.NewWebSearchTool(tools.WebSearchToolOptions{
				BraveAPIKey:          cfg.Tools.Web.Brave.APIKey,
				BraveMaxResults:      cfg.Tools.Web.Brave.MaxResults,
				BraveEnabled:         cfg.Tools.Web.Brave.Enabled,
				TavilyAPIKey:         cfg.Tools.Web.Tavily.APIKey,
				TavilyBaseURL:        cfg.Tools.Web.Tavily.BaseURL,
				TavilyMaxResults:     cfg.Tools.Web.Tavily.MaxResults,
				TavilyEnabled:        cfg.Tools.Web.Tavily.Enabled,
				DuckDuckGoMaxResults: cfg.Tools.Web.DuckDuckGo.MaxResults,
				DuckDuckGoEnabled:    cfg.Tools.Web.DuckDuckGo.Enabled,
				PerplexityAPIKey:     cfg.Tools.Web.Perplexity.APIKey,
				PerplexityMaxResults: cfg.Tools.Web.Perplexity.MaxResults,
				PerplexityEnabled:    cfg.Tools.Web.Perplexity.Enabled,
				SearXNGBaseURL:       cfg.Tools.Web.SearXNG.BaseURL,
				SearXNGMaxResults:    cfg.Tools.Web.SearXNG.MaxResults,
				SearXNGEnabled:       cfg.Tools.Web.SearXNG.Enabled,
				GLMSearchAPIKey:      cfg.Tools.Web.GLMSearch.APIKey,
				GLMSearchBaseURL:     cfg.Tools.Web.GLMSearch.BaseURL,
				GLMSearchEngine:      cfg.Tools.Web.GLMSearch.SearchEngine,
				GLMSearchMaxResults:  cfg.Tools.Web.GLMSearch.MaxResults,
				GLMSearchEnabled:     cfg.Tools.Web.GLMSearch.Enabled,
				Proxy:                cfg.Tools.Web.Proxy,
			})
			if err != nil {
				logger.ErrorCF("agent", "Failed to create web search tool", map[string]any{"error": err.Error()})
			} else if searchTool != nil {
				agent.Tools.Register(searchTool)
			}
		}
		// Web 抓取工具：获取网页内容，有限制大小保护
		if cfg.Tools.IsToolEnabled("web_fetch") {
			fetchTool, err := tools.NewWebFetchToolWithProxy(50000, cfg.Tools.Web.Proxy, cfg.Tools.Web.FetchLimitBytes)
			if err != nil {
				logger.ErrorCF("agent", "Failed to create web fetch tool", map[string]any{"error": err.Error()})
			} else {
				agent.Tools.Register(fetchTool)
			}
		}

		// --- 硬件工具 ---
		// I2C 和 SPI 总线操作工具，仅 Linux 可用，其他平台返回错误
		if cfg.Tools.IsToolEnabled("i2c") {
			agent.Tools.Register(tools.NewI2CTool())
		}
		if cfg.Tools.IsToolEnabled("spi") {
			agent.Tools.Register(tools.NewSPITool())
		}

		// --- 消息工具 ---
		// 允许 AI 主动发送消息到指定渠道
		if cfg.Tools.IsToolEnabled("message") {
			messageTool := tools.NewMessageTool()
			// 设置发送回调，使用消息总线发送出站消息
			messageTool.SetSendCallback(func(channel, chatID, content string) error {
				pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer pubCancel()
				return msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
					Channel: channel,
					ChatID:  chatID,
					Content: content,
				})
			})
			agent.Tools.Register(messageTool)
		}

		// --- 文件发送工具 ---
		// 通过 MediaStore 发送媒体文件，store 在 SetMediaStore 时注入
		if cfg.Tools.IsToolEnabled("send_file") {
			sendFileTool := tools.NewSendFileTool(
				agent.Workspace,
				cfg.Agents.Defaults.RestrictToWorkspace,
				cfg.Agents.Defaults.GetMaxMediaSize(),
				nil,
			)
			agent.Tools.Register(sendFileTool)
		}

		// --- 技能发现和安装工具 ---
		// 支持从 ClawHub 等注册中心搜索和安装技能
		skills_enabled := cfg.Tools.IsToolEnabled("skills")
		find_skills_enable := cfg.Tools.IsToolEnabled("find_skills")
		install_skills_enable := cfg.Tools.IsToolEnabled("install_skill")
		if skills_enabled && (find_skills_enable || install_skills_enable) {
			registryMgr := skills.NewRegistryManagerFromConfig(skills.RegistryConfig{
				MaxConcurrentSearches: cfg.Tools.Skills.MaxConcurrentSearches,
				ClawHub:               skills.ClawHubConfig(cfg.Tools.Skills.Registries.ClawHub),
			})

			// 技能搜索工具（带缓存）
			if find_skills_enable {
				searchCache := skills.NewSearchCache(
					cfg.Tools.Skills.SearchCache.MaxSize,
					time.Duration(cfg.Tools.Skills.SearchCache.TTLSeconds)*time.Second,
				)
				agent.Tools.Register(tools.NewFindSkillsTool(registryMgr, searchCache))
			}

			// 技能安装工具
			if install_skills_enable {
				agent.Tools.Register(tools.NewInstallSkillTool(registryMgr, agent.Workspace))
			}
		}

		// --- Spawn 工具 ---
		// 创建子代理执行特定任务，需要 subagent 功能启用
		if cfg.Tools.IsToolEnabled("spawn") {
			if cfg.Tools.IsToolEnabled("subagent") {
				subagentManager := tools.NewSubagentManager(provider, agent.Model, agent.Workspace, msgBus)
				subagentManager.SetLLMOptions(agent.MaxTokens, agent.Temperature)
				spawnTool := tools.NewSpawnTool(subagentManager)
				currentAgentID := agentID
				// 设置白名单检查器，控制子代理可以 spawn 到哪些目标代理
				spawnTool.SetAllowlistChecker(func(targetAgentID string) bool {
					return registry.CanSpawnSubagent(currentAgentID, targetAgentID)
				})
				agent.Tools.Register(spawnTool)
			} else {
				logger.WarnCF("agent", "spawn tool requires subagent to be enabled", nil)
			}
		}
	}
}

// Run 启动 AgentLoop 的主运行循环
// 这是代理的核心入口点，负责持续监听和处理消息
//
// 参数：
// - ctx: 上下文，用于控制取消和超时
//
// 主要流程：
// 1. 设置运行状态为 true
// 2. 初始化 MCP 服务器（如果启用）
// 3. 进入主循环，持续消费 inbound 消息
// 4. 对每个消息调用 processMessage 进行处理
// 5. 处理响应（发送 outbound 消息或跳过）
// 6. 直到上下文取消或 running 被设为 false
//
// 返回：
// - 通常返回 nil，除非发生严重错误
func (al *AgentLoop) Run(ctx context.Context) error {
	al.running.Store(true)

	// --- 初始化 MCP 服务器 ---
	// MCP (Model Context Protocol) 允许扩展 AI 的能力
	if al.cfg.Tools.IsToolEnabled("mcp") {
		mcpManager := mcp.NewManager()
		// 确保退出时清理 MCP 连接资源
		defer func() {
			if err := mcpManager.Close(); err != nil {
				logger.ErrorCF("agent", "Failed to close MCP manager",
					map[string]any{
						"error": err.Error(),
					})
			}
		}()

		// 获取默认代理的工作空间路径
		defaultAgent := al.registry.GetDefaultAgent()
		var workspacePath string
		if defaultAgent != nil && defaultAgent.Workspace != "" {
			workspacePath = defaultAgent.Workspace
		} else {
			workspacePath = al.cfg.WorkspacePath()
		}

		// 从配置加载 MCP 服务器
		if err := mcpManager.LoadFromMCPConfig(ctx, al.cfg.Tools.MCP, workspacePath); err != nil {
			logger.WarnCF("agent", "Failed to load MCP servers, MCP tools will not be available",
				map[string]any{
					"error": err.Error(),
				})
		} else {
			// 为所有代理注册 MCP 工具
			servers := mcpManager.GetServers()
			uniqueTools := 0
			totalRegistrations := 0
			agentIDs := al.registry.ListAgentIDs()
			agentCount := len(agentIDs)

			for serverName, conn := range servers {
				uniqueTools += len(conn.Tools)
				for _, tool := range conn.Tools {
					for _, agentID := range agentIDs {
						agent, ok := al.registry.GetAgent(agentID)
						if !ok {
							continue
						}

						// 创建 MCP 工具并注册到每个代理
						mcpTool := tools.NewMCPTool(mcpManager, serverName, tool)
						agent.Tools.Register(mcpTool)
						totalRegistrations++
						logger.DebugCF("agent", "Registered MCP tool",
							map[string]any{
								"agent_id": agentID,
								"server":   serverName,
								"tool":     tool.Name,
								"name":     mcpTool.Name(),
							})
					}
				}
			}
			logger.InfoCF("agent", "MCP tools registered successfully",
				map[string]any{
					"server_count":        len(servers),
					"unique_tools":        uniqueTools,
					"total_registrations": totalRegistrations,
					"agent_count":         agentCount,
				})
		}
	}

	// --- 主消息循环 ---
	// 持续监听消息总线，处理 inbound 消息
	for al.running.Load() {
		select {
		case <-ctx.Done():
			// 上下文取消，优雅退出
			return nil
		default:
			msg, ok := al.bus.ConsumeInbound(ctx)
			if !ok {
				// 没有消息，继续循环
				continue
			}

			// 处理消息（使用闭包处理 defer 逻辑）
			func() {
				// TODO: 重新启用媒体清理功能
				// 当前禁用的原因是 inbound media 还没被 LLM 消费就被删除了

				response, err := al.processMessage(ctx, msg)
				if err != nil {
					response = fmt.Sprintf("Error processing message: %v", err)
				}

				// 如果有响应，发布到 outbound 消息总线
				if response != "" {
					// 检查消息工具是否已经在当前 round 发送了响应
					// 避免重复发送消息给用户
					alreadySent := false
					defaultAgent := al.registry.GetDefaultAgent()
					if defaultAgent != nil {
						if tool, ok := defaultAgent.Tools.Get("message"); ok {
							if mt, ok := tool.(*tools.MessageTool); ok {
								alreadySent = mt.HasSentInRound()
							}
						}
					}

					if !alreadySent {
						// 发布 outbound 响应
						al.bus.PublishOutbound(ctx, bus.OutboundMessage{
							Channel: msg.Channel,
							ChatID:  msg.ChatID,
							Content: response,
						})
						logger.InfoCF("agent", "Published outbound response",
							map[string]any{
								"channel":     msg.Channel,
								"chat_id":     msg.ChatID,
								"content_len": len(response),
							})
					} else {
						logger.DebugCF(
							"agent",
							"Skipped outbound (message tool already sent)",
							map[string]any{"channel": msg.Channel},
						)
					}
				}
			}()
		}
	}

	return nil
}

// Stop 停止 AgentLoop 的运行循环
// 通过将 running 标志设为 false 来通知主循环退出
func (al *AgentLoop) Stop() {
	al.running.Store(false)
}

// RegisterTool 向所有代理注册一个新工具
// 参数：
// - tool: 要实现的工具接口
func (al *AgentLoop) RegisterTool(tool tools.Tool) {
	for _, agentID := range al.registry.ListAgentIDs() {
		if agent, ok := al.registry.GetAgent(agentID); ok {
			agent.Tools.Register(tool)
		}
	}
}

// SetChannelManager 设置渠道管理器
// 用于管理各个通讯渠道的启停和配置
func (al *AgentLoop) SetChannelManager(cm *channels.Manager) {
	al.channelManager = cm
}

// SetMediaStore 注入 MediaStore 用于媒体文件生命周期管理
// 媒体文件包括图片、音频、视频等附件
//
// 参数：
// - s: MediaStore 实例，负责媒体文件的存储、解析和清理
func (al *AgentLoop) SetMediaStore(s media.MediaStore) {
	al.mediaStore = s

	// 传播 store 到所有代理的 send_file 工具
	al.registry.ForEachTool("send_file", func(t tools.Tool) {
		if sf, ok := t.(*tools.SendFileTool); ok {
			sf.SetMediaStore(s)
		}
	})
}

// SetTranscriber 注入语音转写器用于代理级别的音频转录
// 参数：
// - t: Transcriber 实例，负责将音频文件转换为文本
func (al *AgentLoop) SetTranscriber(t voice.Transcriber) {
	al.transcriber = t
}

// audioAnnotationRe 匹配音频注释的正则表达式
// 匹配格式：[voice]、[audio]、[voice:xxx]、[audio:xxx] 等
var audioAnnotationRe = regexp.MustCompile(`\[(voice|audio)(?::[^\]]*)?\]`)

// transcribeAudioInMessage 解析音频媒体引用，转写音频内容，
// 并用转写文本替换 msg.Content 中的音频注释
//
// 处理流程：
// 1. 遍历 msg.Media 中的所有媒体引用
// 2. 解析每个引用获取文件路径和元数据
// 3. 对音频文件调用转写器进行语音识别
// 4. 将音频注释 [voice] 或 [audio] 替换为转写文本
//
// 参数：
// - ctx: 上下文用于取消控制
// - msg: 入站消息，包含媒体引用
//
// 返回：
// - 处理后的入站消息（音频注释已替换为文本）
func (al *AgentLoop) transcribeAudioInMessage(ctx context.Context, msg bus.InboundMessage) bus.InboundMessage {
	if al.transcriber == nil || al.mediaStore == nil || len(msg.Media) == 0 {
		return msg
	}

	// 按顺序转写每个音频媒体引用
	var transcriptions []string
	for _, ref := range msg.Media {
		path, meta, err := al.mediaStore.ResolveWithMeta(ref)
		if err != nil {
			logger.WarnCF("voice", "Failed to resolve media ref", map[string]any{"ref": ref, "error": err})
			continue
		}
		// 只转写音频文件，跳过其他类型
		if !utils.IsAudioFile(meta.Filename, meta.ContentType) {
			continue
		}
		result, err := al.transcriber.Transcribe(ctx, path)
		if err != nil {
			logger.WarnCF("voice", "Transcription failed", map[string]any{"ref": ref, "error": err})
			transcriptions = append(transcriptions, "")
			continue
		}
		transcriptions = append(transcriptions, result.Text)
	}

	if len(transcriptions) == 0 {
		return msg
	}

	// 按顺序替换音频注释
	idx := 0
	newContent := audioAnnotationRe.ReplaceAllStringFunc(msg.Content, func(match string) string {
		if idx >= len(transcriptions) {
			return match
		}
		text := transcriptions[idx]
		idx++
		return "[voice: " + text + "]"
	})

	// 追加任何未被注释匹配的剩余转写文本
	for ; idx < len(transcriptions); idx++ {
		newContent += "\n[voice: " + transcriptions[idx] + "]"
	}

	msg.Content = newContent
	return msg
}

// inferMediaType 根据文件名和 MIME 类型推断媒体类型
// 支持的类型：image（图片）、audio（音频）、video（视频）、file（其他文件）
//
// 参数：
// - filename: 文件名（包括扩展名）
// - contentType: MIME 内容类型（如 "image/jpeg"）
//
// 返回：
// - 媒体类型字符串："image"、"audio"、"video" 或 "file"
func inferMediaType(filename, contentType string) string {
	ct := strings.ToLower(contentType)
	fn := strings.ToLower(filename)

	// 首先根据 MIME 类型判断
	if strings.HasPrefix(ct, "image/") {
		return "image"
	}
	if strings.HasPrefix(ct, "audio/") || ct == "application/ogg" {
		return "audio"
	}
	if strings.HasPrefix(ct, "video/") {
		return "video"
	}

	// 根据文件扩展名判断（后备逻辑）
	ext := filepath.Ext(fn)
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg":
		return "image"
	case ".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac", ".wma", ".opus":
		return "audio"
	case ".mp4", ".avi", ".mov", ".webm", ".mkv":
		return "video"
	}

	return "file"
}

// RecordLastChannel 记录此工作空间最后活跃的渠道
// 使用原子状态保存机制防止崩溃时数据丢失
//
// 参数：
// - channel: 渠道标识（格式："channel:chatID"）
//
// 返回：
// - 错误信息（如果有）
func (al *AgentLoop) RecordLastChannel(channel string) error {
	if al.state == nil {
		return nil
	}
	return al.state.SetLastChannel(channel)
}

// RecordLastChatID 记录此工作空间最后活跃的聊天 ID
// 使用原子状态保存机制防止崩溃时数据丢失
//
// 参数：
// - chatID: 聊天标识符
//
// 返回：
// - 错误信息（如果有）
func (al *AgentLoop) RecordLastChatID(chatID string) error {
	if al.state == nil {
		return nil
	}
	return al.state.SetLastChatID(chatID)
}

// ProcessDirect 直接处理一条消息（用于命令行或直接调用）
// 使用默认渠道 "cli" 和会话 ID "direct"
//
// 参数：
// - ctx: 上下文用于取消控制
// - content: 用户消息内容
// - sessionKey: 会话标识符，用于加载/保存对话历史
//
// 返回：
// - 响应内容字符串
// - 错误信息（如果有）
func (al *AgentLoop) ProcessDirect(
	ctx context.Context,
	content, sessionKey string,
) (string, error) {
	return al.ProcessDirectWithChannel(ctx, content, sessionKey, "cli", "direct")
}

// ProcessDirectWithChannel 带渠道信息直接处理一条消息
// 用于定时任务等需要指定渠道的场景
//
// 参数：
// - ctx: 上下文用于取消控制
// - content: 用户消息内容
// - sessionKey: 会话标识符
// - channel: 渠道名称
// - chatID: 聊天标识符
//
// 返回：
// - 响应内容字符串
// - 错误信息（如果有）
func (al *AgentLoop) ProcessDirectWithChannel(
	ctx context.Context,
	content, sessionKey, channel, chatID string,
) (string, error) {
	msg := bus.InboundMessage{
		Channel:    channel,
		SenderID:   "cron",
		ChatID:     chatID,
		Content:    content,
		SessionKey: sessionKey,
	}

	return al.processMessage(ctx, msg)
}

// ProcessHeartbeat 处理心跳请求，不加载会话历史
// 每个心跳请求都是独立的，不累积上下文
// 用于定期活跃性检查或通知
//
// 参数：
// - ctx: 上下文用于取消控制
// - content: 心跳消息内容
// - channel: 渠道名称
// - chatID: 聊天标识符
//
// 返回：
// - 响应内容字符串
// - 错误信息（如果有）
func (al *AgentLoop) ProcessHeartbeat(
	ctx context.Context,
	content, channel, chatID string,
) (string, error) {
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		return "", fmt.Errorf("no default agent for heartbeat")
	}
	return al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      "heartbeat",
		Channel:         channel,
		ChatID:          chatID,
		UserMessage:     content,
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
		NoHistory:       true, // 不为心跳请求加载会话历史
	})
}

// processMessage 处理入站消息的核心函数
// 负责消息的路由、转写、命令处理和 LLM 循环执行
//
// 处理流程：
// 1. 记录消息预览到日志（错误消息显示完整内容）
// 2. 对消息进行音频转写（如果有音频附件）
// 3. 系统消息路由到 processSystemMessage 处理
// 4. 解析消息路由，确定处理代理
// 5. 检查并处理内置命令（/help、/switch 等）
// 6. 重置消息工具状态避免重复发送
// 7. 解析会话键
// 8. 调用 runAgentLoop 执行 LLM 处理
//
// 参数：
// - ctx: 上下文用于取消控制
// - msg: 入站消息
//
// 返回：
// - 响应内容字符串
// - 错误信息（如果有）
func (al *AgentLoop) processMessage(ctx context.Context, msg bus.InboundMessage) (string, error) {
	// 记录消息预览到日志（错误消息显示完整内容）
	var logContent string
	if strings.Contains(msg.Content, "Error:") || strings.Contains(msg.Content, "error") {
		logContent = msg.Content // 错误消息显示完整内容
	} else {
		logContent = utils.Truncate(msg.Content, 80)
	}
	logger.InfoCF(
		"agent",
		fmt.Sprintf("Processing message from %s:%s: %s", msg.Channel, msg.SenderID, logContent),
		map[string]any{
			"channel":     msg.Channel,
			"chat_id":     msg.ChatID,
			"sender_id":   msg.SenderID,
			"session_key": msg.SessionKey,
		},
	)

	// 对消息进行音频转写（如果有音频附件）
	msg = al.transcribeAudioInMessage(ctx, msg)

	// 系统消息路由到 processSystemMessage 处理
	if msg.Channel == "system" {
		return al.processSystemMessage(ctx, msg)
	}

	// 解析消息路由，确定处理代理
	route, agent, routeErr := al.resolveMessageRoute(msg)

	// 在路由失败前检查命令
	// 全局命令（/help、/show、/switch）即使路由失败也能工作
	// 上下文相关命令检查其 Runtime 字段，在能力为 nil 时报"不可用"
	if response, handled := al.handleCommand(ctx, msg, agent); handled {
		return response, nil
	}

	if routeErr != nil {
		return "", routeErr
	}

	// 重置消息工具状态，避免上一轮的状态影响本轮发送
	if tool, ok := agent.Tools.Get("message"); ok {
		if resetter, ok := tool.(interface{ ResetSentInRound() }); ok {
			resetter.ResetSentInRound()
		}
	}

	// 从路由解析会话键，同时保留显式的代理作用域键
	scopeKey := resolveScopeKey(route, msg.SessionKey)
	sessionKey := scopeKey

	logger.InfoCF("agent", "Routed message",
		map[string]any{
			"agent_id":      agent.ID,
			"scope_key":     scopeKey,
			"session_key":   sessionKey,
			"matched_by":    route.MatchedBy,
			"route_agent":   route.AgentID,
			"route_channel": route.Channel,
		})

	return al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      sessionKey,
		Channel:         msg.Channel,
		ChatID:          msg.ChatID,
		UserMessage:     msg.Content,
		Media:           msg.Media,
		DefaultResponse: defaultResponse,
		EnableSummary:   true,
		SendResponse:    false,
	})
}

// resolveMessageRoute 解析消息的路由，确定处理代理
// 根据消息的渠道、账户 ID、对等方信息等匹配绑定的代理
//
// 参数：
// - msg: 入站消息
//
// 返回：
// - 解析的路由信息
// - 代理实例
// - 错误信息（如果有）
func (al *AgentLoop) resolveMessageRoute(msg bus.InboundMessage) (routing.ResolvedRoute, *AgentInstance, error) {
	route := al.registry.ResolveRoute(routing.RouteInput{
		Channel:    msg.Channel,
		AccountID:  inboundMetadata(msg, metadataKeyAccountID),
		Peer:       extractPeer(msg),
		ParentPeer: extractParentPeer(msg),
		GuildID:    inboundMetadata(msg, metadataKeyGuildID),
		TeamID:     inboundMetadata(msg, metadataKeyTeamID),
	})

	// 获取路由对应的代理，如果没有则使用默认代理
	agent, ok := al.registry.GetAgent(route.AgentID)
	if !ok {
		agent = al.registry.GetDefaultAgent()
	}
	if agent == nil {
		return routing.ResolvedRoute{}, nil, fmt.Errorf("no agent available for route (agent_id=%s)", route.AgentID)
	}

	return route, agent, nil
}

// resolveScopeKey 解析会话键，保留显式的代理作用域键
// 如果消息已经包含 agent: 前缀的会话键，则保持不变
// 否则使用路由生成的会话键
func resolveScopeKey(route routing.ResolvedRoute, msgSessionKey string) string {
	if msgSessionKey != "" && strings.HasPrefix(msgSessionKey, sessionKeyAgentPrefix) {
		return msgSessionKey
	}
	return route.SessionKey
}

// processSystemMessage 处理系统消息
// 系统消息通常来自子代理任务完成通知或其他后台任务
//
// 处理流程：
// 1. 验证消息渠道是否为 "system"
// 2. 解析原始渠道和聊天 ID（从 chat_id 字段）
// 3. 提取子代理结果（去掉任务完成前缀）
// 4. 跳过内部渠道（不发送给用户）
// 5. 使用默认代理处理并发送响应
//
// 参数：
// - ctx: 上下文用于取消控制
// - msg: 入站系统消息
//
// 返回：
// - 响应内容字符串
// - 错误信息（如果有）
func (al *AgentLoop) processSystemMessage(
	ctx context.Context,
	msg bus.InboundMessage,
) (string, error) {
	if msg.Channel != "system" {
		return "", fmt.Errorf(
			"processSystemMessage called with non-system message channel: %s",
			msg.Channel,
		)
	}

	logger.InfoCF("agent", "Processing system message",
		map[string]any{
			"sender_id": msg.SenderID,
			"chat_id":   msg.ChatID,
		})

	// 从 chat_id 解析原始渠道（格式："channel:chat_id"）
	var originChannel, originChatID string
	if idx := strings.Index(msg.ChatID, ":"); idx > 0 {
		originChannel = msg.ChatID[:idx]
		originChatID = msg.ChatID[idx+1:]
	} else {
		originChannel = "cli"
		originChatID = msg.ChatID
	}

	// 从消息内容提取子代理结果
	// 格式："Task 'label' completed.\n\nResult:\n<actual content>"
	content := msg.Content
	if idx := strings.Index(content, "Result:\n"); idx >= 0 {
		content = content[idx+8:] // 只提取结果部分
	}

	// 跳过内部渠道 - 只记录不发送给用户
	if constants.IsInternalChannel(originChannel) {
		logger.InfoCF("agent", "Subagent completed (internal channel)",
			map[string]any{
				"sender_id":   msg.SenderID,
				"content_len": len(content),
				"channel":     originChannel,
			})
		return "", nil
	}

	// 使用默认代理处理系统消息
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		return "", fmt.Errorf("no default agent for system message")
	}

	// 使用原始会话作为上下文
	sessionKey := routing.BuildAgentMainSessionKey(agent.ID)

	return al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      sessionKey,
		Channel:         originChannel,
		ChatID:          originChatID,
		UserMessage:     fmt.Sprintf("[System: %s] %s", msg.SenderID, msg.Content),
		DefaultResponse: "Background task completed.",
		EnableSummary:   false,
		SendResponse:    true,
	})
}

// runAgentLoop 是核心消息处理逻辑
// 执行完整的 AI 代理处理流程，包括：
// 1. 记录最后渠道（用于心跳通知）
// 2. 构建消息（加载历史、摘要、上下文）
// 3. 解析媒体引用
// 4. 保存用户消息到会话
// 5. 运行 LLM 迭代循环
// 6. 处理空响应
// 7. 保存助理消息到会话
// 8. 可选的会话摘要
// 9. 可选的发送响应
// 10. 日志记录
//
// 参数：
// - ctx: 上下文用于取消控制
// - agent: 代理实例
// - opts: 处理选项配置
//
// 返回：
// - 最终响应内容
// - 错误信息（如果有）
func (al *AgentLoop) runAgentLoop(
	ctx context.Context,
	agent *AgentInstance,
	opts processOptions,
) (string, error) {
	// 步骤 0: 记录最后活跃渠道（用于心跳通知），跳过内部渠道
	if opts.Channel != "" && opts.ChatID != "" {
		// 不记录内部渠道（cli、system、subagent）
		if !constants.IsInternalChannel(opts.Channel) {
			channelKey := fmt.Sprintf("%s:%s", opts.Channel, opts.ChatID)
			if err := al.RecordLastChannel(channelKey); err != nil {
				logger.WarnCF(
					"agent",
					"Failed to record last channel",
					map[string]any{"error": err.Error()},
				)
			}
		}
	}

	// 步骤 1: 构建消息（心跳请求跳过历史加载）
	var history []providers.Message
	var summary string
	if !opts.NoHistory {
		history = agent.Sessions.GetHistory(opts.SessionKey)
		summary = agent.Sessions.GetSummary(opts.SessionKey)
	}
	messages := agent.ContextBuilder.BuildMessages(
		history,
		summary,
		opts.UserMessage,
		opts.Media,
		opts.Channel,
		opts.ChatID,
	)

	// 将 media:// 引用解析为 base64 数据 URL（用于流式传输）
	maxMediaSize := al.cfg.Agents.Defaults.GetMaxMediaSize()
	messages = resolveMediaRefs(messages, al.mediaStore, maxMediaSize)

	// 步骤 2: 保存用户消息到会话
	agent.Sessions.AddMessage(opts.SessionKey, "user", opts.UserMessage)

	// 步骤 3: 运行 LLM 迭代循环
	finalContent, iteration, err := al.runLLMIteration(ctx, agent, messages, opts)
	if err != nil {
		return "", err
	}

	// 如果最后一个工具有 ForUser 内容且已发送，可能不需要发送最终响应
	// 这由工具的 Silent 标志和 ForUser 内容控制

	// 步骤 4: 处理空响应
	if finalContent == "" {
		finalContent = opts.DefaultResponse
	}

	// 步骤 5: 保存最终助理消息到会话
	agent.Sessions.AddMessage(opts.SessionKey, "assistant", finalContent)
	agent.Sessions.Save(opts.SessionKey)

	// 步骤 6: 可选的会话摘要
	if opts.EnableSummary {
		al.maybeSummarize(agent, opts.SessionKey, opts.Channel, opts.ChatID)
	}

	// 步骤 7: 可选的通过消息总线发送响应
	if opts.SendResponse {
		al.bus.PublishOutbound(ctx, bus.OutboundMessage{
			Channel: opts.Channel,
			ChatID:  opts.ChatID,
			Content: finalContent,
		})
	}

	// 步骤 8: 记录响应日志
	responsePreview := utils.Truncate(finalContent, 120)
	logger.InfoCF("agent", fmt.Sprintf("Response: %s", responsePreview),
		map[string]any{
			"agent_id":     agent.ID,
			"session_key":  opts.SessionKey,
			"iterations":   iteration,
			"final_length": len(finalContent),
		})

	return finalContent, nil
}

// targetReasoningChannelID 获取指定渠道的推理渠道 ID
// 推理渠道用于发送 LLM 的思考过程（如果支持）
//
// 参数：
// - channelName: 渠道名称
//
// 返回：
// - 推理渠道 ID，如果渠道管理器未初始化或渠道不存在则返回空字符串
func (al *AgentLoop) targetReasoningChannelID(channelName string) (chatID string) {
	if al.channelManager == nil {
		return ""
	}
	if ch, ok := al.channelManager.GetChannel(channelName); ok {
		return ch.ReasoningChannelID()
	}
	return ""
}

// handleReasoning 处理 LLM 推理内容的发送
// 将 LLM 的思考过程发送到指定的推理渠道
// 这是一个最佳努力的操作，如果总线满或上下文取消会丢弃
//
// 参数：
// - ctx: 上下文用于取消控制
// - reasoningContent: 推理内容
// - channelName: 渠道名称
// - channelID: 渠道 ID
func (al *AgentLoop) handleReasoning(
	ctx context.Context,
	reasoningContent, channelName, channelID string,
) {
	if reasoningContent == "" || channelName == "" || channelID == "" {
		return
	}

	// 在尝试发布前检查上下文取消
	// 因为 PublishOutbound 的 select 可能在发送和 ctx.Done() 之间有竞争
	if ctx.Err() != nil {
		return
	}

	// 使用短超时防止 goroutine 在 outbound 总线满时无限阻塞
	// 推理输出是最佳努力的，丢弃它可以接受以避免 goroutine 堆积
	pubCtx, pubCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pubCancel()

	if err := al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Channel: channelName,
		ChatID:  channelID,
		Content: reasoningContent,
	}); err != nil {
		// 将 context.DeadlineExceeded / context.Canceled 视为预期错误
		// （总线满或父上下文取消）
		// 检查错误本身而不是 ctx.Err()，因为 pubCtx 可能超时（5 秒）而父上下文仍活跃
		// ErrBusClosed 也视为预期错误 —— 正常关闭时总线可能在所有 goroutine 完成前关闭
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
			errors.Is(err, bus.ErrBusClosed) {
			logger.DebugCF("agent", "Reasoning publish skipped (timeout/cancel)", map[string]any{
				"channel": channelName,
				"error":   err.Error(),
			})
		} else {
			logger.WarnCF("agent", "Failed to publish reasoning (best-effort)", map[string]any{
				"channel": channelName,
				"error":   err.Error(),
			})
		}
	}
}

// runLLMIteration executes the LLM call loop with tool handling.
func (al *AgentLoop) runLLMIteration(
	ctx context.Context,
	agent *AgentInstance,
	messages []providers.Message,
	opts processOptions,
) (string, int, error) {
	iteration := 0
	var finalContent string

	// Determine effective model tier for this conversation turn.
	// selectCandidates evaluates routing once and the decision is sticky for
	// all tool-follow-up iterations within the same turn so that a multi-step
	// tool chain doesn't switch models mid-way through.
	activeCandidates, activeModel := al.selectCandidates(agent, opts.UserMessage, messages)

	for iteration < agent.MaxIterations {
		iteration++

		logger.DebugCF("agent", "LLM iteration",
			map[string]any{
				"agent_id":  agent.ID,
				"iteration": iteration,
				"max":       agent.MaxIterations,
			})

		// Build tool definitions
		providerToolDefs := agent.Tools.ToProviderDefs()

		// Log LLM request details
		logger.DebugCF("agent", "LLM request",
			map[string]any{
				"agent_id":          agent.ID,
				"iteration":         iteration,
				"model":             activeModel,
				"messages_count":    len(messages),
				"tools_count":       len(providerToolDefs),
				"max_tokens":        agent.MaxTokens,
				"temperature":       agent.Temperature,
				"system_prompt_len": len(messages[0].Content),
			})

		// Log full messages (detailed)
		logger.DebugCF("agent", "Full LLM request",
			map[string]any{
				"iteration":     iteration,
				"messages_json": formatMessagesForLog(messages),
				"tools_json":    formatToolsForLog(providerToolDefs),
			})

		// Call LLM with fallback chain if multiple candidates are configured.
		var response *providers.LLMResponse
		var err error

		llmOpts := map[string]any{
			"max_tokens":       agent.MaxTokens,
			"temperature":      agent.Temperature,
			"prompt_cache_key": agent.ID,
		}
		// parseThinkingLevel guarantees ThinkingOff for empty/unknown values,
		// so checking != ThinkingOff is sufficient.
		if agent.ThinkingLevel != ThinkingOff {
			if tc, ok := agent.Provider.(providers.ThinkingCapable); ok && tc.SupportsThinking() {
				llmOpts["thinking_level"] = string(agent.ThinkingLevel)
			} else {
				logger.WarnCF("agent", "thinking_level is set but current provider does not support it, ignoring",
					map[string]any{"agent_id": agent.ID, "thinking_level": string(agent.ThinkingLevel)})
			}
		}

		callLLM := func() (*providers.LLMResponse, error) {
			if len(activeCandidates) > 1 && al.fallback != nil {
				fbResult, fbErr := al.fallback.Execute(
					ctx,
					activeCandidates,
					func(ctx context.Context, provider, model string) (*providers.LLMResponse, error) {
						return agent.Provider.Chat(ctx, messages, providerToolDefs, model, llmOpts)
					},
				)
				if fbErr != nil {
					return nil, fbErr
				}
				if fbResult.Provider != "" && len(fbResult.Attempts) > 0 {
					logger.InfoCF(
						"agent",
						fmt.Sprintf("Fallback: succeeded with %s/%s after %d attempts",
							fbResult.Provider, fbResult.Model, len(fbResult.Attempts)+1),
						map[string]any{"agent_id": agent.ID, "iteration": iteration},
					)
				}
				return fbResult.Response, nil
			}
			return agent.Provider.Chat(ctx, messages, providerToolDefs, activeModel, llmOpts)
		}

		// Retry loop for context/token errors
		maxRetries := 2
		for retry := 0; retry <= maxRetries; retry++ {
			response, err = callLLM()
			if err == nil {
				break
			}

			errMsg := strings.ToLower(err.Error())

			// Check if this is a network/HTTP timeout — not a context window error.
			isTimeoutError := errors.Is(err, context.DeadlineExceeded) ||
				strings.Contains(errMsg, "deadline exceeded") ||
				strings.Contains(errMsg, "client.timeout") ||
				strings.Contains(errMsg, "timed out") ||
				strings.Contains(errMsg, "timeout exceeded")

			// Detect real context window / token limit errors, excluding network timeouts.
			isContextError := !isTimeoutError && (strings.Contains(errMsg, "context_length_exceeded") ||
				strings.Contains(errMsg, "context window") ||
				strings.Contains(errMsg, "maximum context length") ||
				strings.Contains(errMsg, "token limit") ||
				strings.Contains(errMsg, "too many tokens") ||
				strings.Contains(errMsg, "max_tokens") ||
				strings.Contains(errMsg, "invalidparameter") ||
				strings.Contains(errMsg, "prompt is too long") ||
				strings.Contains(errMsg, "request too large"))

			if isTimeoutError && retry < maxRetries {
				backoff := time.Duration(retry+1) * 5 * time.Second
				logger.WarnCF("agent", "Timeout error, retrying after backoff", map[string]any{
					"error":   err.Error(),
					"retry":   retry,
					"backoff": backoff.String(),
				})
				time.Sleep(backoff)
				continue
			}

			if isContextError && retry < maxRetries {
				logger.WarnCF(
					"agent",
					"Context window error detected, attempting compression",
					map[string]any{
						"error": err.Error(),
						"retry": retry,
					},
				)

				if retry == 0 && !constants.IsInternalChannel(opts.Channel) {
					al.bus.PublishOutbound(ctx, bus.OutboundMessage{
						Channel: opts.Channel,
						ChatID:  opts.ChatID,
						Content: "Context window exceeded. Compressing history and retrying...",
					})
				}

				al.forceCompression(agent, opts.SessionKey)
				newHistory := agent.Sessions.GetHistory(opts.SessionKey)
				newSummary := agent.Sessions.GetSummary(opts.SessionKey)
				messages = agent.ContextBuilder.BuildMessages(
					newHistory, newSummary, "",
					nil, opts.Channel, opts.ChatID,
				)
				continue
			}
			break
		}

		if err != nil {
			logger.ErrorCF("agent", "LLM call failed",
				map[string]any{
					"agent_id":  agent.ID,
					"iteration": iteration,
					"error":     err.Error(),
				})
			return "", iteration, fmt.Errorf("LLM call failed after retries: %w", err)
		}

		go al.handleReasoning(
			ctx,
			response.Reasoning,
			opts.Channel,
			al.targetReasoningChannelID(opts.Channel),
		)

		logger.DebugCF("agent", "LLM response",
			map[string]any{
				"agent_id":       agent.ID,
				"iteration":      iteration,
				"content_chars":  len(response.Content),
				"tool_calls":     len(response.ToolCalls),
				"reasoning":      response.Reasoning,
				"target_channel": al.targetReasoningChannelID(opts.Channel),
				"channel":        opts.Channel,
			})
		// Check if no tool calls - we're done
		if len(response.ToolCalls) == 0 {
			finalContent = response.Content
			logger.InfoCF("agent", "LLM response without tool calls (direct answer)",
				map[string]any{
					"agent_id":      agent.ID,
					"iteration":     iteration,
					"content_chars": len(finalContent),
				})
			break
		}

		normalizedToolCalls := make([]providers.ToolCall, 0, len(response.ToolCalls))
		for _, tc := range response.ToolCalls {
			normalizedToolCalls = append(normalizedToolCalls, providers.NormalizeToolCall(tc))
		}

		// Log tool calls
		toolNames := make([]string, 0, len(normalizedToolCalls))
		for _, tc := range normalizedToolCalls {
			toolNames = append(toolNames, tc.Name)
		}
		logger.InfoCF("agent", "LLM requested tool calls",
			map[string]any{
				"agent_id":  agent.ID,
				"tools":     toolNames,
				"count":     len(normalizedToolCalls),
				"iteration": iteration,
			})

		// Build assistant message with tool calls
		assistantMsg := providers.Message{
			Role:             "assistant",
			Content:          response.Content,
			ReasoningContent: response.ReasoningContent,
		}
		for _, tc := range normalizedToolCalls {
			argumentsJSON, _ := json.Marshal(tc.Arguments)
			// Copy ExtraContent to ensure thought_signature is persisted for Gemini 3
			extraContent := tc.ExtraContent
			thoughtSignature := ""
			if tc.Function != nil {
				thoughtSignature = tc.Function.ThoughtSignature
			}

			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, providers.ToolCall{
				ID:   tc.ID,
				Type: "function",
				Name: tc.Name,
				Function: &providers.FunctionCall{
					Name:             tc.Name,
					Arguments:        string(argumentsJSON),
					ThoughtSignature: thoughtSignature,
				},
				ExtraContent:     extraContent,
				ThoughtSignature: thoughtSignature,
			})
		}
		messages = append(messages, assistantMsg)

		// Save assistant message with tool calls to session
		agent.Sessions.AddFullMessage(opts.SessionKey, assistantMsg)

		// Execute tool calls in parallel
		type indexedAgentResult struct {
			result *tools.ToolResult
			tc     providers.ToolCall
		}

		agentResults := make([]indexedAgentResult, len(normalizedToolCalls))
		var wg sync.WaitGroup

		for i, tc := range normalizedToolCalls {
			agentResults[i].tc = tc

			wg.Add(1)
			go func(idx int, tc providers.ToolCall) {
				defer wg.Done()

				argsJSON, _ := json.Marshal(tc.Arguments)
				argsPreview := utils.Truncate(string(argsJSON), 200)
				logger.InfoCF("agent", fmt.Sprintf("Tool call: %s(%s)", tc.Name, argsPreview),
					map[string]any{
						"agent_id":  agent.ID,
						"tool":      tc.Name,
						"iteration": iteration,
					})

				// Create async callback for tools that implement AsyncExecutor
				asyncCallback := func(callbackCtx context.Context, result *tools.ToolResult) {
					if !result.Silent && result.ForUser != "" {
						logger.InfoCF("agent", "Async tool completed, agent will handle notification",
							map[string]any{
								"tool":        tc.Name,
								"content_len": len(result.ForUser),
							})
					}
				}

				toolResult := agent.Tools.ExecuteWithContext(
					ctx,
					tc.Name,
					tc.Arguments,
					opts.Channel,
					opts.ChatID,
					asyncCallback,
				)
				agentResults[idx].result = toolResult
			}(i, tc)
		}
		wg.Wait()

		// Process results in original order (send to user, save to session)
		for _, r := range agentResults {
			// Send ForUser content to user immediately if not Silent
			if !r.result.Silent && r.result.ForUser != "" && opts.SendResponse {
				al.bus.PublishOutbound(ctx, bus.OutboundMessage{
					Channel: opts.Channel,
					ChatID:  opts.ChatID,
					Content: r.result.ForUser,
				})
				logger.DebugCF("agent", "Sent tool result to user",
					map[string]any{
						"tool":        r.tc.Name,
						"content_len": len(r.result.ForUser),
					})
			}

			// If tool returned media refs, publish them as outbound media
			if len(r.result.Media) > 0 {
				parts := make([]bus.MediaPart, 0, len(r.result.Media))
				for _, ref := range r.result.Media {
					part := bus.MediaPart{Ref: ref}
					if al.mediaStore != nil {
						if _, meta, err := al.mediaStore.ResolveWithMeta(ref); err == nil {
							part.Filename = meta.Filename
							part.ContentType = meta.ContentType
							part.Type = inferMediaType(meta.Filename, meta.ContentType)
						}
					}
					parts = append(parts, part)
				}
				al.bus.PublishOutboundMedia(ctx, bus.OutboundMediaMessage{
					Channel: opts.Channel,
					ChatID:  opts.ChatID,
					Parts:   parts,
				})
			}

			// Determine content for LLM based on tool result
			contentForLLM := r.result.ForLLM
			if contentForLLM == "" && r.result.Err != nil {
				contentForLLM = r.result.Err.Error()
			}

			toolResultMsg := providers.Message{
				Role:       "tool",
				Content:    contentForLLM,
				ToolCallID: r.tc.ID,
			}
			messages = append(messages, toolResultMsg)

			// Save tool result message to session
			agent.Sessions.AddFullMessage(opts.SessionKey, toolResultMsg)
		}
	}

	return finalContent, iteration, nil
}

// selectCandidates returns the model candidates and resolved model name to use
// for a conversation turn. When model routing is configured and the incoming
// message scores below the complexity threshold, it returns the light model
// candidates instead of the primary ones.
//
// The returned (candidates, model) pair is used for all LLM calls within one
// turn — tool follow-up iterations use the same tier as the initial call so
// that a multi-step tool chain doesn't switch models mid-way.
func (al *AgentLoop) selectCandidates(
	agent *AgentInstance,
	userMsg string,
	history []providers.Message,
) (candidates []providers.FallbackCandidate, model string) {
	if agent.Router == nil || len(agent.LightCandidates) == 0 {
		return agent.Candidates, agent.Model
	}

	_, usedLight, score := agent.Router.SelectModel(userMsg, history, agent.Model)
	if !usedLight {
		logger.DebugCF("agent", "Model routing: primary model selected",
			map[string]any{
				"agent_id":  agent.ID,
				"score":     score,
				"threshold": agent.Router.Threshold(),
			})
		return agent.Candidates, agent.Model
	}

	logger.InfoCF("agent", "Model routing: light model selected",
		map[string]any{
			"agent_id":    agent.ID,
			"light_model": agent.Router.LightModel(),
			"score":       score,
			"threshold":   agent.Router.Threshold(),
		})
	return agent.LightCandidates, agent.Router.LightModel()
}

// maybeSummarize triggers summarization if the session history exceeds thresholds.
func (al *AgentLoop) maybeSummarize(agent *AgentInstance, sessionKey, channel, chatID string) {
	newHistory := agent.Sessions.GetHistory(sessionKey)
	tokenEstimate := al.estimateTokens(newHistory)
	threshold := agent.ContextWindow * agent.SummarizeTokenPercent / 100

	if len(newHistory) > agent.SummarizeMessageThreshold || tokenEstimate > threshold {
		summarizeKey := agent.ID + ":" + sessionKey
		if _, loading := al.summarizing.LoadOrStore(summarizeKey, true); !loading {
			go func() {
				defer al.summarizing.Delete(summarizeKey)
				logger.Debug("Memory threshold reached. Optimizing conversation history...")
				al.summarizeSession(agent, sessionKey)
			}()
		}
	}
}

// forceCompression aggressively reduces context when the limit is hit.
// It drops the oldest 50% of messages (keeping system prompt and last user message).
func (al *AgentLoop) forceCompression(agent *AgentInstance, sessionKey string) {
	history := agent.Sessions.GetHistory(sessionKey)
	if len(history) <= 4 {
		return
	}

	// Keep system prompt (usually [0]) and the very last message (user's trigger)
	// We want to drop the oldest half of the *conversation*
	// Assuming [0] is system, [1:] is conversation
	conversation := history[1 : len(history)-1]
	if len(conversation) == 0 {
		return
	}

	// Helper to find the mid-point of the conversation
	mid := len(conversation) / 2

	// New history structure:
	// 1. System Prompt (with compression note appended)
	// 2. Second half of conversation
	// 3. Last message

	droppedCount := mid
	keptConversation := conversation[mid:]

	newHistory := make([]providers.Message, 0, 1+len(keptConversation)+1)

	// Append compression note to the original system prompt instead of adding a new system message
	// This avoids having two consecutive system messages which some APIs (like Zhipu) reject
	compressionNote := fmt.Sprintf(
		"\n\n[System Note: Emergency compression dropped %d oldest messages due to context limit]",
		droppedCount,
	)
	enhancedSystemPrompt := history[0]
	enhancedSystemPrompt.Content = enhancedSystemPrompt.Content + compressionNote
	newHistory = append(newHistory, enhancedSystemPrompt)

	newHistory = append(newHistory, keptConversation...)
	newHistory = append(newHistory, history[len(history)-1]) // Last message

	// Update session
	agent.Sessions.SetHistory(sessionKey, newHistory)
	agent.Sessions.Save(sessionKey)

	logger.WarnCF("agent", "Forced compression executed", map[string]any{
		"session_key":  sessionKey,
		"dropped_msgs": droppedCount,
		"new_count":    len(newHistory),
	})
}

// GetStartupInfo returns information about loaded tools and skills for logging.
func (al *AgentLoop) GetStartupInfo() map[string]any {
	info := make(map[string]any)

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		return info
	}

	// Tools info
	toolsList := agent.Tools.List()
	info["tools"] = map[string]any{
		"count": len(toolsList),
		"names": toolsList,
	}

	// Skills info
	info["skills"] = agent.ContextBuilder.GetSkillsInfo()

	// Agents info
	info["agents"] = map[string]any{
		"count": len(al.registry.ListAgentIDs()),
		"ids":   al.registry.ListAgentIDs(),
	}

	return info
}

// formatMessagesForLog formats messages for logging
func formatMessagesForLog(messages []providers.Message) string {
	if len(messages) == 0 {
		return "[]"
	}

	var sb strings.Builder
	sb.WriteString("[\n")
	for i, msg := range messages {
		fmt.Fprintf(&sb, "  [%d] Role: %s\n", i, msg.Role)
		if len(msg.ToolCalls) > 0 {
			sb.WriteString("  ToolCalls:\n")
			for _, tc := range msg.ToolCalls {
				fmt.Fprintf(&sb, "    - ID: %s, Type: %s, Name: %s\n", tc.ID, tc.Type, tc.Name)
				if tc.Function != nil {
					fmt.Fprintf(
						&sb,
						"      Arguments: %s\n",
						utils.Truncate(tc.Function.Arguments, 200),
					)
				}
			}
		}
		if msg.Content != "" {
			content := utils.Truncate(msg.Content, 200)
			fmt.Fprintf(&sb, "  Content: %s\n", content)
		}
		if msg.ToolCallID != "" {
			fmt.Fprintf(&sb, "  ToolCallID: %s\n", msg.ToolCallID)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("]")
	return sb.String()
}

// formatToolsForLog formats tool definitions for logging
func formatToolsForLog(toolDefs []providers.ToolDefinition) string {
	if len(toolDefs) == 0 {
		return "[]"
	}

	var sb strings.Builder
	sb.WriteString("[\n")
	for i, tool := range toolDefs {
		fmt.Fprintf(&sb, "  [%d] Type: %s, Name: %s\n", i, tool.Type, tool.Function.Name)
		fmt.Fprintf(&sb, "      Description: %s\n", tool.Function.Description)
		if len(tool.Function.Parameters) > 0 {
			fmt.Fprintf(
				&sb,
				"      Parameters: %s\n",
				utils.Truncate(fmt.Sprintf("%v", tool.Function.Parameters), 200),
			)
		}
	}
	sb.WriteString("]")
	return sb.String()
}

// summarizeSession summarizes the conversation history for a session.
func (al *AgentLoop) summarizeSession(agent *AgentInstance, sessionKey string) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	history := agent.Sessions.GetHistory(sessionKey)
	summary := agent.Sessions.GetSummary(sessionKey)

	// Keep last 4 messages for continuity
	if len(history) <= 4 {
		return
	}

	toSummarize := history[:len(history)-4]

	// Oversized Message Guard
	maxMessageTokens := agent.ContextWindow / 2
	validMessages := make([]providers.Message, 0)
	omitted := false

	for _, m := range toSummarize {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		msgTokens := len(m.Content) / 2
		if msgTokens > maxMessageTokens {
			omitted = true
			continue
		}
		validMessages = append(validMessages, m)
	}

	if len(validMessages) == 0 {
		return
	}

	// Multi-Part Summarization
	var finalSummary string
	if len(validMessages) > 10 {
		mid := len(validMessages) / 2
		part1 := validMessages[:mid]
		part2 := validMessages[mid:]

		s1, _ := al.summarizeBatch(ctx, agent, part1, "")
		s2, _ := al.summarizeBatch(ctx, agent, part2, "")

		mergePrompt := fmt.Sprintf(
			"Merge these two conversation summaries into one cohesive summary:\n\n1: %s\n\n2: %s",
			s1,
			s2,
		)
		resp, err := agent.Provider.Chat(
			ctx,
			[]providers.Message{{Role: "user", Content: mergePrompt}},
			nil,
			agent.Model,
			map[string]any{
				"max_tokens":       1024,
				"temperature":      0.3,
				"prompt_cache_key": agent.ID,
			},
		)
		if err == nil {
			finalSummary = resp.Content
		} else {
			finalSummary = s1 + " " + s2
		}
	} else {
		finalSummary, _ = al.summarizeBatch(ctx, agent, validMessages, summary)
	}

	if omitted && finalSummary != "" {
		finalSummary += "\n[Note: Some oversized messages were omitted from this summary for efficiency.]"
	}

	if finalSummary != "" {
		agent.Sessions.SetSummary(sessionKey, finalSummary)
		agent.Sessions.TruncateHistory(sessionKey, 4)
		agent.Sessions.Save(sessionKey)
	}
}

// summarizeBatch summarizes a batch of messages.
func (al *AgentLoop) summarizeBatch(
	ctx context.Context,
	agent *AgentInstance,
	batch []providers.Message,
	existingSummary string,
) (string, error) {
	var sb strings.Builder
	sb.WriteString(
		"Provide a concise summary of this conversation segment, preserving core context and key points.\n",
	)
	if existingSummary != "" {
		sb.WriteString("Existing context: ")
		sb.WriteString(existingSummary)
		sb.WriteString("\n")
	}
	sb.WriteString("\nCONVERSATION:\n")
	for _, m := range batch {
		fmt.Fprintf(&sb, "%s: %s\n", m.Role, m.Content)
	}
	prompt := sb.String()

	response, err := agent.Provider.Chat(
		ctx,
		[]providers.Message{{Role: "user", Content: prompt}},
		nil,
		agent.Model,
		map[string]any{
			"max_tokens":       1024,
			"temperature":      0.3,
			"prompt_cache_key": agent.ID,
		},
	)
	if err != nil {
		return "", err
	}
	return response.Content, nil
}

// estimateTokens estimates the number of tokens in a message list.
// Uses a safe heuristic of 2.5 characters per token to account for CJK and other
// overheads better than the previous 3 chars/token.
func (al *AgentLoop) estimateTokens(messages []providers.Message) int {
	totalChars := 0
	for _, m := range messages {
		totalChars += utf8.RuneCountInString(m.Content)
	}
	// 2.5 chars per token = totalChars * 2 / 5
	return totalChars * 2 / 5
}

func (al *AgentLoop) handleCommand(
	ctx context.Context,
	msg bus.InboundMessage,
	agent *AgentInstance,
) (string, bool) {
	if !commands.HasCommandPrefix(msg.Content) {
		return "", false
	}

	if al.cmdRegistry == nil {
		return "", false
	}

	rt := al.buildCommandsRuntime(agent)
	executor := commands.NewExecutor(al.cmdRegistry, rt)

	var commandReply string
	result := executor.Execute(ctx, commands.Request{
		Channel:  msg.Channel,
		ChatID:   msg.ChatID,
		SenderID: msg.SenderID,
		Text:     msg.Content,
		Reply: func(text string) error {
			commandReply = text
			return nil
		},
	})

	switch result.Outcome {
	case commands.OutcomeHandled:
		if result.Err != nil {
			return mapCommandError(result), true
		}
		if commandReply != "" {
			return commandReply, true
		}
		return "", true
	default: // OutcomePassthrough — let the message fall through to LLM
		return "", false
	}
}

func (al *AgentLoop) buildCommandsRuntime(agent *AgentInstance) *commands.Runtime {
	rt := &commands.Runtime{
		Config:          al.cfg,
		ListAgentIDs:    al.registry.ListAgentIDs,
		ListDefinitions: al.cmdRegistry.Definitions,
		GetEnabledChannels: func() []string {
			if al.channelManager == nil {
				return nil
			}
			return al.channelManager.GetEnabledChannels()
		},
		SwitchChannel: func(value string) error {
			if al.channelManager == nil {
				return fmt.Errorf("channel manager not initialized")
			}
			if _, exists := al.channelManager.GetChannel(value); !exists && value != "cli" {
				return fmt.Errorf("channel '%s' not found or not enabled", value)
			}
			return nil
		},
	}
	if agent != nil {
		rt.GetModelInfo = func() (string, string) {
			return agent.Model, al.cfg.Agents.Defaults.Provider
		}
		rt.SwitchModel = func(value string) (string, error) {
			oldModel := agent.Model
			agent.Model = value
			return oldModel, nil
		}
	}
	return rt
}

func mapCommandError(result commands.ExecuteResult) string {
	if result.Command == "" {
		return fmt.Sprintf("Failed to execute command: %v", result.Err)
	}
	return fmt.Sprintf("Failed to execute /%s: %v", result.Command, result.Err)
}

// extractPeer extracts the routing peer from the inbound message's structured Peer field.
func extractPeer(msg bus.InboundMessage) *routing.RoutePeer {
	if msg.Peer.Kind == "" {
		return nil
	}
	peerID := msg.Peer.ID
	if peerID == "" {
		if msg.Peer.Kind == "direct" {
			peerID = msg.SenderID
		} else {
			peerID = msg.ChatID
		}
	}
	return &routing.RoutePeer{Kind: msg.Peer.Kind, ID: peerID}
}

func inboundMetadata(msg bus.InboundMessage, key string) string {
	if msg.Metadata == nil {
		return ""
	}
	return msg.Metadata[key]
}

// extractParentPeer extracts the parent peer (reply-to) from inbound message metadata.
func extractParentPeer(msg bus.InboundMessage) *routing.RoutePeer {
	parentKind := inboundMetadata(msg, metadataKeyParentPeerKind)
	parentID := inboundMetadata(msg, metadataKeyParentPeerID)
	if parentKind == "" || parentID == "" {
		return nil
	}
	return &routing.RoutePeer{Kind: parentKind, ID: parentID}
}
