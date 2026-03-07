// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

// Package channels 提供通讯渠道功能
// 本文件包含渠道管理器实现
// 负责：
// - 渠道初始化和启动/停止
// - 消息路由和速率限制
// - HTTP 服务器和 Webhook 处理
// - 占位符消息和输入指示器管理

package channels

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/health"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
)

const (
	defaultChannelQueueSize = 16                 // 默认渠道队列大小（16 条消息）
	defaultRateLimit        = 10                 // 默认速率限制（10 条消息/秒）
	maxRetries              = 3                  // 最大重试次数
	rateLimitDelay          = 1 * time.Second    // 速率限制延迟
	baseBackoff             = 500 * time.Millisecond // 基础退避时间
	maxBackoff              = 8 * time.Second    // 最大退避时间

	janitorInterval = 10 * time.Second // 清理器运行间隔
	typingStopTTL   = 5 * time.Minute  // 输入指示器停止函数 TTL
	placeholderTTL  = 10 * time.Minute // 占位符消息 TTL
)

// typingEntry 输入指示器条目
// 包含停止函数和创建时间戳用于 TTL 驱逐
type typingEntry struct {
	stop      func()      // 停止输入指示器的函数
	createdAt time.Time   // 创建时间
}

// reactionEntry 消息反应条目
// 包含反应撤销函数和创建时间戳用于 TTL 驱逐
type reactionEntry struct {
	undo      func()      // 撤销反应的函数
	createdAt time.Time   // 创建时间
}

// placeholderEntry 占位符条目
// 包含占位符消息 ID 和创建时间戳用于 TTL 驱逐
type placeholderEntry struct {
	id        string      // 占位符消息 ID
	createdAt time.Time   // 创建时间
}

// channelRateConfig 渠道速率限制配置
// 映射渠道名称到每秒速率限制
var channelRateConfig = map[string]float64{
	"telegram": 20, // Telegram 20 条/秒
	"discord":  1,  // Discord 1 条/秒
	"slack":    1,  // Slack 1 条/秒
	"line":     10, // LINE 10 条/秒
}

// channelWorker 渠道工作者
// 负责处理单个渠道的消息发送，带速率限制
type channelWorker struct {
	ch         Channel          // 渠道实例
	queue      chan bus.OutboundMessage        // 消息队列
	mediaQueue chan bus.OutboundMediaMessage   // 媒体消息队列
	done       chan struct{}    // 完成信号
	mediaDone  chan struct{}    // 媒体完成信号
	limiter    *rate.Limiter    // 速率限制器
}

// Manager 渠道管理器
// 管理所有通讯渠道的生命周期和消息路由
type Manager struct {
	channels      map[string]Channel           // 渠道映射表
	workers       map[string]*channelWorker    // 工作者映射表
	bus           *bus.MessageBus              // 消息总线
	config        *config.Config               // 配置
	mediaStore    media.MediaStore             // 媒体存储
	dispatchTask  *asyncTask                   // 调度任务
	mux           *http.ServeMux               // HTTP 多路复用器
	httpServer    *http.Server                 // HTTP 服务器
	mu            sync.RWMutex                 // 读写锁
	placeholders  sync.Map // "channel:chatID" → placeholderEntry (占位符)
	typingStops   sync.Map // "channel:chatID" → typingEntry (输入指示器)
	reactionUndos sync.Map // "channel:chatID" → reactionEntry (消息反应)
}

// asyncTask 异步任务
// 用于管理后台 goroutine 的取消
type asyncTask struct {
	cancel context.CancelFunc  // 取消函数
}

// RecordPlaceholder 记录占位符消息用于后续编辑
// 实现 PlaceholderRecorder 接口
func (m *Manager) RecordPlaceholder(channel, chatID, placeholderID string) {
	key := channel + ":" + chatID
	m.placeholders.Store(key, placeholderEntry{id: placeholderID, createdAt: time.Now()})
}

// RecordTypingStop 记录输入指示器停止函数用于后续调用
// 实现 PlaceholderRecorder 接口
func (m *Manager) RecordTypingStop(channel, chatID string, stop func()) {
	key := channel + ":" + chatID
	m.typingStops.Store(key, typingEntry{stop: stop, createdAt: time.Now()})
}

// RecordReactionUndo 记录反应撤销函数用于后续调用
// 实现 PlaceholderRecorder 接口
func (m *Manager) RecordReactionUndo(channel, chatID string, undo func()) {
	key := channel + ":" + chatID
	m.reactionUndos.Store(key, reactionEntry{undo: undo, createdAt: time.Now()})
}

// preSend 在发送消息前处理输入指示器停止、反应撤销和占位符编辑
// 返回 true 表示消息已编辑到占位符（跳过 Send）
//
// 参数：
// - ctx: 上下文
// - name: 渠道名称
// - msg: 出站消息
// - ch: 渠道实例
//
// 返回：
// - bool: 是否已编辑占位符（跳过发送）
func (m *Manager) preSend(ctx context.Context, name string, msg bus.OutboundMessage, ch Channel) bool {
	key := name + ":" + msg.ChatID

	// 1. 停止输入指示器
	if v, loaded := m.typingStops.LoadAndDelete(key); loaded {
		if entry, ok := v.(typingEntry); ok {
			entry.stop() // 幂等，安全
		}
	}

	// 2. 撤销消息反应
	if v, loaded := m.reactionUndos.LoadAndDelete(key); loaded {
		if entry, ok := v.(reactionEntry); ok {
			entry.undo() // 幂等，安全
		}
	}

	// 3. 尝试编辑占位符
	if v, loaded := m.placeholders.LoadAndDelete(key); loaded {
		if entry, ok := v.(placeholderEntry); ok && entry.id != "" {
			if editor, ok := ch.(MessageEditor); ok {
				if err := editor.EditMessage(ctx, msg.ChatID, entry.id, msg.Content); err == nil {
					return true // 编辑成功，跳过发送
				}
				// 编辑失败 → 回退到正常发送
			}
		}
	}

	return false
}

// NewManager 创建渠道管理器
//
// 参数：
// - cfg: 配置对象
// - messageBus: 消息总线
// - store: 媒体存储
//
// 返回：
// - *Manager: 渠道管理器指针
// - error: 创建错误
func NewManager(cfg *config.Config, messageBus *bus.MessageBus, store media.MediaStore) (*Manager, error) {
	m := &Manager{
		channels:   make(map[string]Channel),
		workers:    make(map[string]*channelWorker),
		bus:        messageBus,
		config:     cfg,
		mediaStore: store,
	}

	if err := m.initChannels(); err != nil {
		return nil, err
	}

	return m, nil
}

// initChannel 初始化单个渠道的辅助函数
// 按名称查找工厂函数并创建渠道
//
// 参数：
// - name: 渠道名称（如 "telegram"）
// - displayName: 渠道显示名称（如 "Telegram"）
func (m *Manager) initChannel(name, displayName string) {
	f, ok := getFactory(name)
	if !ok {
		logger.WarnCF("channels", "Factory not registered", map[string]any{
			"channel": displayName,
		})
		return
	}
	logger.DebugCF("channels", "Attempting to initialize channel", map[string]any{
		"channel": displayName,
	})
	ch, err := f(m.config, m.bus)
	if err != nil {
		logger.ErrorCF("channels", "Failed to initialize channel", map[string]any{
			"channel": displayName,
			"error":   err.Error(),
		})
	} else {
		// 如果渠道支持，注入 MediaStore
		if m.mediaStore != nil {
			if setter, ok := ch.(interface{ SetMediaStore(s media.MediaStore) }); ok {
				setter.SetMediaStore(m.mediaStore)
			}
		}
		// 如果渠道支持，注入 PlaceholderRecorder
		if setter, ok := ch.(interface{ SetPlaceholderRecorder(r PlaceholderRecorder) }); ok {
			setter.SetPlaceholderRecorder(m)
		}
		// 注入 owner 引用，以便 BaseChannel.HandleMessage 自动触发输入/反应
		if setter, ok := ch.(interface{ SetOwner(ch Channel) }); ok {
			setter.SetOwner(ch)
		}
		m.channels[name] = ch
		logger.InfoCF("channels", "Channel enabled successfully", map[string]any{
			"channel": displayName,
		})
	}
}

// initChannels 初始化所有启用的渠道
// 遍历配置中的 Channels，为每个启用的渠道调用 initChannel
//
// 返回：
// - error: 初始化错误
func (m *Manager) initChannels() error {
	logger.InfoC("channels", "Initializing channel manager")

	if m.config.Channels.Telegram.Enabled && m.config.Channels.Telegram.Token != "" {
		m.initChannel("telegram", "Telegram")
	}

	if m.config.Channels.WhatsApp.Enabled {
		waCfg := m.config.Channels.WhatsApp
		if waCfg.UseNative {
			m.initChannel("whatsapp_native", "WhatsApp Native")
		} else if waCfg.BridgeURL != "" {
			m.initChannel("whatsapp", "WhatsApp")
		}
	}

	if m.config.Channels.Feishu.Enabled {
		m.initChannel("feishu", "Feishu")
	}

	if m.config.Channels.Discord.Enabled && m.config.Channels.Discord.Token != "" {
		m.initChannel("discord", "Discord")
	}

	if m.config.Channels.MaixCam.Enabled {
		m.initChannel("maixcam", "MaixCam")
	}

	if m.config.Channels.QQ.Enabled {
		m.initChannel("qq", "QQ")
	}

	if m.config.Channels.DingTalk.Enabled && m.config.Channels.DingTalk.ClientID != "" {
		m.initChannel("dingtalk", "DingTalk")
	}

	if m.config.Channels.Slack.Enabled && m.config.Channels.Slack.BotToken != "" {
		m.initChannel("slack", "Slack")
	}

	if m.config.Channels.LINE.Enabled && m.config.Channels.LINE.ChannelAccessToken != "" {
		m.initChannel("line", "LINE")
	}

	if m.config.Channels.OneBot.Enabled && m.config.Channels.OneBot.WSUrl != "" {
		m.initChannel("onebot", "OneBot")
	}

	if m.config.Channels.WeCom.Enabled && m.config.Channels.WeCom.Token != "" {
		m.initChannel("wecom", "WeCom")
	}

	if m.config.Channels.WeComAIBot.Enabled && m.config.Channels.WeComAIBot.Token != "" {
		m.initChannel("wecom_aibot", "WeCom AI Bot")
	}

	if m.config.Channels.WeComApp.Enabled && m.config.Channels.WeComApp.CorpID != "" {
		m.initChannel("wecom_app", "WeCom App")
	}

	if m.config.Channels.Pico.Enabled && m.config.Channels.Pico.Token != "" {
		m.initChannel("pico", "Pico")
	}

	logger.InfoCF("channels", "Channel initialization completed", map[string]any{
		"enabled_channels": len(m.channels),
	})

	return nil
}

// SetupHTTPServer 创建共享 HTTP 服务器
// 注册健康检查端点，并发现实现 WebhookHandler 和/或 HealthChecker 的渠道来注册它们的处理程序
//
// 参数：
// - addr: HTTP 服务器监听地址
// - healthServer: 健康检查服务器
func (m *Manager) SetupHTTPServer(addr string, healthServer *health.Server) {
	m.mux = http.NewServeMux()

	// 注册健康检查端点
	if healthServer != nil {
		healthServer.RegisterOnMux(m.mux)
	}

	// 发现并注册 Webhook 处理程序和健康检查端点
	for name, ch := range m.channels {
		if wh, ok := ch.(WebhookHandler); ok {
			m.mux.Handle(wh.WebhookPath(), wh)
			logger.InfoCF("channels", "Webhook handler registered", map[string]any{
				"channel": name,
				"path":    wh.WebhookPath(),
			})
		}
		if hc, ok := ch.(HealthChecker); ok {
			m.mux.HandleFunc(hc.HealthPath(), hc.HealthHandler)
			logger.InfoCF("channels", "Health endpoint registered", map[string]any{
				"channel": name,
				"path":    hc.HealthPath(),
			})
		}
	}

	m.httpServer = &http.Server{
		Addr:         addr,
		Handler:      m.mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
}

// StartAll 启动所有渠道
// 为每个渠道创建工作者并启动消息调度
//
// 参数：
// - ctx: 上下文用于取消控制
//
// 返回：
// - error: 启动错误
func (m *Manager) StartAll(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.channels) == 0 {
		logger.WarnC("channels", "No channels enabled")
		return errors.New("no channels enabled")
	}

	logger.InfoC("channels", "Starting all channels")

	dispatchCtx, cancel := context.WithCancel(ctx)
	m.dispatchTask = &asyncTask{cancel: cancel}

	for name, channel := range m.channels {
		logger.InfoCF("channels", "Starting channel", map[string]any{
			"channel": name,
		})
		if err := channel.Start(ctx); err != nil {
			logger.ErrorCF("channels", "Failed to start channel", map[string]any{
				"channel": name,
				"error":   err.Error(),
			})
			continue
		}
		// 仅在渠道成功启动后懒创建工作者
		w := newChannelWorker(name, channel)
		m.workers[name] = w
		go m.runWorker(dispatchCtx, name, w)
		go m.runMediaWorker(dispatchCtx, name, w)
	}

	// 启动从消息总线读取并路由到工作者的调度器
	go m.dispatchOutbound(dispatchCtx)
	go m.dispatchOutboundMedia(dispatchCtx)

	// 启动 TTL 清理器以清理过时的输入/占位符条目
	go m.runTTLJanitor(dispatchCtx)

	// 如果配置了，启动共享 HTTP 服务器
	if m.httpServer != nil {
		go func() {
			logger.InfoCF("channels", "Shared HTTP server listening", map[string]any{
				"addr": m.httpServer.Addr,
			})
			if err := m.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.ErrorCF("channels", "Shared HTTP server error", map[string]any{
					"error": err.Error(),
				})
			}
		}()
	}

	logger.InfoC("channels", "All channels started")
	return nil
}

// StopAll 停止所有渠道
// 关闭 HTTP 服务器、取消调度器、等待工作者排空
//
// 参数：
// - ctx: 上下文用于取消控制
//
// 返回：
// - error: 停止错误
func (m *Manager) StopAll(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	logger.InfoC("channels", "Stopping all channels")

	// 首先关闭共享 HTTP 服务器
	if m.httpServer != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := m.httpServer.Shutdown(shutdownCtx); err != nil {
			logger.ErrorCF("channels", "Shared HTTP server shutdown error", map[string]any{
				"error": err.Error(),
			})
		}
		m.httpServer = nil
	}

	// 取消调度器
	if m.dispatchTask != nil {
		m.dispatchTask.cancel()
		m.dispatchTask = nil
	}

	// 关闭所有工作者队列并等待排空
	for _, w := range m.workers {
		if w != nil {
			close(w.queue)
		}
	}
	for _, w := range m.workers {
		if w != nil {
			<-w.done
		}
	}
	// 关闭所有媒体工作者队列并等待排空
	for _, w := range m.workers {
		if w != nil {
			close(w.mediaQueue)
		}
	}
	for _, w := range m.workers {
		if w != nil {
			<-w.mediaDone
		}
	}

	// 停止所有渠道
	for name, channel := range m.channels {
		logger.InfoCF("channels", "Stopping channel", map[string]any{
			"channel": name,
		})
		if err := channel.Stop(ctx); err != nil {
			logger.ErrorCF("channels", "Error stopping channel", map[string]any{
				"channel": name,
				"error":   err.Error(),
			})
		}
	}

	logger.InfoC("channels", "All channels stopped")
	return nil
}

// newChannelWorker 创建带速率限制器的渠道工作者
//
// 参数：
// - name: 渠道名称
// - ch: 渠道实例
//
// 返回：
// - *channelWorker: 渠道工作者指针
func newChannelWorker(name string, ch Channel) *channelWorker {
	rateVal := float64(defaultRateLimit)
	if r, ok := channelRateConfig[name]; ok {
		rateVal = r
	}
	burst := int(math.Max(1, math.Ceil(rateVal/2)))

	return &channelWorker{
		ch:         ch,
		queue:      make(chan bus.OutboundMessage, defaultChannelQueueSize),
		mediaQueue: make(chan bus.OutboundMediaMessage, defaultChannelQueueSize),
		done:       make(chan struct{}),
		mediaDone:  make(chan struct{}),
		limiter:    rate.NewLimiter(rate.Limit(rateVal), burst),
	}
}

// runWorker 处理单个渠道的出站消息
// 分割超过渠道最大消息长度的消息
//
// 参数：
// - ctx: 上下文
// - name: 渠道名称
// - w: 渠道工作者
func (m *Manager) runWorker(ctx context.Context, name string, w *channelWorker) {
	defer close(w.done)
	for {
		select {
		case msg, ok := <-w.queue:
			if !ok {
				return
			}
			maxLen := 0
			if mlp, ok := w.ch.(MessageLengthProvider); ok {
				maxLen = mlp.MaxMessageLength()
			}
			if maxLen > 0 && len([]rune(msg.Content)) > maxLen {
				chunks := SplitMessage(msg.Content, maxLen)
				for _, chunk := range chunks {
					chunkMsg := msg
					chunkMsg.Content = chunk
					m.sendWithRetry(ctx, name, w, chunkMsg)
				}
			} else {
				m.sendWithRetry(ctx, name, w, msg)
			}
		case <-ctx.Done():
			return
		}
	}
}

// sendWithRetry 通过渠道发送消息，带速率限制和重试逻辑
// 分类错误以确定重试策略：
//   - ErrNotRunning / ErrSendFailed: 永久性错误，不重试
//   - ErrRateLimit: 固定延迟重试
//   - ErrTemporary / 未知错误：指数退避重试
//
// 参数：
// - ctx: 上下文
// - name: 渠道名称
// - w: 渠道工作者
// - msg: 出站消息
func (m *Manager) sendWithRetry(ctx context.Context, name string, w *channelWorker, msg bus.OutboundMessage) {
	// 速率限制：等待令牌
	if err := w.limiter.Wait(ctx); err != nil {
		// 上下文取消，正在关闭
		return
	}

	// 预发送：停止输入指示器并尝试编辑占位符
	if m.preSend(ctx, name, msg, w.ch) {
		return // 占位符已成功编辑，跳过发送
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		lastErr = w.ch.Send(ctx, msg)
		if lastErr == nil {
			return
		}

		// 永久性错误 — 不重试
		if errors.Is(lastErr, ErrNotRunning) || errors.Is(lastErr, ErrSendFailed) {
			break
		}

		// 重试次数已用尽 — 不睡眠
		if attempt == maxRetries {
			break
		}

		// 速率限制错误 — 固定延迟
		if errors.Is(lastErr, ErrRateLimit) {
			select {
			case <-time.After(rateLimitDelay):
				continue
			case <-ctx.Done():
				return
			}
		}

		// ErrTemporary 或未知错误 — 指数退避
		backoff := min(time.Duration(float64(baseBackoff)*math.Pow(2, float64(attempt))), maxBackoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
	}

	// 重试耗尽或永久性失败
	logger.ErrorCF("channels", "Send failed", map[string]any{
		"channel": name,
		"chat_id": msg.ChatID,
		"error":   lastErr.Error(),
		"retries": maxRetries,
	})
}

// dispatchLoop 通用调度循环
// 从消息总线订阅消息并路由到对应渠道的工作者
//
// 参数：
// - ctx: 上下文
// - m: 渠道管理器
// - subscribe: 订阅函数
// - getChannel: 获取渠道名称的函数
// - enqueue: 入队函数
// - startMsg: 启动日志消息
// - stopMsg: 停止日志消息
// - unknownMsg: 未知渠道日志消息
// - noWorkerMsg: 无工作者日志消息
func dispatchLoop[M any](
	ctx context.Context,
	m *Manager,
	subscribe func(context.Context) (M, bool),
	getChannel func(M) string,
	enqueue func(context.Context, *channelWorker, M) bool,
	startMsg, stopMsg, unknownMsg, noWorkerMsg string,
) {
	logger.InfoC("channels", startMsg)

	for {
		msg, ok := subscribe(ctx)
		if !ok {
			logger.InfoC("channels", stopMsg)
			return
		}

		channel := getChannel(msg)

		// 静默跳过内部渠道
		if constants.IsInternalChannel(channel) {
			continue
		}

		m.mu.RLock()
		_, exists := m.channels[channel]
		w, wExists := m.workers[channel]
		m.mu.RUnlock()

		if !exists {
			logger.WarnCF("channels", unknownMsg, map[string]any{"channel": channel})
			continue
		}

		if wExists && w != nil {
			if !enqueue(ctx, w, msg) {
				return
			}
		} else if exists {
			logger.WarnCF("channels", noWorkerMsg, map[string]any{"channel": channel})
		}
	}
}

// dispatchOutbound 调度出站消息到渠道
func (m *Manager) dispatchOutbound(ctx context.Context) {
	dispatchLoop(
		ctx, m,
		m.bus.SubscribeOutbound,
		func(msg bus.OutboundMessage) string { return msg.Channel },
		func(ctx context.Context, w *channelWorker, msg bus.OutboundMessage) bool {
			select {
			case w.queue <- msg:
				return true
			case <-ctx.Done():
				return false
			}
		},
		"Outbound dispatcher started",
		"Outbound dispatcher stopped",
		"Unknown channel for outbound message",
		"Channel has no active worker, skipping message",
	)
}

// dispatchOutboundMedia 调度出站媒体消息到渠道
func (m *Manager) dispatchOutboundMedia(ctx context.Context) {
	dispatchLoop(
		ctx, m,
		m.bus.SubscribeOutboundMedia,
		func(msg bus.OutboundMediaMessage) string { return msg.Channel },
		func(ctx context.Context, w *channelWorker, msg bus.OutboundMediaMessage) bool {
			select {
			case w.mediaQueue <- msg:
				return true
			case <-ctx.Done():
				return false
			}
		},
		"Outbound media dispatcher started",
		"Outbound media dispatcher stopped",
		"Unknown channel for outbound media message",
		"Channel has no active worker, skipping media message",
	)
}

// runMediaWorker 处理单个渠道的出站媒体消息
//
// 参数：
// - ctx: 上下文
// - name: 渠道名称
// - w: 渠道工作者
func (m *Manager) runMediaWorker(ctx context.Context, name string, w *channelWorker) {
	defer close(w.mediaDone)
	for {
		select {
		case msg, ok := <-w.mediaQueue:
			if !ok {
				return
			}
			m.sendMediaWithRetry(ctx, name, w, msg)
		case <-ctx.Done():
			return
		}
	}
}

// sendMediaWithRetry 通过渠道发送媒体消息，带速率限制和重试逻辑
// 如果渠道未实现 MediaSender 接口，则静默跳过
//
// 参数：
// - ctx: 上下文
// - name: 渠道名称
// - w: 渠道工作者
// - msg: 出站媒体消息
func (m *Manager) sendMediaWithRetry(ctx context.Context, name string, w *channelWorker, msg bus.OutboundMediaMessage) {
	ms, ok := w.ch.(MediaSender)
	if !ok {
		logger.DebugCF("channels", "Channel does not support MediaSender, skipping media", map[string]any{
			"channel": name,
		})
		return
	}

	// 速率限制：等待令牌
	if err := w.limiter.Wait(ctx); err != nil {
		return
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		lastErr = ms.SendMedia(ctx, msg)
		if lastErr == nil {
			return
		}

		// 永久性错误 — 不重试
		if errors.Is(lastErr, ErrNotRunning) || errors.Is(lastErr, ErrSendFailed) {
			break
		}

		// 重试次数已用尽 — 不睡眠
		if attempt == maxRetries {
			break
		}

		// 速率限制错误 — 固定延迟
		if errors.Is(lastErr, ErrRateLimit) {
			select {
			case <-time.After(rateLimitDelay):
				continue
			case <-ctx.Done():
				return
			}
		}

		// ErrTemporary 或未知错误 — 指数退避
		backoff := min(time.Duration(float64(baseBackoff)*math.Pow(2, float64(attempt))), maxBackoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
	}

	// 重试耗尽或永久性失败
	logger.ErrorCF("channels", "SendMedia failed", map[string]any{
		"channel": name,
		"chat_id": msg.ChatID,
		"error":   lastErr.Error(),
		"retries": maxRetries,
	})
}

// runTTLJanitor 定期清理过时的输入指示器和占位符条目
// 防止内存积累（当出站路径未能触发 preSend 时，如 LLM 错误）
//
// 参数：
// - ctx: 上下文
func (m *Manager) runTTLJanitor(ctx context.Context) {
	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			// 清理过时的输入指示器停止函数
			m.typingStops.Range(func(key, value any) bool {
				if entry, ok := value.(typingEntry); ok {
					if now.Sub(entry.createdAt) > typingStopTTL {
						if _, loaded := m.typingStops.LoadAndDelete(key); loaded {
							entry.stop() // 幂等，安全
						}
					}
				}
				return true
			})
			// 清理过时的反应撤销函数
			m.reactionUndos.Range(func(key, value any) bool {
				if entry, ok := value.(reactionEntry); ok {
					if now.Sub(entry.createdAt) > typingStopTTL {
						if _, loaded := m.reactionUndos.LoadAndDelete(key); loaded {
							entry.undo() // 幂等，安全
						}
					}
				}
				return true
			})
			// 清理过时的占位符
			m.placeholders.Range(func(key, value any) bool {
				if entry, ok := value.(placeholderEntry); ok {
					if now.Sub(entry.createdAt) > placeholderTTL {
						m.placeholders.Delete(key)
					}
				}
				return true
			})
		}
	}
}

// GetChannel 根据名称获取渠道
//
// 参数：
// - name: 渠道名称
//
// 返回：
// - Channel: 渠道实例
// - bool: 是否找到
func (m *Manager) GetChannel(name string) (Channel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	channel, ok := m.channels[name]
	return channel, ok
}

// GetStatus 获取所有渠道的状态
//
// 返回：
// - map[string]any: 渠道状态映射
func (m *Manager) GetStatus() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()

	status := make(map[string]any)
	for name, channel := range m.channels {
		status[name] = map[string]any{
			"enabled": true,
			"running": channel.IsRunning(),
		}
	}
	return status
}

// GetEnabledChannels 获取所有启用的渠道名称列表
//
// 返回：
// - []string: 渠道名称列表
func (m *Manager) GetEnabledChannels() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.channels))
	for name := range m.channels {
		names = append(names, name)
	}
	return names
}

// RegisterChannel 注册渠道
//
// 参数：
// - name: 渠道名称
// - channel: 渠道实例
func (m *Manager) RegisterChannel(name string, channel Channel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channels[name] = channel
}

// UnregisterChannel 注销渠道
// 关闭工作者并等待排空
//
// 参数：
// - name: 渠道名称
func (m *Manager) UnregisterChannel(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w, ok := m.workers[name]; ok && w != nil {
		close(w.queue)
		<-w.done
		close(w.mediaQueue)
		<-w.mediaDone
	}
	delete(m.workers, name)
	delete(m.channels, name)
}

// SendToChannel 直接发送消息到渠道
//
// 参数：
// - ctx: 上下文
// - channelName: 渠道名称
// - chatID: 聊天 ID
// - content: 消息内容
//
// 返回：
// - error: 发送错误
func (m *Manager) SendToChannel(ctx context.Context, channelName, chatID, content string) error {
	m.mu.RLock()
	_, exists := m.channels[channelName]
	w, wExists := m.workers[channelName]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("channel %s not found", channelName)
	}

	msg := bus.OutboundMessage{
		Channel: channelName,
		ChatID:  chatID,
		Content: content,
	}

	if wExists && w != nil {
		select {
		case w.queue <- msg:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// 回退：直接发送（不应该发生）
	channel, _ := m.channels[channelName]
	return channel.Send(ctx, msg)
}
