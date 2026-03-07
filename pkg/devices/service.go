// Package devices 提供设备热插拔检测功能
// 支持 USB 设备检测（Linux）
// 未来可扩展支持蓝牙、PCI 等设备
package devices

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/devices/events"
	"github.com/sipeed/picoclaw/pkg/devices/sources"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/state"
)

// Service 设备事件服务
// 监控设备热插拔事件并发布到消息总线
//
// 字段说明：
// - bus: 消息总线
// - state: 状态管理器
// - sources: 事件源列表（USB、蓝牙等）
// - enabled: 是否启用
// - ctx: 上下文
// - cancel: 取消函数
// - mu: 保护并发访问的读写锁
type Service struct {
	bus     *bus.MessageBus
	state   *state.Manager
	sources []events.EventSource
	enabled bool
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.RWMutex
}

// Config 设备监控配置
type Config struct {
	Enabled    bool // 是否启用
	MonitorUSB bool // 监控 USB 热插拔（仅 Linux）
	// 未来扩展：MonitorBluetooth（蓝牙）, MonitorPCI（PCI）等
}

// NewService 创建新的设备事件服务
//
// 参数：
// - cfg: 配置
// - stateMgr: 状态管理器
//
// 返回：
// - *Service: 设备事件服务
func NewService(cfg Config, stateMgr *state.Manager) *Service {
	s := &Service{
		state:   stateMgr,
		enabled: cfg.Enabled,
		sources: make([]EventSource, 0),
	}

	if cfg.Enabled && cfg.MonitorUSB {
		s.sources = append(s.sources, sources.NewUSBMonitor())
	}

	return s
}

// SetBus 设置消息总线
//
// 参数：
// - msgBus: 消息总线
func (s *Service) SetBus(msgBus *bus.MessageBus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bus = msgBus
}

// Start 启动设备事件服务
// 启动所有配置的事件源并开始监控
//
// 参数：
// - ctx: 上下文
//
// 返回：
// - error: 启动错误
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.enabled || len(s.sources) == 0 {
		logger.InfoC("devices", "Device event service disabled or no sources")
		return nil
	}

	s.ctx, s.cancel = context.WithCancel(ctx)

	for _, src := range s.sources {
		eventCh, err := src.Start(s.ctx)
		if err != nil {
			logger.ErrorCF("devices", "Failed to start source", map[string]any{
				"kind":  src.Kind(),
				"error": err.Error(),
			})
			continue
		}
		go s.handleEvents(src.Kind(), eventCh)
		logger.InfoCF("devices", "Device source started", map[string]any{
			"kind": src.Kind(),
		})
	}

	logger.InfoC("devices", "Device event service started")
	return nil
}

// Stop 停止设备事件服务
// 停止所有事件源并清理资源
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}

	for _, src := range s.sources {
		src.Stop()
	}

	logger.InfoC("devices", "Device event service stopped")
}

// handleEvents 处理设备事件
// 将设备事件转换为入站消息并发布到消息总线
//
// 参数：
// - kind: 事件类型（USB、蓝牙等）
// - eventCh: 事件通道
func (s *Service) handleEvents(kind events.Kind, eventCh <-chan *events.DeviceEvent) {
	for ev := range eventCh {
		if ev == nil {
			continue
		}
		s.sendNotification(ev)
	}
}

func (s *Service) sendNotification(ev *events.DeviceEvent) {
	s.mu.RLock()
	msgBus := s.bus
	s.mu.RUnlock()

	if msgBus == nil {
		return
	}

	lastChannel := s.state.GetLastChannel()
	if lastChannel == "" {
		logger.DebugCF("devices", "No last channel, skipping notification", map[string]any{
			"event": ev.FormatMessage(),
		})
		return
	}

	platform, userID := parseLastChannel(lastChannel)
	if platform == "" || userID == "" || constants.IsInternalChannel(platform) {
		return
	}

	msg := ev.FormatMessage()
	pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pubCancel()
	msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Channel: platform,
		ChatID:  userID,
		Content: msg,
	})

	logger.InfoCF("devices", "Device notification sent", map[string]any{
		"kind":   ev.Kind,
		"action": ev.Action,
		"to":     platform,
	})
}

func parseLastChannel(lastChannel string) (platform, userID string) {
	if lastChannel == "" {
		return "", ""
	}
	parts := strings.SplitN(lastChannel, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}
