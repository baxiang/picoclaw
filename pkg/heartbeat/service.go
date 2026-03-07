// Package heartbeat 提供心跳服务功能
// 定期执行预设任务，如检查未读消息、日历事件、设备状态等
// 任务定义在 workspace/HEARTBEAT.md 文件中
package heartbeat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/state"
	"github.com/sipeed/picoclaw/pkg/tools"
)

const (
	minIntervalMinutes     = 5  // 最小间隔时间（分钟）
	defaultIntervalMinutes = 30 // 默认间隔时间（分钟）
)

// HeartbeatHandler 心跳处理器函数类型
// 用于处理心跳请求，返回 ToolResult 可能表示异步操作
// channel 和 chatID 从最后活跃的用户渠道派生
//
// 参数：
// - prompt: 心跳提示内容
// - channel: 渠道名称
// - chatID: 聊天标识符
//
// 返回：
// - *tools.ToolResult: 工具执行结果
type HeartbeatHandler func(prompt, channel, chatID string) *tools.ToolResult

// HeartbeatService 心跳服务
// 管理定期心跳检查的生命周期
//
// 字段说明：
// - workspace: 工作空间路径
// - bus: 消息总线，用于发送心跳结果
// - state: 状态管理器，存储最后渠道等信息
// - handler: 心跳处理器
// - interval: 心跳间隔时间
// - enabled: 是否启用
// - mu: 保护并发访问的读写锁
// - stopChan: 停止信号通道
type HeartbeatService struct {
	workspace string
	bus       *bus.MessageBus
	state     *state.Manager
	handler   HeartbeatHandler
	interval  time.Duration
	enabled   bool
	mu        sync.RWMutex
	stopChan  chan struct{}
}

// NewHeartbeatService 创建新的心跳服务
//
// 参数：
// - workspace: 工作空间路径
// - intervalMinutes: 心跳间隔（分钟），0 表示使用默认值
// - enabled: 是否启用
//
// 返回：
// - *HeartbeatService: 心跳服务实例
func NewHeartbeatService(workspace string, intervalMinutes int, enabled bool) *HeartbeatService {
	// 应用最小间隔限制
	if intervalMinutes < minIntervalMinutes && intervalMinutes != 0 {
		intervalMinutes = minIntervalMinutes
	}

	if intervalMinutes == 0 {
		intervalMinutes = defaultIntervalMinutes
	}

	return &HeartbeatService{
		workspace: workspace,
		interval:  time.Duration(intervalMinutes) * time.Minute,
		enabled:   enabled,
		state:     state.NewManager(workspace),
	}
}

// SetBus 设置消息总线用于传递心跳结果
//
// 参数：
// - msgBus: 消息总线实例
func (hs *HeartbeatService) SetBus(msgBus *bus.MessageBus) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.bus = msgBus
}

// SetHandler 设置心跳处理器
//
// 参数：
// - handler: 心跳处理器函数
func (hs *HeartbeatService) SetHandler(handler HeartbeatHandler) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.handler = handler
}

// Start 启动心跳服务
// 启动定时器 goroutine，定期执行心跳检查
//
// 返回：
// - error: 启动错误
func (hs *HeartbeatService) Start() error {
	hs.mu.Lock()
	defer hs.mu.Unlock()

	if hs.stopChan != nil {
		logger.InfoC("heartbeat", "Heartbeat service already running")
		return nil
	}

	if !hs.enabled {
		logger.InfoC("heartbeat", "Heartbeat service disabled")
		return nil
	}

	hs.stopChan = make(chan struct{})
	go hs.runLoop(hs.stopChan)

	logger.InfoCF("heartbeat", "Heartbeat service started", map[string]any{
		"interval_minutes": hs.interval.Minutes(),
	})

	return nil
}

// Stop 优雅停止心跳服务
// 关闭停止通道，等待 goroutine 退出
func (hs *HeartbeatService) Stop() {
	hs.mu.Lock()
	defer hs.mu.Unlock()

	if hs.stopChan == nil {
		return
	}

	logger.InfoC("heartbeat", "Stopping heartbeat service")
	close(hs.stopChan)
	hs.stopChan = nil
}

// IsRunning 检查服务是否正在运行
//
// 返回：
// - bool: true 表示正在运行
func (hs *HeartbeatService) IsRunning() bool {
	hs.mu.RLock()
	defer hs.mu.RUnlock()
	return hs.stopChan != nil
}

// runLoop 运行心跳定时器循环
// 使用 ticker 定期触发心跳检查
//
// 参数：
// - stopChan: 停止信号通道
func (hs *HeartbeatService) runLoop(stopChan chan struct{}) {
	ticker := time.NewTicker(hs.interval)
	defer ticker.Stop()

	// 首次心跳在初始延迟后执行
	time.AfterFunc(time.Second, func() {
		hs.executeHeartbeat()
	})

	for {
		select {
		case <-stopChan:
			return
		case <-ticker.C:
			hs.executeHeartbeat()
		}
	}
}

// executeHeartbeat 执行单次心跳检查
// 构建提示、调用处理器、发送结果
func (hs *HeartbeatService) executeHeartbeat() {
	hs.mu.RLock()
	enabled := hs.enabled
	handler := hs.handler
	if !hs.enabled || hs.stopChan == nil {
		hs.mu.RUnlock()
		return
	}
	hs.mu.RUnlock()

	if !enabled {
		return
	}

	logger.DebugC("heartbeat", "Executing heartbeat")

	// 构建心跳提示
	prompt := hs.buildPrompt()
	if prompt == "" {
		logger.InfoC("heartbeat", "No heartbeat prompt (HEARTBEAT.md empty or missing)")
		return
	}

	if handler == nil {
		hs.logErrorf("Heartbeat handler not configured")
		return
	}

	// 获取最后渠道信息用于上下文
	lastChannel := hs.state.GetLastChannel()
	channel, chatID := hs.parseLastChannel(lastChannel)

	// 调试日志：渠道解析
	hs.logInfof("Resolved channel: %s, chatID: %s (from lastChannel: %s)", channel, chatID, lastChannel)

	// 调用处理器
	result := handler(prompt, channel, chatID)

	if result == nil {
		hs.logInfof("Heartbeat handler returned nil result")
		return
	}

	// 处理不同类型的结果
	if result.IsError {
		hs.logErrorf("Heartbeat error: %s", result.ForLLM)
		return
	}

	if result.Async {
		// 异步任务（如 spawn 子代理）
		hs.logInfof("Async task started: %s", result.ForLLM)
		logger.InfoCF("heartbeat", "Async heartbeat task started",
			map[string]any{
				"message": result.ForLLM,
			})
		return
	}

	// 检查是否静默
	if result.Silent {
		hs.logInfof("Heartbeat OK - silent")
		return
	}

	// 发送结果给用户
	if result.ForUser != "" {
		hs.sendResponse(result.ForUser)
	} else if result.ForLLM != "" {
		hs.sendResponse(result.ForLLM)
	}

	hs.logInfof("Heartbeat completed: %s", result.ForLLM)
}

// buildPrompt 从 HEARTBEAT.md 构建心跳提示
// 如果文件不存在，创建默认模板
//
// 返回：
// - string: 心跳提示内容（空表示没有任务）
func (hs *HeartbeatService) buildPrompt() string {
	heartbeatPath := filepath.Join(hs.workspace, "HEARTBEAT.md")

	data, err := os.ReadFile(heartbeatPath)
	if err != nil {
		if os.IsNotExist(err) {
			hs.createDefaultHeartbeatTemplate()
			return ""
		}
		hs.logErrorf("Error reading HEARTBEAT.md: %v", err)
		return ""
	}

	content := string(data)
	if len(content) == 0 {
		return ""
	}

	now := time.Now().Format("2006-01-02 15:04:05")
	return fmt.Sprintf(`# Heartbeat Check

Current time: %s

You are a proactive AI assistant. This is a scheduled heartbeat check.
Review the following tasks and execute any necessary actions using available skills.
If there is nothing that requires attention, respond ONLY with: HEARTBEAT_OK

%s
`, now, content)
}

// createDefaultHeartbeatTemplate 创建默认 HEARTBEAT.md 模板
// 包含示例任务和使用说明
func (hs *HeartbeatService) createDefaultHeartbeatTemplate() {
	heartbeatPath := filepath.Join(hs.workspace, "HEARTBEAT.md")

	defaultContent := `# Heartbeat Check List

This file contains tasks for the heartbeat service to check periodically.

## Examples

- Check for unread messages
- Review upcoming calendar events
- Check device status (e.g., MaixCam)

## Instructions

- Execute ALL tasks listed below. Do NOT skip any task.
- For simple tasks (e.g., report current time), respond directly.
- For complex tasks that may take time, use the spawn tool to create a subagent.
- The spawn tool is async - subagent results will be sent to the user automatically.
- After spawning a subagent, CONTINUE to process remaining tasks.
- Only respond with HEARTBEAT_OK when ALL tasks are done AND nothing needs attention.

---

Add your heartbeat tasks below this line:
`

	if err := fileutil.WriteFileAtomic(heartbeatPath, []byte(defaultContent), 0o644); err != nil {
		hs.logErrorf("Failed to create default HEARTBEAT.md: %v", err)
	} else {
		hs.logInfof("Created default HEARTBEAT.md template")
	}
}

// sendResponse 发送心跳结果到最后渠道
//
// 参数：
// - response: 响应内容
func (hs *HeartbeatService) sendResponse(response string) {
	hs.mu.RLock()
	msgBus := hs.bus
	hs.mu.RUnlock()

	if msgBus == nil {
		hs.logInfof("No message bus configured, heartbeat result not sent")
		return
	}

	// 从状态获取最后渠道
	lastChannel := hs.state.GetLastChannel()
	if lastChannel == "" {
		hs.logInfof("No last channel recorded, heartbeat result not sent")
		return
	}

	platform, userID := hs.parseLastChannel(lastChannel)

	// 跳过无法接收消息的内部渠道
	if platform == "" || userID == "" {
		return
	}

	pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pubCancel()
	msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Channel: platform,
		ChatID:  userID,
		Content: response,
	})

	hs.logInfof("Heartbeat result sent to %s", platform)
}

// parseLastChannel 解析最后渠道字符串为 platform 和 userID
// 返回空字符串表示无效或内部渠道
//
// 参数：
// - lastChannel: 最后渠道字符串（格式："platform:user_id"）
//
// 返回：
// - platform: 渠道平台名称
// - userID: 用户标识符
func (hs *HeartbeatService) parseLastChannel(lastChannel string) (platform, userID string) {
	if lastChannel == "" {
		return "", ""
	}

	// 解析渠道格式："platform:user_id"（如 "telegram:123456"）
	parts := strings.SplitN(lastChannel, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		hs.logErrorf("Invalid last channel format: %s", lastChannel)
		return "", ""
	}

	platform, userID = parts[0], parts[1]

	// 跳过内部渠道
	if constants.IsInternalChannel(platform) {
		hs.logInfof("Skipping internal channel: %s", platform)
		return "", ""
	}

	return platform, userID
}

// logInfof 记录 INFO 级别日志到心跳日志
func (hs *HeartbeatService) logInfof(format string, args ...any) {
	hs.logf("INFO", format, args...)
}

// logErrorf 记录 ERROR 级别日志到心跳日志
func (hs *HeartbeatService) logErrorf(format string, args ...any) {
	hs.logf("ERROR", format, args...)
}

// logf 写入消息到心跳日志文件
// 日志文件位于 workspace/heartbeat.log
func (hs *HeartbeatService) logf(level, format string, args ...any) {
	logFile := filepath.Join(hs.workspace, "heartbeat.log")
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	fmt.Fprintf(f, "[%s] [%s] %s\n", timestamp, level, fmt.Sprintf(format, args...))
}
