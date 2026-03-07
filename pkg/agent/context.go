// Package agent 提供了 PicoClaw AI 代理的核心实现
// 本文件主要包含 ContextBuilder 结构体及其相关函数
// ContextBuilder 负责构建 LLM 请求的系统提示和消息上下文
// 核心功能：
// - 系统提示缓存（避免重复构建开销）
// - 文件变更检测（基于 mtime 的自动失效）
// - 技能系统加载
// - 记忆上下文管理
package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"   // 日志系统
	"github.com/sipeed/picoclaw/pkg/providers" // LLM 提供商接口
	"github.com/sipeed/picoclaw/pkg/skills"    // 技能系统
)

// ContextBuilder 上下文构建器
// 负责构建 LLM 请求的系统提示和消息列表
// 包含系统提示缓存机制，避免每次请求都重新构建（优化性能）
//
// 字段说明：
// - workspace: 工作空间目录路径
// - skillsLoader: 技能加载器，加载和管理技能
// - memory: 记忆存储，管理长期记忆和日记
// - systemPromptMutex: 保护缓存的读写锁
// - cachedSystemPrompt: 缓存的系统提示内容
// - cachedAt: 缓存构建时的时间戳（最大文件修改时间）
// - existedAtCache: 缓存构建时存在的文件路径快照
// - skillFilesAtCache: 缓存构建时技能文件的快照（用于检测技能文件变更）
type ContextBuilder struct {
	workspace    string
	skillsLoader *skills.SkillsLoader
	memory       *MemoryStore

	// 系统提示缓存，避免每次调用都重新构建
	// 修复 issue #607：重复重新处理整个上下文的问题
	// 缓存通过 mtime 检查自动失效（当工作空间源文件变更时）
	systemPromptMutex  sync.RWMutex
	cachedSystemPrompt string
	cachedAt           time.Time // 缓存构建时所有跟踪路径的最大 mtime

	// existedAtCache 跟踪缓存构建时哪些源文件路径存在
	// 用于检测新创建的文件（缓存时不存在，现在存在）
	// 或删除的文件（缓存时存在，现在不存在）—— 两者都应触发缓存重建
	existedAtCache map[string]bool

	// skillFilesAtCache 在缓存构建时快照技能树文件集和 mtime
	// 用于捕获嵌套文件的创建/删除/mtime 变更
	// 这些变更可能不会更新顶层技能根目录的 mtime
	skillFilesAtCache map[string]time.Time
}

// getGlobalConfigDir 获取全局配置目录路径
// 优先使用 PICOCLAW_HOME 环境变量，否则使用 ~/.picoclaw
//
// 返回：
// - 全局配置目录路径
func getGlobalConfigDir() string {
	if home := os.Getenv("PICOCLAW_HOME"); home != "" {
		return home
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".picoclaw")
}

// NewContextBuilder 创建一个新的上下文构建器
// 参数：
// - workspace: 工作空间目录路径
//
// 初始化内容：
// 1. 内置技能目录（当前工作目录的 skills/ 或 PICOCLAW_BUILTIN_SKILLS 环境变量）
// 2. 全局技能目录（~/.picoclaw/skills）
// 3. 技能加载器（管理三个技能来源）
// 4. 记忆存储（工作空间的 memory 目录）
//
// 返回：
// - 初始化好的 ContextBuilder 指针
func NewContextBuilder(workspace string) *ContextBuilder {
	// 内置技能：当前项目的 skills 目录
	// 使用当前工作目录下的 skills/ 目录
	builtinSkillsDir := strings.TrimSpace(os.Getenv("PICOCLAW_BUILTIN_SKILLS"))
	if builtinSkillsDir == "" {
		wd, _ := os.Getwd()
		builtinSkillsDir = filepath.Join(wd, "skills")
	}
	// 全局技能目录
	globalSkillsDir := filepath.Join(getGlobalConfigDir(), "skills")

	return &ContextBuilder{
		workspace:    workspace,
		skillsLoader: skills.NewSkillsLoader(workspace, globalSkillsDir, builtinSkillsDir),
		memory:       NewMemoryStore(workspace),
	}
}

// getIdentity 获取 AI 助手的身份定义
// 返回一个格式化的 Markdown 字符串，包含：
// 1. 基本身份介绍（picoclaw 助手）
// 2. 工作空间路径和信息
// 3. 重要规则（必须使用工具、诚实准确、记忆更新、上下文摘要说明）
//
// 返回：
// - 身份定义字符串（Markdown 格式）
func (cb *ContextBuilder) getIdentity() string {
	workspacePath, _ := filepath.Abs(filepath.Join(cb.workspace))

	return fmt.Sprintf(`# picoclaw 🦞

You are picoclaw, a helpful AI assistant.

## Workspace
Your workspace is at: %s
- Memory: %s/memory/MEMORY.md
- Daily Notes: %s/memory/YYYYMM/YYYYMMDD.md
- Skills: %s/skills/{skill-name}/SKILL.md

## Important Rules

1. **ALWAYS use tools** - When you need to perform an action (schedule reminders, send messages, execute commands, etc.), you MUST call the appropriate tool. Do NOT just say you'll do it or pretend to do it.

2. **Be helpful and accurate** - When using tools, briefly explain what you're doing.

3. **Memory** - When interacting with me if something seems memorable, update %s/memory/MEMORY.md

4. **Context summaries** - Conversation summaries provided as context are approximate references only. They may be incomplete or outdated. Always defer to explicit user instructions over summary content.`,
		workspacePath, workspacePath, workspacePath, workspacePath, workspacePath)
}

// BuildSystemPrompt 构建完整的系统提示
// 包含四个部分：
// 1. 核心身份（getIdentity）
// 2. 引导文件（AGENTS.md, SOUL.md, USER.md, IDENTITY.md）
// 3. 技能摘要（AI 可通过 read_file 工具读取完整内容）
// 4. 记忆上下文（MEMORY.md 和日记）
//
// 返回：
// - 完整的系统提示字符串（使用 "---" 分隔各部分）
func (cb *ContextBuilder) BuildSystemPrompt() string {
	parts := []string{}

	// 核心身份部分
	parts = append(parts, cb.getIdentity())

	// 引导文件（AGENTS.md, SOUL.md, USER.md, IDENTITY.md）
	bootstrapContent := cb.LoadBootstrapFiles()
	if bootstrapContent != "" {
		parts = append(parts, bootstrapContent)
	}

	// 技能 - 显示摘要，AI 可通过 read_file 工具读取完整内容
	skillsSummary := cb.skillsLoader.BuildSkillsSummary()
	if skillsSummary != "" {
		parts = append(parts, fmt.Sprintf(`# Skills

The following skills extend your capabilities. To use a skill, read its SKILL.md file using the read_file tool.

%s`, skillsSummary))
	}

	// 记忆上下文
	memoryContext := cb.memory.GetMemoryContext()
	if memoryContext != "" {
		parts = append(parts, "# Memory\n\n"+memoryContext)
	}

	// 使用 "---" 分隔符连接各部分
	return strings.Join(parts, "\n\n---\n\n")
}

// BuildSystemPromptWithCache 带缓存的系统提示构建
// 如果缓存可用且源文件未变更，返回缓存的系统提示
// 否则重新构建并缓存
//
// 缓存失效检测：
// - 通过 mtime 检查检测源文件变更（廉价的 stat 调用）
// - 跟踪文件存在状态（检测新建和删除）
// - 技能文件递归快照（检测嵌套变更）
//
// 线程安全：
// - 使用读写锁保护缓存
// - 先尝试读锁（快速路径）
// - 需要重建时获取写锁
//
// 返回：
// - 系统提示字符串（缓存或新建）
func (cb *ContextBuilder) BuildSystemPromptWithCache() string {
	// 先尝试读锁 —— 缓存有效时的快速路径
	cb.systemPromptMutex.RLock()
	if cb.cachedSystemPrompt != "" && !cb.sourceFilesChangedLocked() {
		result := cb.cachedSystemPrompt
		cb.systemPromptMutex.RUnlock()
		return result
	}
	cb.systemPromptMutex.RUnlock()

	// 获取写锁用于构建
	cb.systemPromptMutex.Lock()
	defer cb.systemPromptMutex.Unlock()

	// 双重检查：可能在等待锁时另一个 goroutine 已重建
	if cb.cachedSystemPrompt != "" && !cb.sourceFilesChangedLocked() {
		return cb.cachedSystemPrompt
	}

	// 在构建提示前快照基线（存在性 + 最大 mtime）
	// 这样 cachedAt 反映构建前的状态：
	// 如果文件在 BuildSystemPrompt 期间被修改，
	// 新的 mtime 会 > baseline.maxMtime，
	// 下次 sourceFilesChangedLocked 检查会正确触发重建
	baseline := cb.buildCacheBaseline()
	prompt := cb.BuildSystemPrompt()
	cb.cachedSystemPrompt = prompt
	cb.cachedAt = baseline.maxMtime
	cb.existedAtCache = baseline.existed
	cb.skillFilesAtCache = baseline.skillFiles

	logger.DebugCF("agent", "System prompt cached",
		map[string]any{
			"length": len(prompt),
		})

	return prompt
}

// InvalidateCache 清除缓存的系统提示
// 通常不需要调用，因为缓存通过 mtime 检查自动失效
// 但在测试或显式重载命令时有用
func (cb *ContextBuilder) InvalidateCache() {
	cb.systemPromptMutex.Lock()
	defer cb.systemPromptMutex.Unlock()

	cb.cachedSystemPrompt = ""
	cb.cachedAt = time.Time{}
	cb.existedAtCache = nil
	cb.skillFilesAtCache = nil

	logger.DebugCF("agent", "System prompt cache invalidated", nil)
}

// sourcePaths 返回需要跟踪的非技能工作空间源文件路径
// 用于缓存失效检测（引导文件 + 记忆）
// 技能根目录单独处理，因为它们需要目录级和递归文件级检查
//
// 返回：
// - 源文件路径列表（AGENTS.md, SOUL.md, USER.md, IDENTITY.md, MEMORY.md）
func (cb *ContextBuilder) sourcePaths() []string {
	return []string{
		filepath.Join(cb.workspace, "AGENTS.md"),
		filepath.Join(cb.workspace, "SOUL.md"),
		filepath.Join(cb.workspace, "USER.md"),
		filepath.Join(cb.workspace, "IDENTITY.md"),
		filepath.Join(cb.workspace, "memory", "MEMORY.md"),
	}
}

// skillRoots 返回所有影响 BuildSkillsSummary 输出的技能根目录
// 包括工作空间技能、全局技能和内置技能
//
// 返回：
// - 技能根目录路径列表
func (cb *ContextBuilder) skillRoots() []string {
	if cb.skillsLoader == nil {
		return []string{filepath.Join(cb.workspace, "skills")}
	}

	roots := cb.skillsLoader.SkillRoots()
	if len(roots) == 0 {
		return []string{filepath.Join(cb.workspace, "skills")}
	}
	return roots
}

// cacheBaseline 缓存基线结构
// 持有文件存在性快照和所有跟踪路径的最新观察 mtime
// 用作缓存的参考点
type cacheBaseline struct {
	existed    map[string]bool      // 文件存在性快照
	skillFiles map[string]time.Time // 技能文件 mtime 快照
	maxMtime   time.Time            // 最大修改时间
}

// buildCacheBaseline 记录哪些跟踪路径当前存在，并计算
// 所有跟踪文件 + 技能目录内容的最新 mtime
// 在缓存构建时调用（需持有写锁）
//
// 返回：
// - 缓存基线结构
func (cb *ContextBuilder) buildCacheBaseline() cacheBaseline {
	skillRoots := cb.skillRoots()

	// 所有跟踪路径：源文件 + 所有技能根目录
	allPaths := append(cb.sourcePaths(), skillRoots...)

	existed := make(map[string]bool, len(allPaths))
	skillFiles := make(map[string]time.Time)
	var maxMtime time.Time

	// 检查所有路径的存在性和 mtime
	for _, p := range allPaths {
		info, err := os.Stat(p)
		existed[p] = err == nil
		if err == nil && info.ModTime().After(maxMtime) {
			maxMtime = info.ModTime()
		}
	}

	// 递归遍历所有技能根目录，快照技能文件和 mtime
	// 使用 os.Stat（而不是 d.Info）以保持与 sourceFilesChanged 检查一致
	for _, root := range skillRoots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr == nil && !d.IsDir() {
				if info, err := os.Stat(path); err == nil {
					skillFiles[path] = info.ModTime()
					if info.ModTime().After(maxMtime) {
						maxMtime = info.ModTime()
					}
				}
			}
			return nil
		})
	}

	// 如果没有跟踪的文件（空工作空间），maxMtime 为零
	// 使用很旧的非零时间，以便：
	// 1. cachedAt.IsZero() 不会触发永久的重建
	// 2. 之后创建的任何真实文件的 mtime > cachedAt，能被 fileChangedSince 检测到
	if maxMtime.IsZero() {
		maxMtime = time.Unix(1, 0)
	}

	return cacheBaseline{existed: existed, skillFiles: skillFiles, maxMtime: maxMtime}
}

// sourceFilesChangedLocked 检查是否有任何工作空间源文件
// 自缓存上次构建后被修改、创建或删除
//
// 重要提示：调用者必须至少持有 systemPromptMutex 的读锁
// Go 的 sync.RWMutex 不可重入，所以此函数不能自己获取锁
// （当从已持有 RLock 或 Lock 的 BuildSystemPromptWithCache 调用时会死锁）
//
// 检测范围：
// 1. 引导文件（AGENTS.md 等）和记忆文件（MEMORY.md）
// 2. 技能根目录（创建/删除/mtime 变更）
// 3. 技能文件递归快照（嵌套文件变更）
//
// 返回：
// - true: 文件已变更，需要重建缓存
// - false: 文件未变更，缓存仍然有效
func (cb *ContextBuilder) sourceFilesChangedLocked() bool {
	if cb.cachedAt.IsZero() {
		return true
	}

	// 检查跟踪的源文件（引导文件 + 记忆）
	if slices.ContainsFunc(cb.sourcePaths(), cb.fileChangedSince) {
		return true
	}

	// --- 技能根目录（工作空间/全局/内置）---
	//
	// 对于每个根目录：
	// 1. 创建/删除和根目录 mtime 变更由 fileChangedSince 跟踪
	// 2. 嵌套文件创建/删除/mtime 变更由技能文件快照跟踪
	for _, root := range cb.skillRoots() {
		if cb.fileChangedSince(root) {
			return true
		}
	}
	// 检查技能文件快照是否有变更
	if skillFilesChangedSince(cb.skillRoots(), cb.skillFilesAtCache) {
		return true
	}

	return false
}

// fileChangedSince 返回 true 如果跟踪的源文件自缓存构建后
// 被修改、新建或删除
//
// 四种情况：
// - 缓存时存在，现在存在 -> 检查 mtime
// - 缓存时存在，现在不存在 -> 已变更（删除）
// - 缓存时不存在，现在存在 -> 已变更（新建）
// - 缓存时不存在，现在不存在 -> 无变更
//
// 参数：
// - path: 要检查的文件路径
//
// 返回：
// - true: 文件已变更
// - false: 文件未变更
func (cb *ContextBuilder) fileChangedSince(path string) bool {
	// 防御性：如果 existedAtCache 从未初始化，视为已变更
	// 这样缓存会重建而不是静默提供过期数据
	if cb.existedAtCache == nil {
		return true
	}

	existedBefore := cb.existedAtCache[path]
	info, err := os.Stat(path)
	existsNow := err == nil

	if existedBefore != existsNow {
		return true // 文件被创建或删除
	}
	if !existsNow {
		return false // 之前不存在，现在也不存在
	}
	return info.ModTime().After(cb.cachedAt) // 检查 mtime 是否变更
}

// errWalkStop 是一个哨兵错误，用于提前停止 filepath.WalkDir
// 使用专门的错误（而不是 fs.SkipAll）使提前退出的意图更明确
// 避免 nilerr linter 警告（当回调的 err 参数非 nil 时返回 nil 会触发）
var errWalkStop = errors.New("walk stop")

// skillFilesChangedSince 比较当前递归技能文件树与缓存时的快照
// 任何创建/删除/mtime 变更都会使缓存失效
//
// 参数：
// - skillRoots: 技能根目录列表
// - filesAtCache: 缓存时的文件 mtime 快照
//
// 返回：
// - true: 技能文件已变更
// - false: 技能文件未变更
func skillFilesChangedSince(skillRoots []string, filesAtCache map[string]time.Time) bool {
	// 防御性：如果快照从未初始化，强制重建
	if filesAtCache == nil {
		return true
	}

	// 检查缓存的文件仍然存在且 mtime 相同
	for path, cachedMtime := range filesAtCache {
		info, err := os.Stat(path)
		if err != nil {
			// 之前跟踪的文件消失了（或变得不可访问）：
			// 无论哪种情况，缓存的技能摘要现在可能已过时
			return true
		}
		if !info.ModTime().Equal(cachedMtime) {
			return true
		}
	}

	// 检查是否有任何新文件出现在技能根目录下
	changed := false
	for _, root := range skillRoots {
		if strings.TrimSpace(root) == "" {
			continue
		}

		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				// 将意外的 walk 错误视为已变更，以避免缓存过期
				if !os.IsNotExist(walkErr) {
					changed = true
					return errWalkStop
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			// 检查是否有新文件不在缓存快照中
			if _, ok := filesAtCache[path]; !ok {
				changed = true
				return errWalkStop
			}
			return nil
		})

		if changed {
			return true
		}
		if err != nil && !errors.Is(err, errWalkStop) && !os.IsNotExist(err) {
			logger.DebugCF("agent", "skills walk error", map[string]any{"error": err.Error()})
			return true
		}
	}

	return false
}

// LoadBootstrapFiles 加载引导文件内容
// 引导文件包括：AGENTS.md, SOUL.md, USER.md, IDENTITY.md
// 这些文件定义了 AI 的行为、身份和用户信息
//
// 返回：
// - 引导文件内容（Markdown 格式），如果文件不存在则跳过
func (cb *ContextBuilder) LoadBootstrapFiles() string {
	bootstrapFiles := []string{
		"AGENTS.md",
		"SOUL.md",
		"USER.md",
		"IDENTITY.md",
	}

	var sb strings.Builder
	for _, filename := range bootstrapFiles {
		filePath := filepath.Join(cb.workspace, filename)
		if data, err := os.ReadFile(filePath); err == nil {
			fmt.Fprintf(&sb, "## %s\n\n%s\n\n", filename, data)
		}
	}

	return sb.String()
}

// buildDynamicContext 构建简短的动态上下文字符串，包含每请求变化的信息
// 这包括：当前时间、运行时信息、会话信息
// 这会在每次请求时变化，所以不在缓存的系统提示中
//
// LLM 侧 KV 缓存复用通过各提供商适配器的原生机制实现：
// - Anthropic: 每块的 cache_control (ephemeral) 在静态 SystemParts 块上
// - OpenAI / Codex: prompt_cache_key 用于基于前缀的缓存
//
// 参数：
// - channel: 渠道名称
// - chatID: 聊天标识符
//
// 返回：
// - 动态上下文字符串
func (cb *ContextBuilder) buildDynamicContext(channel, chatID string) string {
	now := time.Now().Format("2006-01-02 15:04 (Monday)")
	rt := fmt.Sprintf("%s %s, Go %s", runtime.GOOS, runtime.GOARCH, runtime.Version())

	var sb strings.Builder
	fmt.Fprintf(&sb, "## Current Time\n%s\n\n## Runtime\n%s", now, rt)

	if channel != "" && chatID != "" {
		fmt.Fprintf(&sb, "\n\n## Current Session\nChannel: %s\nChat ID: %s", channel, chatID)
	}

	return sb.String()
}

// BuildMessages 构建完整的 LLM 消息列表
// 这是发送给 LLM 的完整上下文，包括：
// 1. 系统提示（静态缓存部分 + 动态部分 + 可选摘要）
// 2. 对话历史
// 3. 当前用户消息（可选带媒体）
//
// 参数：
// - history: 对话历史消息列表
// - summary: 对话摘要（可选）
// - currentMessage: 当前用户消息
// - media: 媒体文件引用列表
// - channel: 渠道名称
// - chatID: 聊天标识符
//
// 返回：
// - 完整的消息列表，第一个消息是系统消息
func (cb *ContextBuilder) BuildMessages(
	history []providers.Message,
	summary string,
	currentMessage string,
	media []string,
	channel, chatID string,
) []providers.Message {
	messages := []providers.Message{}

	// 静态部分（身份、引导文件、技能、记忆）被缓存以避免
	// 每次调用时重复的文件 I/O 和字符串构建（修复 issue #607）
	// 动态部分（时间、会话、摘要）在每次请求时追加
	// 所有内容作为单个系统消息发送以保持提供商兼容性：
	// - Anthropic 适配器提取 messages[0] (Role=="system") 并将其内容
	//   映射到 Messages API 请求的顶层 "system" 参数
	// - Codex 只将第一个系统消息映射到其 instructions 字段
	// - OpenAI 兼容原样传递消息
	staticPrompt := cb.BuildSystemPromptWithCache()

	// 构建简短的动态上下文（时间、运行时、会话）—— 每次请求变化
	dynamicCtx := cb.buildDynamicContext(channel, chatID)

	// 组合单个系统消息：静态（缓存）+ 动态 + 可选摘要
	// 将所有系统内容保持在一个消息中确保每个提供商适配器能正确提取
	//
	// SystemParts 携带相同的结构化为块的内容，以便
	// 缓存感知的适配器（Anthropic）可以设置每块 cache_control
	// 静态块被标记为 "ephemeral" —— 其前缀哈希在不同请求间稳定
	// 实现 LLM 侧 KV 缓存复用
	stringParts := []string{staticPrompt, dynamicCtx}

	contentBlocks := []providers.ContentBlock{
		{Type: "text", Text: staticPrompt, CacheControl: &providers.CacheControl{Type: "ephemeral"}},
		{Type: "text", Text: dynamicCtx},
	}

	// 如果有对话摘要，添加到系统消息
	if summary != "" {
		summaryText := fmt.Sprintf(
			"CONTEXT_SUMMARY: The following is an approximate summary of prior conversation "+
				"for reference only. It may be incomplete or outdated — always defer to explicit instructions.\n\n%s",
			summary)
		stringParts = append(stringParts, summaryText)
		contentBlocks = append(contentBlocks, providers.ContentBlock{Type: "text", Text: summaryText})
	}

	fullSystemPrompt := strings.Join(stringParts, "\n\n---\n\n")

	// 记录系统提示摘要用于调试（仅调试模式）
	// 在锁下读取 cachedSystemPrompt 以避免与并发 InvalidateCache / BuildSystemPromptWithCache 写入的数据竞争
	cb.systemPromptMutex.RLock()
	isCached := cb.cachedSystemPrompt != ""
	cb.systemPromptMutex.RUnlock()

	logger.DebugCF("agent", "System prompt built",
		map[string]any{
			"static_chars":  len(staticPrompt),
			"dynamic_chars": len(dynamicCtx),
			"total_chars":   len(fullSystemPrompt),
			"has_summary":   summary != "",
			"cached":        isCached,
		})

	// 记录系统提示预览（避免记录巨大内容）
	preview := fullSystemPrompt
	if len(preview) > 500 {
		preview = preview[:500] + "... (truncated)"
	}
	logger.DebugCF("agent", "System prompt preview",
		map[string]any{
			"preview": preview,
		})

	// 清理历史记录（移除无效消息）
	history = sanitizeHistoryForProvider(history)

	// 单个系统消息包含所有上下文 —— 兼容所有提供商
	// SystemParts 使缓存感知的适配器可以设置每块 cache_control
	// Content 是为不读取 SystemParts 的适配器的连接后备
	messages = append(messages, providers.Message{
		Role:        "system",
		Content:     fullSystemPrompt,
		SystemParts: contentBlocks,
	})

	// 添加对话历史
	messages = append(messages, history...)

	// 添加当前用户消息（如果有内容）
	if strings.TrimSpace(currentMessage) != "" {
		msg := providers.Message{
			Role:    "user",
			Content: currentMessage,
		}
		if len(media) > 0 {
			msg.Media = media
		}
		messages = append(messages, msg)
	}

	return messages
}

// sanitizeHistoryForProvider 清理对话历史，使其符合 LLM 提供商的 API 要求
// 主要处理：
// 1. 移除系统消息（BuildMessages 总是构建自己的单个系统消息）
// 2. 移除孤立的 tool 消息（没有对应的 assistant tool_calls）
// 3. 移除孤立的 assistant tool-call 消息（没有前驱 user/tool 消息）
// 4. 确保每个有 tool_calls 的 assistant 消息都有完整的 tool 结果
//
// 参数：
// - history: 原始对话历史
//
// 返回：
// - 清理后的对话历史
func sanitizeHistoryForProvider(history []providers.Message) []providers.Message {
	if len(history) == 0 {
		return history
	}

	sanitized := make([]providers.Message, 0, len(history))
	for _, msg := range history {
		switch msg.Role {
		case "system":
			// 从历史中删除系统消息
			// BuildMessages 总是构建自己的单个系统消息（静态 + 动态 + 摘要）
			// 额外的系统消息会破坏只接受一个的提供商（Anthropic, Codex）
			logger.DebugCF("agent", "Dropping system message from history", map[string]any{})
			continue

		case "tool":
			// 检查是否有对应的 assistant tool_calls 消息
			if len(sanitized) == 0 {
				logger.DebugCF("agent", "Dropping orphaned leading tool message", map[string]any{})
				continue
			}
			// 向后遍历找到最近的 assistant 消息
			// 跳过任何前面的 tool 消息（多工具调用情况）
			foundAssistant := false
			for i := len(sanitized) - 1; i >= 0; i-- {
				if sanitized[i].Role == "tool" {
					continue
				}
				if sanitized[i].Role == "assistant" && len(sanitized[i].ToolCalls) > 0 {
					foundAssistant = true
				}
				break
			}
			if !foundAssistant {
				logger.DebugCF("agent", "Dropping orphaned tool message", map[string]any{})
				continue
			}
			sanitized = append(sanitized, msg)

		case "assistant":
			// 如果有助理消息带 tool_calls，检查是否有有效的前驱
			if len(msg.ToolCalls) > 0 {
				if len(sanitized) == 0 {
					logger.DebugCF("agent", "Dropping assistant tool-call turn at history start", map[string]any{})
					continue
				}
				prev := sanitized[len(sanitized)-1]
				if prev.Role != "user" && prev.Role != "tool" {
					logger.DebugCF(
						"agent",
						"Dropping assistant tool-call turn with invalid predecessor",
						map[string]any{"prev_role": prev.Role},
					)
					continue
				}
			}
			sanitized = append(sanitized, msg)

		default:
			sanitized = append(sanitized, msg)
		}
	}

	// 第二次遍历：确保每个带 tool_calls 的助理消息都有匹配的 tool 结果消息
	// 这是严格提供商（如 DeepSeek）的要求：
	// "An assistant message with 'tool_calls' must be followed by tool messages responding to each 'tool_call_id'."
	final := make([]providers.Message, 0, len(sanitized))
	for i := 0; i < len(sanitized); i++ {
		msg := sanitized[i]
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			// 收集期望的 tool_call IDs
			expected := make(map[string]bool, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				expected[tc.ID] = false
			}

			// 检查后续消息是否有匹配的 tool 结果
			toolMsgCount := 0
			for j := i + 1; j < len(sanitized); j++ {
				if sanitized[j].Role != "tool" {
					break
				}
				toolMsgCount++
				if _, exists := expected[sanitized[j].ToolCallID]; exists {
					expected[sanitized[j].ToolCallID] = true
				}
			}

			// 如果任何 tool_call_id 缺失，删除这个助理消息和其部分 tool 消息
			allFound := true
			for toolCallID, found := range expected {
				if !found {
					allFound = false
					logger.DebugCF(
						"agent",
						"Dropping assistant message with incomplete tool results",
						map[string]any{
							"missing_tool_call_id": toolCallID,
							"expected_count":       len(expected),
							"found_count":          toolMsgCount,
						},
					)
					break
				}
			}

			if !allFound {
				// 跳过这个助理消息和其 tool 消息
				i += toolMsgCount
				continue
			}
		}
		final = append(final, msg)
	}

	return final
}

// AddToolResult 添加工具执行结果到消息列表
// 用于将工具调用的结果添加回对话上下文
//
// 参数：
// - messages: 当前消息列表
// - toolCallID: 工具调用 ID（用于关联 assistant 的 tool_call）
// - toolName: 工具名称
// - result: 工具执行结果
//
// 返回：
// - 添加了工具结果的新消息列表
func (cb *ContextBuilder) AddToolResult(
	messages []providers.Message,
	toolCallID, toolName, result string,
) []providers.Message {
	messages = append(messages, providers.Message{
		Role:       "tool",
		Content:    result,
		ToolCallID: toolCallID,
	})
	return messages
}

// AddAssistantMessage 添加助理消息到消息列表
// 用于将助理的响应（可能包含工具调用）添加到对话上下文
//
// 参数：
// - messages: 当前消息列表
// - content: 助理响应内容
// - toolCalls: 工具调用列表（可选）
//
// 返回：
// - 添加了助理消息的新消息列表
func (cb *ContextBuilder) AddAssistantMessage(
	messages []providers.Message,
	content string,
	toolCalls []map[string]any,
) []providers.Message {
	msg := providers.Message{
		Role:    "assistant",
		Content: content,
	}
	// 总是添加助理消息，无论是否有工具调用
	messages = append(messages, msg)
	return messages
}

// GetSkillsInfo 返回已加载技能的信息
// 用于调试和状态查询
//
// 返回：
// - 包含技能总数、可用数量和名称列表的映射
func (cb *ContextBuilder) GetSkillsInfo() map[string]any {
	allSkills := cb.skillsLoader.ListSkills()
	skillNames := make([]string, 0, len(allSkills))
	for _, s := range allSkills {
		skillNames = append(skillNames, s.Name)
	}
	return map[string]any{
		"total":     len(allSkills),
		"available": len(allSkills),
		"names":     skillNames,
	}
}
