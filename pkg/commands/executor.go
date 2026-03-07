package commands

import (
	"context"
	"fmt"
)

// Outcome 命令执行结果类型
type Outcome int

const (
	// OutcomePassthrough 表示输入应该继续通过正常的 agent 流程处理
	OutcomePassthrough Outcome = iota
	// OutcomeHandled 表示命令处理器已执行（无论是否有错误）
	OutcomeHandled
)

// ExecuteResult 命令执行结果
type ExecuteResult struct {
	Outcome Outcome  // 执行结果类型
	Command string   // 执行的命令名称
	Err     error    // 执行错误（如果有）
}

// Executor 命令执行器
// 负责根据注册表和运行时执行命令
type Executor struct {
	reg *Registry  // 命令注册表
	rt  *Runtime   // 运行时依赖
}

// NewExecutor 创建命令执行器
//
// 参数：
// - reg: 命令注册表
// - rt: 运行时依赖
//
// 返回：
// - 初始化好的 Executor 指针
func NewExecutor(reg *Registry, rt *Runtime) *Executor {
	return &Executor{reg: reg, rt: rt}
}

// Execute 执行两阶段命令决策：
// 1) handled: 立即执行命令
// 2) passthrough: 不是命令或故意延迟到 agent 逻辑处理
//
// 参数：
// - ctx: 上下文用于取消控制
// - req: 命令请求
//
// 返回：
// - ExecuteResult: 执行结果
func (e *Executor) Execute(ctx context.Context, req Request) ExecuteResult {
	cmdName, ok := parseCommandName(req.Text)
	if !ok {
		return ExecuteResult{Outcome: OutcomePassthrough}
	}

	if e == nil || e.reg == nil {
		return ExecuteResult{Outcome: OutcomePassthrough, Command: cmdName}
	}

	def, found := e.reg.Lookup(cmdName)
	if !found {
		return ExecuteResult{Outcome: OutcomePassthrough, Command: cmdName}
	}

	return e.executeDefinition(ctx, req, def)
}

func (e *Executor) executeDefinition(ctx context.Context, req Request, def Definition) ExecuteResult {
	// Ensure Reply is always non-nil so handlers don't need to check.
	if req.Reply == nil {
		req.Reply = func(string) error { return nil }
	}

	// Simple command — no sub-commands
	if len(def.SubCommands) == 0 {
		if def.Handler == nil {
			return ExecuteResult{Outcome: OutcomePassthrough, Command: def.Name}
		}
		err := def.Handler(ctx, req, e.rt)
		return ExecuteResult{Outcome: OutcomeHandled, Command: def.Name, Err: err}
	}

	// Sub-command routing
	subName := nthToken(req.Text, 1)
	if subName == "" {
		err := req.Reply("Usage: " + def.EffectiveUsage())
		return ExecuteResult{Outcome: OutcomeHandled, Command: def.Name, Err: err}
	}

	normalized := normalizeCommandName(subName)
	for _, sc := range def.SubCommands {
		if normalizeCommandName(sc.Name) == normalized {
			if sc.Handler == nil {
				return ExecuteResult{Outcome: OutcomePassthrough, Command: def.Name}
			}
			err := sc.Handler(ctx, req, e.rt)
			return ExecuteResult{Outcome: OutcomeHandled, Command: def.Name, Err: err}
		}
	}

	// Unknown sub-command
	err := req.Reply(fmt.Sprintf("Unknown option: %s. Usage: %s", subName, def.EffectiveUsage()))
	return ExecuteResult{Outcome: OutcomeHandled, Command: def.Name, Err: err}
}
