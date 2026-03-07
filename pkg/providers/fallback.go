// Package providers 提供 LLM 提供商的接口定义和实现
// 本文件包含降级链（FallbackChain）的实现
// 用于在主要 LLM 提供商失败时自动切换到备用提供商

package providers

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// FallbackChain 降级链，协调跨多个候选模型的降级流程
// 核心功能：
// 1. 按顺序尝试多个候选提供商/模型
// 2. 尊重冷却状态（跳过冷却中的提供商）
// 3. 错误分类决定是否降级
// 4. 记录所有尝试的详细信息
//
// 字段说明：
// - cooldown: 冷却追踪器，管理每个提供商的冷却状态
type FallbackChain struct {
	cooldown *CooldownTracker
}

// FallbackCandidate 降级候选，代表一个要尝试的模型/提供商
type FallbackCandidate struct {
	Provider string // 提供商名称（如 "openai", "anthropic"）
	Model    string // 模型名称（如 "gpt-4", "claude-sonnet-4-5"）
}

// FallbackResult 降级执行结果，包含成功响应和所有尝试的元数据
type FallbackResult struct {
	Response *LLMResponse      // 成功的 LLM 响应
	Provider string            // 成功的提供商
	Model    string            // 成功的模型
	Attempts []FallbackAttempt // 所有尝试的记录
}

// FallbackAttempt 记录降级链中的一次尝试
type FallbackAttempt struct {
	Provider string       // 提供商名称
	Model    string       // 模型名称
	Error    error        // 错误信息（如果失败）
	Reason   FailoverReason // 失败原因分类
	Duration time.Duration // 耗时
	Skipped  bool         // 是否被跳过（由于冷却）
}

// NewFallbackChain 创建一个新的降级链
// 参数：
// - cooldown: 冷却追踪器
//
// 返回：
// - 初始化好的 FallbackChain 指针
func NewFallbackChain(cooldown *CooldownTracker) *FallbackChain {
	return &FallbackChain{cooldown: cooldown}
}

// ResolveCandidates 解析模型配置为去重的候选列表
// 参数：
// - cfg: 模型配置（包含主模型和降级列表）
// - defaultProvider: 默认提供商（当模型名没有协议前缀时使用）
//
// 返回：
// - 去重后的候选列表
func ResolveCandidates(cfg ModelConfig, defaultProvider string) []FallbackCandidate {
	return ResolveCandidatesWithLookup(cfg, defaultProvider, nil)
}

// ResolveCandidatesWithLookup 解析模型配置为去重的候选列表，支持查找表解析
// 与 ResolveCandidates 的区别：支持通过 lookup 函数解析模型别名
//
// 参数：
// - cfg: 模型配置
// - defaultProvider: 默认提供商
// - lookup: 可选的查找函数，用于将模型别名解析为完整模型名
//
// 返回：
// - 去重后的候选列表
func ResolveCandidatesWithLookup(
	cfg ModelConfig,
	defaultProvider string,
	lookup func(raw string) (resolved string, ok bool),
) []FallbackCandidate {
	seen := make(map[string]bool) // 用于去重
	var candidates []FallbackCandidate

	// 添加候选的辅助函数
	addCandidate := func(raw string) {
		candidateRaw := strings.TrimSpace(raw)
		// 如果有 lookup 函数，尝试解析模型别名
		if lookup != nil {
			if resolved, ok := lookup(candidateRaw); ok {
				candidateRaw = resolved
			}
		}

		// 解析模型引用（提供商/模型）
		ref := ParseModelRef(candidateRaw, defaultProvider)
		if ref == nil {
			return // 无效的模型引用，跳过
		}
		key := ModelKey(ref.Provider, ref.Model)
		if seen[key] {
			return // 已存在，跳过（去重）
		}
		seen[key] = true
		candidates = append(candidates, FallbackCandidate{
			Provider: ref.Provider,
			Model:    ref.Model,
		})
	}

	// 优先添加主模型
	addCandidate(cfg.Primary)

	// 然后添加降级模型
	for _, fb := range cfg.Fallbacks {
		addCandidate(fb)
	}

	return candidates
}

// Execute 执行降级链，用于文本/聊天请求
// 按顺序尝试每个候选，尊重冷却和错误分类
//
// 行为说明：
// 1. 冷却中的候选被跳过（记录为跳过的尝试）
// 2. context.Canceled 立即中止（用户取消，不降级）
// 3. 不可降级错误（格式错误）立即中止
// 4. 可降级错误触发降级到下一个候选
// 5. 成功标记提供商为良好（重置冷却）
// 6. 如果全部失败，返回包含所有尝试的聚合错误
//
// 参数：
// - ctx: 上下文用于取消控制
// - candidates: 候选列表
// - run: 执行函数，接收提供商和模型名称，返回 LLM 响应
//
// 返回：
// - FallbackResult: 包含成功响应和所有尝试记录
// - error: 如果所有候选都失败，返回 FallbackExhaustedError
func (fc *FallbackChain) Execute(
	ctx context.Context,
	candidates []FallbackCandidate,
	run func(ctx context.Context, provider, model string) (*LLMResponse, error),
) (*FallbackResult, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("fallback: no candidates configured")
	}

	result := &FallbackResult{
		Attempts: make([]FallbackAttempt, 0, len(candidates)),
	}

	for i, candidate := range candidates {
		// 在每次尝试前检查上下文
		if ctx.Err() == context.Canceled {
			return nil, context.Canceled
		}

		// 检查冷却状态
		if !fc.cooldown.IsAvailable(candidate.Provider) {
			remaining := fc.cooldown.CooldownRemaining(candidate.Provider)
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Provider: candidate.Provider,
				Model:    candidate.Model,
				Skipped:  true,
				Reason:   FailoverRateLimit,
				Error: fmt.Errorf(
					"provider %s in cooldown (%s remaining)",
					candidate.Provider,
					remaining.Round(time.Second),
				),
			})
			continue
		}

		// 执行请求
		start := time.Now()
		resp, err := run(ctx, candidate.Provider, candidate.Model)
		elapsed := time.Since(start)

		if err == nil {
			// 成功：标记提供商为良好
			fc.cooldown.MarkSuccess(candidate.Provider)
			result.Response = resp
			result.Provider = candidate.Provider
			result.Model = candidate.Model
			return result, nil
		}

		// 上下文取消：立即中止，不降级
		if ctx.Err() == context.Canceled {
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Provider: candidate.Provider,
				Model:    candidate.Model,
				Error:    err,
				Duration: elapsed,
			})
			return nil, context.Canceled
		}

		// 分类错误
		failErr := ClassifyError(err, candidate.Provider, candidate.Model)

		if failErr == nil {
			// 无法分类的错误：不降级，立即返回
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Provider: candidate.Provider,
				Model:    candidate.Model,
				Error:    err,
				Duration: elapsed,
			})
			return nil, fmt.Errorf("fallback: unclassified error from %s/%s: %w",
				candidate.Provider, candidate.Model, err)
		}

		// 不可降级错误：立即中止
		if !failErr.IsRetriable() {
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Provider: candidate.Provider,
				Model:    candidate.Model,
				Error:    failErr,
				Reason:   failErr.Reason,
				Duration: elapsed,
			})
			return nil, failErr
		}

		// 可降级错误：标记失败并继续下一个候选
		fc.cooldown.MarkFailure(candidate.Provider, failErr.Reason)
		result.Attempts = append(result.Attempts, FallbackAttempt{
			Provider: candidate.Provider,
			Model:    candidate.Model,
			Error:    failErr,
			Reason:   failErr.Reason,
			Duration: elapsed,
		})

		// 如果是最后一个候选，返回聚合错误
		if i == len(candidates)-1 {
			return nil, &FallbackExhaustedError{Attempts: result.Attempts}
		}
	}

	// 所有候选都被跳过（都在冷却中）
	return nil, &FallbackExhaustedError{Attempts: result.Attempts}
}

// ExecuteImage 执行降级链，用于图像/视觉请求
// 比 Execute 简单：没有冷却检查（图像端点有不同的速率限制）
// 图像尺寸/大小错误立即中止（不可降级）
//
// 参数：
// - ctx: 上下文用于取消控制
// - candidates: 候选列表
// - run: 执行函数，接收提供商和模型名称，返回 LLM 响应
//
// 返回：
// - FallbackResult: 包含成功响应和所有尝试记录
// - error: 如果所有候选都失败，返回 FallbackExhaustedError
func (fc *FallbackChain) ExecuteImage(
	ctx context.Context,
	candidates []FallbackCandidate,
	run func(ctx context.Context, provider, model string) (*LLMResponse, error),
) (*FallbackResult, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("image fallback: no candidates configured")
	}

	result := &FallbackResult{
		Attempts: make([]FallbackAttempt, 0, len(candidates)),
	}

	for i, candidate := range candidates {
		if ctx.Err() == context.Canceled {
			return nil, context.Canceled
		}

		start := time.Now()
		resp, err := run(ctx, candidate.Provider, candidate.Model)
		elapsed := time.Since(start)

		if err == nil {
			result.Response = resp
			result.Provider = candidate.Provider
			result.Model = candidate.Model
			return result, nil
		}

		if ctx.Err() == context.Canceled {
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Provider: candidate.Provider,
				Model:    candidate.Model,
				Error:    err,
				Duration: elapsed,
			})
			return nil, context.Canceled
		}

		// 图像尺寸/大小错误不可降级
		errMsg := strings.ToLower(err.Error())
		if IsImageDimensionError(errMsg) || IsImageSizeError(errMsg) {
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Provider: candidate.Provider,
				Model:    candidate.Model,
				Error:    err,
				Reason:   FailoverFormat,
				Duration: elapsed,
			})
			return nil, &FailoverError{
				Reason:   FailoverFormat,
				Provider: candidate.Provider,
				Model:    candidate.Model,
				Wrapped:  err,
			}
		}

		// 任何其他错误：记录并尝试下一个
		result.Attempts = append(result.Attempts, FallbackAttempt{
			Provider: candidate.Provider,
			Model:    candidate.Model,
			Error:    err,
			Duration: elapsed,
		})

		if i == len(candidates)-1 {
			return nil, &FallbackExhaustedError{Attempts: result.Attempts}
		}
	}

	return nil, &FallbackExhaustedError{Attempts: result.Attempts}
}

// FallbackExhaustedError 表示所有降级候选都已尝试并失败
type FallbackExhaustedError struct {
	Attempts []FallbackAttempt // 所有尝试的记录
}

// Error 实现 error 接口
func (e *FallbackExhaustedError) Error() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("fallback: all %d candidates failed:", len(e.Attempts)))
	for i, a := range e.Attempts {
		if a.Skipped {
			sb.WriteString(fmt.Sprintf("\n  [%d] %s/%s: skipped (cooldown)", i+1, a.Provider, a.Model))
		} else {
			sb.WriteString(fmt.Sprintf("\n  [%d] %s/%s: %v (reason=%s, %s)",
				i+1, a.Provider, a.Model, a.Error, a.Reason, a.Duration.Round(time.Millisecond)))
		}
	}
	return sb.String()
}
