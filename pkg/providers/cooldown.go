// Package providers 提供 LLM 提供商的接口定义和实现
// 本文件包含冷却追踪器（CooldownTracker）的实现
// 用于管理降级链中每个提供商的冷却状态

package providers

import (
	"math"
	"sync"
	"time"
)

const (
	// defaultFailureWindow 失败计数重置的时间窗口（24 小时）
	// 如果提供商在 24 小时内没有新的失败，错误计数会被重置
	defaultFailureWindow = 24 * time.Hour
)

// CooldownTracker 冷却追踪器
// 管理降级链中每个提供商的冷却状态
// 特性：
// - 线程安全：通过 sync.RWMutex 保护并发访问
// - 仅内存存储：重启后状态会重置
// - 支持两种冷却：标准冷却（速率限制等）和计费冷却（余额不足等）
type CooldownTracker struct {
	mu            sync.RWMutex        // 读写锁，保护并发访问
	entries       map[string]*cooldownEntry // 每个提供商的冷却条目
	failureWindow time.Duration       // 失败计数重置的时间窗口
	nowFunc       func() time.Time    // 当前时间函数（用于测试注入）
}
type CooldownTracker struct {
	mu            sync.RWMutex
	entries       map[string]*cooldownEntry
	failureWindow time.Duration
	nowFunc       func() time.Time // for testing
}

// cooldownEntry 冷却条目
// 存储单个提供商的冷却状态信息
type cooldownEntry struct {
	ErrorCount     int            // 累计错误次数
	FailureCounts  map[FailoverReason]int // 每种失败原因的计数
	CooldownEnd    time.Time      // 标准冷却截止时间（速率限制、超时等）
	DisabledUntil  time.Time      // 计费问题禁用截止时间（更长冷却）
	DisabledReason FailoverReason // 禁用原因（通常为 FailoverBilling）
	LastFailure    time.Time      // 最后一次失败时间
}

// NewCooldownTracker 创建冷却追踪器
// 使用默认的 24 小时失败窗口
//
// 返回：
// - 初始化好的 CooldownTracker 指针
func NewCooldownTracker() *CooldownTracker {
	return &CooldownTracker{
		entries:       make(map[string]*cooldownEntry),
		failureWindow: defaultFailureWindow,
		nowFunc:       time.Now,
	}
}

// MarkFailure 记录提供商失败并设置相应的冷却时间
// 如果上次失败超过 failureWindow（24 小时），重置所有计数器
//
// 参数：
// - provider: 提供商名称（如 "openai", "anthropic"）
// - reason: 失败原因分类（FailoverReason）
func (ct *CooldownTracker) MarkFailure(provider string, reason FailoverReason) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	now := ct.nowFunc()
	entry := ct.getOrCreate(provider)

	// 24 小时失败窗口重置：如果上次失败超过 failureWindow，重置计数器
	if !entry.LastFailure.IsZero() && now.Sub(entry.LastFailure) > ct.failureWindow {
		entry.ErrorCount = 0
		entry.FailureCounts = make(map[FailoverReason]int)
	}

	entry.ErrorCount++
	entry.FailureCounts[reason]++
	entry.LastFailure = now

	if reason == FailoverBilling {
		// 计费问题：使用更长的禁用时间
		billingCount := entry.FailureCounts[FailoverBilling]
		entry.DisabledUntil = now.Add(calculateBillingCooldown(billingCount))
		entry.DisabledReason = FailoverBilling
	} else {
		// 标准冷却：指数退避
		entry.CooldownEnd = now.Add(calculateStandardCooldown(entry.ErrorCount))
	}
}

// MarkSuccess 记录提供商成功并重置所有计数器和冷却
// 当请求成功时调用此方法，清除该提供商的所有冷却状态
//
// 参数：
// - provider: 提供商名称
func (ct *CooldownTracker) MarkSuccess(provider string) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	entry := ct.entries[provider]
	if entry == nil {
		return
	}

	entry.ErrorCount = 0
	entry.FailureCounts = make(map[FailoverReason]int)
	entry.CooldownEnd = time.Time{}
	entry.DisabledUntil = time.Time{}
	entry.DisabledReason = ""
}

// IsAvailable 检查提供商是否可用（不在冷却或禁用状态）
//
// 参数：
// - provider: 提供商名称
//
// 返回：
// - true: 可用
// - false: 在冷却中或被禁用
func (ct *CooldownTracker) IsAvailable(provider string) bool {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	entry := ct.entries[provider]
	if entry == nil {
		return true // 无记录表示可用
	}

	now := ct.nowFunc()

	// 计费禁用优先检查（更长的冷却时间）
	if !entry.DisabledUntil.IsZero() && now.Before(entry.DisabledUntil) {
		return false
	}

	// 标准冷却检查
	if !entry.CooldownEnd.IsZero() && now.Before(entry.CooldownEnd) {
		return false
	}

	return true
}

// CooldownRemaining 返回提供商变为可用前还需等待的时间
//
// 参数：
// - provider: 提供商名称
//
// 返回：
// - 剩余冷却时间（如果已可用则返回 0）
func (ct *CooldownTracker) CooldownRemaining(provider string) time.Duration {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	entry := ct.entries[provider]
	if entry == nil {
		return 0
	}

	now := ct.nowFunc()
	var remaining time.Duration

	// 计费禁用剩余时间
	if !entry.DisabledUntil.IsZero() && now.Before(entry.DisabledUntil) {
		d := entry.DisabledUntil.Sub(now)
		if d > remaining {
			remaining = d
		}
	}

	// 标准冷却剩余时间
	if !entry.CooldownEnd.IsZero() && now.Before(entry.CooldownEnd) {
		d := entry.CooldownEnd.Sub(now)
		if d > remaining {
			remaining = d
		}
	}

	return remaining
}

// ErrorCount 返回提供商的当前错误计数
//
// 参数：
// - provider: 提供商名称
//
// 返回：
// - 错误次数（无记录返回 0）
func (ct *CooldownTracker) ErrorCount(provider string) int {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	entry := ct.entries[provider]
	if entry == nil {
		return 0
	}
	return entry.ErrorCount
}

// FailureCount 返回提供商特定失败原因的计数
//
// 参数：
// - provider: 提供商名称
// - reason: 失败原因分类
//
// 返回：
// - 该原因的失败次数（无记录返回 0）
func (ct *CooldownTracker) FailureCount(provider string, reason FailoverReason) int {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	entry := ct.entries[provider]
	if entry == nil {
		return 0
	}
	return entry.FailureCounts[reason]
}

// getOrCreate 获取或创建冷却条目
// 内部辅助函数，不加锁（由调用者负责加锁）
//
// 参数：
// - provider: 提供商名称
//
// 返回：
// - 冷却条目指针
func (ct *CooldownTracker) getOrCreate(provider string) *cooldownEntry {
	entry := ct.entries[provider]
	if entry == nil {
		entry = &cooldownEntry{
			FailureCounts: make(map[FailoverReason]int),
		}
		ct.entries[provider] = entry
	}
	return entry
}

// calculateStandardCooldown 计算标准指数退避冷却时间
// 公式来自 OpenClaw: min(1h, 1min * 5^min(n-1, 3))
//
// 冷却时间表：
// - 1 次错误 → 1 分钟
// - 2 次错误 → 5 分钟
// - 3 次错误 → 25 分钟
// - 4+ 次错误 → 1 小时（上限）
//
// 参数：
// - errorCount: 错误次数
//
// 返回：
// - 冷却时长
func calculateStandardCooldown(errorCount int) time.Duration {
	n := max(1, errorCount)
	exp := min(n-1, 3)
	ms := 60_000 * int(math.Pow(5, float64(exp)))
	ms = min(3_600_000, ms) // 上限 1 小时
	return time.Duration(ms) * time.Millisecond
}

// calculateBillingCooldown 计算计费问题特有的指数退避冷却时间
// 公式来自 OpenClaw: min(24h, 5h * 2^min(n-1, 10))
//
// 冷却时间表：
// - 1 次计费错误 → 5 小时
// - 2 次计费错误 → 10 小时
// - 3 次计费错误 → 20 小时
// - 4+ 次计费错误 → 24 小时（上限）
//
// 参数：
// - billingErrorCount: 计费错误次数
//
// 返回：
// - 冷却时长
func calculateBillingCooldown(billingErrorCount int) time.Duration {
	const baseMs = 5 * 60 * 60 * 1000 // 5 小时
	const maxMs = 24 * 60 * 60 * 1000 // 24 小时

	n := max(1, billingErrorCount)
	exp := min(n-1, 10)
	raw := float64(baseMs) * math.Pow(2, float64(exp))
	ms := int(math.Min(float64(maxMs), raw))
	return time.Duration(ms) * time.Millisecond
}
