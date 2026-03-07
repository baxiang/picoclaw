// Package agent 提供了 PicoClaw AI 代理的核心实现
// 本文件主要包含 AgentInstance 结构体及其相关函数
// AgentInstance 代表一个完全配置的代理实例，包含独立的工作空间、会话管理器、上下文构建器和工具注册表
package agent

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"      // 配置管理
	"github.com/sipeed/picoclaw/pkg/providers"   // LLM 提供商接口
	"github.com/sipeed/picoclaw/pkg/routing"     // 消息路由系统
	"github.com/sipeed/picoclaw/pkg/session"     // 会话管理
	"github.com/sipeed/picoclaw/pkg/tools"       // 工具实现
)

// AgentInstance 代表一个完全配置的 AI 代理实例
// 每个代理实例都有自己独立的工作空间、会话历史、工具集和配置
//
// 字段说明：
// - ID: 代理标识符（如 "main" 或自定义 ID）
// - Name: 代理名称（可选的人类可读名称）
// - Model: 主模型名称
// - Fallbacks: 备用模型列表，主模型失败时使用
// - Workspace: 工作空间目录路径，包含会话、记忆、技能等
// - MaxIterations: 最大工具迭代次数，防止无限工具调用
// - MaxTokens: 最大令牌数，限制 LLM 响应长度
// - Temperature: 温度参数，控制 LLM 输出的随机性（0-1）
// - ThinkingLevel: 思考级别配置（off/low/medium/high 等）
// - ContextWindow: 上下文窗口大小（令牌数）
// - SummarizeMessageThreshold: 触发摘要的消息数量阈值
// - SummarizeTokenPercent: 触发摘要的令牌百分比阈值
// - Provider: LLM 提供商接口实现
// - Sessions: 会话管理器，管理对话历史和摘要
// - ContextBuilder: 上下文构建器，构建 LLM 请求的系统提示和消息
// - Tools: 工具注册表，管理所有可用工具
// - Subagents: 子代理配置，控制子代理的创建和使用
// - SkillsFilter: 技能过滤器，限制可用的技能列表
// - Candidates: LLM 降级候选列表，用于故障转移
// - Router: 模型路由器，根据消息复杂度选择使用主模型还是轻模型
// - LightCandidates: 轻模型候选列表，用于简单任务以节省成本
type AgentInstance struct {
	ID                        string
	Name                      string
	Model                     string
	Fallbacks                 []string
	Workspace                 string
	MaxIterations             int
	MaxTokens                 int
	Temperature               float64
	ThinkingLevel             ThinkingLevel
	ContextWindow             int
	SummarizeMessageThreshold int
	SummarizeTokenPercent     int
	Provider                  providers.LLMProvider
	Sessions                  *session.SessionManager
	ContextBuilder            *ContextBuilder
	Tools                     *tools.ToolRegistry
	Subagents                 *config.SubagentsConfig
	SkillsFilter              []string
	Candidates                []providers.FallbackCandidate

	// Router: 当配置了模型路由且轻模型成功解析时非 nil
	// 对每个入站消息进行评分，决定路由到 LightCandidates 还是使用 Candidates
	Router *routing.Router
	// LightCandidates: 轻模型的提供商候选列表
	// 在代理创建时预计算，避免运行时重复查找 model_list
	LightCandidates []providers.FallbackCandidate
}

// NewAgentInstance 从配置创建一个代理实例
// 这是代理实例的主要构造函数，负责：
// 1. 解析工作空间、模型、备用配置
// 2. 注册基础工具（文件操作、执行等）
// 3. 初始化会话管理器和上下文构建器
// 4. 配置模型降级和路由
//
// 参数：
// - agentCfg: 代理特定配置，可为 nil（使用默认配置）
// - defaults: 默认代理配置
// - cfg: 全局配置
// - provider: LLM 提供商接口
//
// 返回：
// - 初始化好的 AgentInstance 指针
func NewAgentInstance(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	cfg *config.Config,
	provider providers.LLMProvider,
) *AgentInstance {
	// 解析工作空间目录并创建
	workspace := resolveAgentWorkspace(agentCfg, defaults)
	os.MkdirAll(workspace, 0o755)

	// 解析模型和备用配置
	model := resolveAgentModel(agentCfg, defaults)
	fallbacks := resolveAgentFallbacks(agentCfg, defaults)

	// 计算路径限制配置
	restrict := defaults.RestrictToWorkspace
	readRestrict := restrict && !defaults.AllowReadOutsideWorkspace

	// 从配置编译路径白名单模式（正则表达式）
	allowReadPaths := compilePatterns(cfg.Tools.AllowReadPaths)
	allowWritePaths := compilePatterns(cfg.Tools.AllowWritePaths)

	// 创建工具注册表
	toolsRegistry := tools.NewToolRegistry()

	// --- 注册文件操作工具 ---
	// 读取文件工具
	if cfg.Tools.IsToolEnabled("read_file") {
		toolsRegistry.Register(tools.NewReadFileTool(workspace, readRestrict, allowReadPaths))
	}
	// 写入文件工具
	if cfg.Tools.IsToolEnabled("write_file") {
		toolsRegistry.Register(tools.NewWriteFileTool(workspace, restrict, allowWritePaths))
	}
	// 列出目录工具
	if cfg.Tools.IsToolEnabled("list_dir") {
		toolsRegistry.Register(tools.NewListDirTool(workspace, readRestrict, allowReadPaths))
	}
	// Shell 执行工具
	if cfg.Tools.IsToolEnabled("exec") {
		execTool, err := tools.NewExecToolWithConfig(workspace, restrict, cfg)
		if err != nil {
			log.Fatalf("Critical error: unable to initialize exec tool: %v", err)
		}
		toolsRegistry.Register(execTool)
	}

	// --- 注册文件编辑工具 ---
	// 编辑文件工具（行级编辑）
	if cfg.Tools.IsToolEnabled("edit_file") {
		toolsRegistry.Register(tools.NewEditFileTool(workspace, restrict, allowWritePaths))
	}
	// 追加文件工具（在文件末尾添加内容）
	if cfg.Tools.IsToolEnabled("append_file") {
		toolsRegistry.Register(tools.NewAppendFileTool(workspace, restrict, allowWritePaths))
	}

	// 创建会话管理器（存储在 workspace/sessions 目录）
	sessionsDir := filepath.Join(workspace, "sessions")
	sessionsManager := session.NewSessionManager(sessionsDir)

	// 创建上下文构建器（用于构建 LLM 请求的系统提示和消息）
	contextBuilder := NewContextBuilder(workspace)

	// 设置代理 ID 和名称
	agentID := routing.DefaultAgentID
	agentName := ""
	var subagents *config.SubagentsConfig
	var skillsFilter []string

	if agentCfg != nil {
		agentID = routing.NormalizeAgentID(agentCfg.ID)
		agentName = agentCfg.Name
		subagents = agentCfg.Subagents
		skillsFilter = agentCfg.Skills
	}

	// --- 设置默认参数 ---
	// 最大工具迭代次数（默认 20）
	maxIter := defaults.MaxToolIterations
	if maxIter == 0 {
		maxIter = 20
	}

	// 最大令牌数（默认 8192）
	maxTokens := defaults.MaxTokens
	if maxTokens == 0 {
		maxTokens = 8192
	}

	// 温度参数（默认 0.7）
	temperature := 0.7
	if defaults.Temperature != nil {
		temperature = *defaults.Temperature
	}

	// 思考级别配置（从模型配置获取）
	var thinkingLevelStr string
	if mc, err := cfg.GetModelConfig(model); err == nil {
		thinkingLevelStr = mc.ThinkingLevel
	}
	thinkingLevel := parseThinkingLevel(thinkingLevelStr)

	// 会话摘要阈值（默认 20 条消息）
	summarizeMessageThreshold := defaults.SummarizeMessageThreshold
	if summarizeMessageThreshold == 0 {
		summarizeMessageThreshold = 20
	}

	// 会话摘要令牌百分比阈值（默认 75%）
	summarizeTokenPercent := defaults.SummarizeTokenPercent
	if summarizeTokenPercent == 0 {
		summarizeTokenPercent = 75
	}

	// --- 解析降级候选列表 ---
	modelCfg := providers.ModelConfig{
		Primary:   model,
		Fallbacks: fallbacks,
	}
	// 定义模型解析函数，支持从 model_list 查找
	resolveFromModelList := func(raw string) (string, bool) {
		// 确保模型名称包含协议前缀（如 openai/gpt-4）
		ensureProtocol := func(model string) string {
			model = strings.TrimSpace(model)
			if model == "" {
				return ""
			}
			if strings.Contains(model, "/") {
				return model
			}
			return "openai/" + model
		}

		raw = strings.TrimSpace(raw)
		if raw == "" {
			return "", false
		}

		if cfg != nil {
			// 从 GetModelConfig 查找
			if mc, err := cfg.GetModelConfig(raw); err == nil && mc != nil && strings.TrimSpace(mc.Model) != "" {
				return ensureProtocol(mc.Model), true
			}

			// 从 ModelList 遍历查找
			for i := range cfg.ModelList {
				fullModel := strings.TrimSpace(cfg.ModelList[i].Model)
				if fullModel == "" {
					continue
				}
				if fullModel == raw {
					return ensureProtocol(fullModel), true
				}
				_, modelID := providers.ExtractProtocol(fullModel)
				if modelID == raw {
					return ensureProtocol(fullModel), true
				}
			}
		}

		return "", false
	}

	// 解析候选列表（支持降级）
	candidates := providers.ResolveCandidatesWithLookup(modelCfg, defaults.Provider, resolveFromModelList)

	// --- 模型路由设置 ---
	// 预解析轻模型候选列表，避免运行时重复查找 model_list
	var router *routing.Router
	var lightCandidates []providers.FallbackCandidate
	if rc := defaults.Routing; rc != nil && rc.Enabled && rc.LightModel != "" {
		lightModelCfg := providers.ModelConfig{Primary: rc.LightModel}
		resolved := providers.ResolveCandidatesWithLookup(lightModelCfg, defaults.Provider, resolveFromModelList)
		if len(resolved) > 0 {
			router = routing.New(routing.RouterConfig{
				LightModel: rc.LightModel,
				Threshold:  rc.Threshold,
			})
			lightCandidates = resolved
		} else {
			log.Printf("routing: light_model %q not found in model_list — routing disabled for agent %q",
				rc.LightModel, agentID)
		}
	}

	// 返回初始化好的代理实例
	return &AgentInstance{
		ID:                        agentID,
		Name:                      agentName,
		Model:                     model,
		Fallbacks:                 fallbacks,
		Workspace:                 workspace,
		MaxIterations:             maxIter,
		MaxTokens:                 maxTokens,
		Temperature:               temperature,
		ThinkingLevel:             thinkingLevel,
		ContextWindow:             maxTokens,
		SummarizeMessageThreshold: summarizeMessageThreshold,
		SummarizeTokenPercent:     summarizeTokenPercent,
		Provider:                  provider,
		Sessions:                  sessionsManager,
		ContextBuilder:            contextBuilder,
		Tools:                     toolsRegistry,
		Subagents:                 subagents,
		SkillsFilter:              skillsFilter,
		Candidates:                candidates,
		Router:                    router,
		LightCandidates:           lightCandidates,
	}
}

// resolveAgentWorkspace 确定代理的工作空间目录
// 解析逻辑：
// 1. 如果代理配置中有显式 workspace，使用它（展开 ~ 为家目录）
// 2. 如果是默认代理或主代理，使用默认工作空间
// 3. 对于命名代理，使用默认工作空间的兄弟目录 workspace-{agentID}
//
// 参数：
// - agentCfg: 代理配置，可为 nil
// - defaults: 默认配置
//
// 返回：
// - 工作空间目录路径
func resolveAgentWorkspace(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) string {
	// 优先使用代理配置中的显式 workspace
	if agentCfg != nil && strings.TrimSpace(agentCfg.Workspace) != "" {
		return expandHome(strings.TrimSpace(agentCfg.Workspace))
	}
	// 使用配置的默认工作空间（尊重 PICOCLAW_HOME）
	if agentCfg == nil || agentCfg.Default || agentCfg.ID == "" || routing.NormalizeAgentID(agentCfg.ID) == "main" {
		return expandHome(defaults.Workspace)
	}
	// 对于没有显式 workspace 的命名代理，使用默认 workspace 的兄弟目录
	id := routing.NormalizeAgentID(agentCfg.ID)
	return filepath.Join(expandHome(defaults.Workspace), "..", "workspace-"+id)
}

// resolveAgentModel 解析代理的主模型
// 解析逻辑：
// 1. 如果代理配置中有显式模型，使用它
// 2. 否则使用默认配置中的模型名称
//
// 参数：
// - agentCfg: 代理配置
// - defaults: 默认配置
//
// 返回：
// - 模型名称字符串
func resolveAgentModel(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) string {
	if agentCfg != nil && agentCfg.Model != nil && strings.TrimSpace(agentCfg.Model.Primary) != "" {
		return strings.TrimSpace(agentCfg.Model.Primary)
	}
	return defaults.GetModelName()
}

// resolveAgentFallbacks 解析代理的备用模型列表
// 解析逻辑：
// 1. 如果代理配置中有显式 fallbacks，使用它
// 2. 否则使用默认配置中的 fallbacks
//
// 参数：
// - agentCfg: 代理配置
// - defaults: 默认配置
//
// 返回：
// - 备用模型列表
func resolveAgentFallbacks(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) []string {
	if agentCfg != nil && agentCfg.Model != nil && agentCfg.Model.Fallbacks != nil {
		return agentCfg.Model.Fallbacks
	}
	return defaults.ModelFallbacks
}

// compilePatterns 将字符串模式列表编译为正则表达式列表
// 用于路径白名单/黑名单匹配
//
// 参数：
// - patterns: 字符串模式列表
//
// 返回：
// - 编译后的正则表达式列表（无效模式会被跳过并打印警告）
func compilePatterns(patterns []string) []*regexp.Regexp {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			fmt.Printf("Warning: invalid path pattern %q: %v\n", p, err)
			continue
		}
		compiled = append(compiled, re)
	}
	return compiled
}

// expandHome 展开路径中的家目录符号 (~)
// 将 ~/xxx 转换为 /home/user/xxx 或 C:\\Users\\user\\xxx
//
// 参数：
// - path: 可能包含 ~ 的路径
//
// 返回：
// - 展开后的路径
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
