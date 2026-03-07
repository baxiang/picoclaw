# Tools 和 Skills 技术文档

> 文档版本：1.1  
> 最后更新：2026-03-07  
> 作者：PicoClaw Team

---

## 目录

- [概述](#概述)
- [核心区别](#核心区别)
- [Tools 详解](#tools-详解)
- [Skills 详解](#skills-详解)
- [与大模型的协作流程](#与大模型的协作流程)
- [System Prompt 数据格式](#system-prompt-数据格式)
- [扩展指南](#扩展指南)
- [最佳实践](#最佳实践)

---

## 概述

PicoClaw 使用 **Tools（工具）** 和 **Skills（技能）** 两种机制来扩展 AI 助手的能力。这种双层设计实现了**稳定性**与**灵活性**的平衡：

- **Tools** = 原子能力，由 Go 代码实现，稳定可靠
- **Skills** = 组合能力，由 Markdown 文档定义，灵活可扩展

这种设计是 PicoClaw "AI 自举"（AI-Bootstrapped）理念的核心实现。

---

## 核心区别

| 维度 | **Tools（工具）** | **Skills（技能）** |
|------|------------------|-------------------|
| **定义位置** | Go 代码 (`pkg/tools/`) | Markdown 文件 (`workspace/skills/`) |
| **注册方式** | 编译时注册到 `ToolRegistry` | 运行时从文件系统加载 |
| **调用方式** | LLM 直接通过 `function_call` 调用 | AI 通过 `read_file` 工具读取 `SKILL.md` 学习后使用 |
| **扩展方式** | 需要修改代码并重新编译 | 只需添加/修改 `SKILL.md` 文件 |
| **执行主体** | 代码直接执行 | AI 阅读技能文档后，使用基础工具组合实现 |
| **示例** | `read_file`, `exec`, `web_search` | `weather`, `github`, `summarize` |

---

## Tools 详解

### 定义位置

Tools 在 `pkg/tools/` 目录下定义，每个工具实现 `Tool` 接口：

```
pkg/tools/
├── filesystem.go      # read_file, write_file, edit_file, list_dir
├── shell.go           # exec (Shell 命令执行)
├── web.go             # web_search, web_fetch
├── message.go         # message (发送消息)
├── send_file.go       # send_file (发送媒体文件)
├── spawn.go           # spawn (创建子代理)
├── skills_search.go   # find_skills (搜索技能)
├── skills_install.go  # install_skill (安装技能)
├── i2c.go             # i2c (I2C 总线操作)
├── spi.go             # spi (SPI 总线操作)
└── mcp_tool.go        # MCP 工具 (动态加载)
```

### 工具接口

```go
// pkg/tools/types.go
type Tool interface {
    // Name 返回工具名称（LLM 调用时使用）
    Name() string
    
    // Description 返回工具描述（用于向 LLM 解释功能）
    Description() string
    
    // Parameters 返回 JSON Schema 格式的参数定义
    Parameters() map[string]any
    
    // Execute 执行工具
    Execute(ctx context.Context, args map[string]any, channel, chatID string) *ToolResult
}
```

### 工具注册

在 `pkg/agent/loop.go` 的 `registerSharedTools()` 函数中注册：

```go
// pkg/agent/loop.go:registerSharedTools()
func registerSharedTools(cfg *config.Config, msgBus *bus.MessageBus, registry *AgentRegistry, provider providers.LLMProvider) {
    for _, agentID := range registry.ListAgentIDs() {
        agent, ok := registry.GetAgent(agentID)
        if !ok {
            continue
        }

        // --- Web 搜索工具 ---
        if cfg.Tools.IsToolEnabled("web") {
            searchTool, err := tools.NewWebSearchTool(tools.WebSearchToolOptions{
                BraveAPIKey:          cfg.Tools.Web.Brave.APIKey,
                DuckDuckGoEnabled:    cfg.Tools.Web.DuckDuckGo.Enabled,
                // ... 其他配置
            })
            if err != nil {
                logger.ErrorCF("agent", "Failed to create web search tool", map[string]any{"error": err.Error()})
            } else if searchTool != nil {
                agent.Tools.Register(searchTool)
            }
        }

        // --- 文件操作工具 ---
        // 在 pkg/agent/instance.go 中注册
        if cfg.Tools.IsToolEnabled("read_file") {
            agent.Tools.Register(tools.NewReadFileTool(workspace, readRestrict, allowReadPaths))
        }

        // --- 技能发现工具 ---
        if cfg.Tools.IsToolEnabled("find_skills") {
            agent.Tools.Register(tools.NewFindSkillsTool(registryMgr, searchCache))
        }
    }
}
```

### 工具调用流程

```
┌─────────────┐    ┌──────────────┐    ┌─────────────┐    ┌──────────────┐
│   用户消息   │ →  │  LLM 处理    │ →  │ Tool Call   │ →  │ Tool.Execute │
│             │    │              │    │             │    │              │
│ "查询天气"   │    │ 返回 tool_calls│    │ web_search  │    │ 执行 HTTP 请求 │
└─────────────┘    └──────────────┘    └─────────────┘    └──────────────┘
                                                                   │
┌─────────────┐    ┌──────────────┐    ┌─────────────┐           │
│  最终响应   │ ←  │  LLM 处理    │ ←  │ ToolResult  │ ←─────────┘
│             │    │              │    │             │
│ "北京 +8°C"  │    │ 生成自然语言  │    │ 天气数据     │
└─────────────┘    └──────────────┘    └─────────────┘
```

---

## Skills 详解

### 定义位置

Skills 在以下目录中定义：

```
~/.picoclaw/
├── workspace/skills/     # 工作空间技能（项目级）
│   ├── weather/
│   │   └── SKILL.md
│   ├── github/
│   │   └── SKILL.md
│   └── summarize/
│       └── SKILL.md
├── skills/               # 全局技能（用户级）
│   └── {skill-name}/
│       └── SKILL.md
└── config.json
```

项目内置技能在 `skills/` 目录（如果存在）。

### SKILL.md 文件格式

```markdown
---
name: weather
description: Get current weather and forecasts (no API key required).
homepage: https://wttr.in/:help
metadata: {"nanobot":{"emoji":"🌤️","requires":{"bins":["curl"]}}}
---

# Weather

Two free services, no API keys needed.

## wttr.in (primary)

Quick one-liner:
```bash
curl -s "wttr.in/London?format=3"
# Output: London: ⛅️ +8°C
```

Compact format:
```bash
curl -s "wttr.in/London?format=%l:+%c+%t+%h+%w"
# Output: London: ⛅️ +8°C 71% ↙5km/h
```

Full forecast:
```bash
curl -s "wttr.in/London?T"
```

Format codes: `%c` condition · `%t` temp · `%h` humidity · `%w` wind · `%l` location · `%m` moon

Tips:
- URL-encode spaces: `wttr.in/New+York`
- Airport codes: `wttr.in/JFK`
- Units: `?m` (metric) `?u` (USCS)
- Today only: `?1` · Current only: `?0`
```

### 技能加载

在 `pkg/skills/loader.go` 中实现：

```go
// pkg/skills/loader.go
type SkillsLoader struct {
    workspace       string  // 工作空间根目录
    workspaceSkills string  // 工作空间技能目录
    globalSkills    string  // 全局技能目录 (~/.picoclaw/skills)
    builtinSkills   string  // 内置技能目录
}

// BuildSkillsSummary 构建技能摘要（XML 格式，用于 System Prompt）
func (sl *SkillsLoader) BuildSkillsSummary() string {
    allSkills := sl.ListSkills()
    if len(allSkills) == 0 {
        return ""
    }

    var lines []string
    lines = append(lines, "<skills>")
    for _, s := range allSkills {
        lines = append(lines, fmt.Sprintf("  <skill>"))
        lines = append(lines, fmt.Sprintf("    <name>%s</name>", escapeXML(s.Name)))
        lines = append(lines, fmt.Sprintf("    <description>%s</description>", escapeXML(s.Description)))
        lines = append(lines, fmt.Sprintf("    <location>%s</location>", escapeXML(s.Path)))
        lines = append(lines, fmt.Sprintf("    <source>%s</source>", s.Source))
        lines = append(lines, "  </skill>")
    }
    lines = append(lines, "</skills>")

    return strings.Join(lines, "\n")
}
```

### 技能使用流程

```
┌─────────────────┐
│  System Prompt  │
│                 │
│  # Skills       │
│  <skills>       │ ← AI 看到技能列表
│    <name>       │
│      weather    │
│    </name>      │
│  </skills>      │
└─────────────────┘
         │
         ▼
┌─────────────────┐
│  AI 决策：       │
│  "我需要学习    │
│   weather 技能" │
└─────────────────┘
         │
         ▼
┌─────────────────┐
│  调用 read_file  │
│  工具读取       │
│  SKILL.md       │
└─────────────────┘
         │
         ▼
┌─────────────────┐
│  AI 学习技能内容 │
│  理解如何使用   │
│  curl 查询天气   │
└─────────────────┘
         │
         ▼
┌─────────────────┐
│  调用 exec 工具  │
│  执行 curl 命令  │
└─────────────────┘
         │
         ▼
┌─────────────────┐
│  返回天气数据   │
│  给用户         │
└─────────────────┘
```

---

## 与大模型的协作流程

### 完整数据流

```
┌──────────────────────────────────────────────────────────────────┐
│                        Agent Loop (主循环)                        │
│                                                                  │
│  ┌─────────────┐    ┌──────────────┐    ┌─────────────────┐    │
│  │   用户消息   │ →  │ ContextBuilder│ →  │ System Prompt    │    │
│  └─────────────┘    └──────────────┘    └─────────────────┘    │
│                                              │                   │
│                                              ▼                   │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ System Prompt 包含：                                     │    │
│  │ 1. Identity (身份定义)                                   │    │
│  │ 2. Bootstrap Files (AGENTS.md, etc.)                    │    │
│  │ 3. Skills Summary (技能列表) ← Skills 在这里！           │    │
│  │    <skills>                                              │    │
│  │      <name>weather</name>                               │    │
│  │      <description>Get current weather...</description>  │    │
│  │    </skills>                                             │    │
│  │ 4. Memory Context                                       │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                              │                   │
│                                              ▼                   │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │  Tool Definitions (工具定义) ← Tools 在这里！             │    │
│  │  [                                                      │    │
│  │    {"name": "read_file", "description": "..."},         │    │
│  │    {"name": "exec", "description": "..."},              │    │
│  │    {"name": "web_search", "description": "..."},        │    │
│  │    ...                                                  │    │
│  │  ]                                                      │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                              │                   │
│                                              ▼                   │
│                              ┌───────────────────────┐          │
│                              │      LLM (大模型)      │          │
│                              │                       │          │
│                              │  接收：System Prompt  │          │
│                              │       + Tool Defs     │          │
│                              │                       │          │
│                              │  决策：               │          │
│                              │  1. 直接回答          │          │
│                              │  2. 调用 Tool         │          │
│                              │  3. 读取 Skill 学习   │          │
│                              └───────────────────────┘          │
│                                              │                   │
│                    ┌─────────────────────────┼──────────────┐   │
│                    │                         │              │   │
│                    ▼                         ▼              ▼   │
│            ┌────────────┐           ┌────────────┐  ┌──────────┐│
│            │ Tool 调用   │           │ read_file  │  │ 其他工具 ││
│            │ (直接执行)  │           │ 读取技能   │  │          ││
│            └────────────┘           └────────────┘  └──────────┘│
│                    │                         │                   │
│                    │                         ▼                   │
│                    │              ┌─────────────────┐           │
│                    │              │ AI 学习技能后    │           │
│                    │              │ 使用基础工具    │           │
│                    │              │ 组合实现技能    │           │
│                    │              └─────────────────┘           │
│                    │                         │                   │
│                    └─────────────────────────┼──────────────┐   │
│                                              ▼              │   │
│                                    ┌────────────────────┐   │   │
│                                    │   ToolResult       │   │   │
│                                    │   (执行结果)       │   │   │
│                                    └────────────────────┘   │   │
│                                              │               │   │
│                                              ▼               │   │
│                                    ┌────────────────────┐   │   │
│                                    │   返回给 LLM        │   │   │
│                                    │   继续下一轮        │   │   │
│                                    └────────────────────┘   │   │
└──────────────────────────────────────────────────────────────────┘
```

### 代码位置

| 功能 | 代码位置 | 说明 |
|------|----------|------|
| System Prompt 构建 | `pkg/agent/context.go:BuildSystemPrompt()` | 组合 4 个部分 |
| 技能摘要构建 | `pkg/skills/loader.go:BuildSkillsSummary()` | XML 格式技能列表 |
| 工具注册 | `pkg/agent/loop.go:registerSharedTools()` | 注册共享工具 |
| 消息构建 | `pkg/agent/context.go:BuildMessages()` | 添加历史 + 当前消息 |
| LLM 迭代 | `pkg/agent/loop.go:runLLMIteration()` | 调用 LLM + 工具执行 |

---

## System Prompt 数据格式

### 什么是 system_parts？

`system_parts` 是 PicoClaw 为了优化 LLM 调用性能而设计的**分块缓存机制**。它将 System Prompt 分为多个块（ContentBlock），每个块可以独立设置缓存策略。

```go
// pkg/providers/types.go
type ContentBlock struct {
    Type         string         `json:"type"`          // 内容类型："text", "image", etc.
    Text         string         `json:"text"`          // 文本内容
    CacheControl *CacheControl  `json:"cache_control,omitempty"` // 缓存控制
}

type CacheControl struct {
    Type string `json:"type"`  // "ephemeral" - 临时缓存（Anthropic）
}
```

#### 静态部分（缓存）

**静态部分**包含每次对话都相同的内容，使用 `cache_control: {type: "ephemeral"}` 标记，LLM 提供商（如 Anthropic）会缓存这部分的 KV 状态，避免重复计算。

**包含内容：**
1. **Identity（身份定义）** - AI 的基本身份和规则
2. **Bootstrap Files** - AGENTS.md, SOUL.md, USER.md, IDENTITY.md
3. **Skills Summary** - 技能列表（XML 格式）
4. **Memory Context** - MEMORY.md 和最近日记

**代码位置：** `pkg/agent/context.go:BuildSystemPromptWithCache()`

```go
// 静态部分被缓存以避免每次调用时重复的文件 I/O 和字符串构建
staticPrompt := cb.BuildSystemPromptWithCache()

contentBlocks := []providers.ContentBlock{
    {
        Type: "text", 
        Text: staticPrompt, 
        CacheControl: &providers.CacheControl{Type: "ephemeral"},
    },
    // ...
}
```

#### 动态部分（每次变化）

**动态部分**包含每次请求都变化的内容，不设置缓存，每次都会重新计算。

**包含内容：**
1. **当前时间** - 格式化的日期时间
2. **运行时信息** - OS、架构、Go 版本
3. **会话信息** - Channel 和 ChatID
4. **对话摘要**（如果有）- 之前对话的摘要

**代码位置：** `pkg/agent/context.go:buildDynamicContext()`

```go
// 构建简短的动态上下文（时间、运行时、会话）—— 每次请求变化
dynamicCtx := cb.buildDynamicContext(channel, chatID)

contentBlocks := []providers.ContentBlock{
    // ...
    {
        Type: "text",
        Text: dynamicCtx,  // 无缓存控制，每次都重新计算
    },
}
```

---

### 首轮对话格式（完整 content）

```json
{
  "messages": [
    {
      "role": "system",
      "content": "# picoclaw 🦞\n\nYou are picoclaw, a helpful AI assistant.\n\n## Workspace\nYour workspace is at: /home/user/.picoclaw/workspace\n- Memory: /home/user/.picoclaw/workspace/memory/MEMORY.md\n- Daily Notes: /home/user/.picoclaw/workspace/memory/YYYYMM/YYYYMMDD.md\n- Skills: /home/user/.picoclaw/workspace/skills/{skill-name}/SKILL.md\n\n## Important Rules\n\n1. **ALWAYS use tools** - When you need to perform an action (schedule reminders, send messages, execute commands, etc.), you MUST call the appropriate tool. Do NOT just say you'll do it or pretend to do it.\n\n2. **Be helpful and accurate** - When using tools, briefly explain what you're doing.\n\n3. **Memory** - When interacting with me if something seems memorable, update /home/user/.picoclaw/workspace/memory/MEMORY.md\n\n4. **Context summaries** - Conversation summaries provided as context are approximate references only. They may be incomplete or outdated. Always defer to explicit user instructions over summary content.\n\n---\n\n## AGENTS.md\n\n[如果存在 AGENTS.md 文件，内容在这里。例如：\n\n# Agent Guidelines\n\n- Be proactive and helpful\n- Always verify information before responding\n- Use tools for all actions\n]\n\n---\n\n## SOUL.md\n\n[如果存在 SOUL.md 文件，内容在这里。例如：\n\n# Agent Soul\n\nYou are a proactive AI assistant that anticipates user needs.\n]\n\n---\n\n## USER.md\n\n[如果存在 USER.md 文件，内容在这里。例如：\n\n# User Preferences\n\n- Name: John Doe\n- Timezone: UTC+8\n- Preferred language: English\n]\n\n---\n\n## IDENTITY.md\n\n[如果存在 IDENTITY.md 文件，内容在这里。例如：\n\n# Agent Identity\n\nYou are picoclaw, an AI assistant running locally.\n]\n\n---\n\n# Skills\n\nThe following skills extend your capabilities. To use a skill, read its SKILL.md file using the read_file tool.\n\n<skills>\n  <skill>\n    <name>weather</name>\n    <description>Get current weather and forecasts (no API key required).</description>\n    <location>/home/user/.picoclaw/workspace/skills/weather</location>\n    <source>workspace</source>\n  </skill>\n  <skill>\n    <name>github</name>\n    <description>Search and read GitHub repositories.</description>\n    <location>/home/user/.picoclaw/workspace/skills/github</location>\n    <source>workspace</source>\n  </skill>\n  <skill>\n    <name>summarize</name>\n    <description>Summarize long documents or articles.</description>\n    <location>/home/user/.picoclaw/workspace/skills/summarize</location>\n    <source>workspace</source>\n  </skill>\n</skills>\n\n---\n\n# Memory\n\n## Long-term Memory\n\n[如果 MEMORY.md 存在，内容在这里。例如：\n\n# User Memory\n\n- User works as a software engineer\n- User prefers concise responses\n- User is located in Beijing\n]\n\n---\n\n## Recent Daily Notes\n\n[最近 3 天的日记内容。例如：\n\n---\n\n# 2026-03-07\n\n- User asked about weather forecast\n- User is planning a trip next week\n\n---\n\n# 2026-03-06\n\n- User worked on PicoClaw project\n- User fixed several bugs\n]\n\n---\n\n## Current Time\n2026-03-07 15:30 (Saturday)\n\n## Runtime\ndarwin arm64, Go go1.25.7\n\n## Current Session\nChannel: cli\nChat ID: direct",
      "system_parts": [
        {
          "type": "text",
          "text": "# picoclaw 🦞\n\nYou are picoclaw, a helpful AI assistant.\n\n## Workspace\nYour workspace is at: /home/user/.picoclaw/workspace\n- Memory: /home/user/.picoclaw/workspace/memory/MEMORY.md\n- Daily Notes: /home/user/.picoclaw/workspace/memory/YYYYMM/YYYYMMDD.md\n- Skills: /home/user/.picoclaw/workspace/skills/{skill-name}/SKILL.md\n\n## Important Rules\n\n1. **ALWAYS use tools** - ...\n\n---\n\n## AGENTS.md\n[内容]\n\n---\n\n## SOUL.md\n[内容]\n\n---\n\n## USER.md\n[内容]\n\n---\n\n## IDENTITY.md\n[内容]\n\n---\n\n# Skills\n\n<skills>\n  <skill>\n    <name>weather</name>\n    <description>Get current weather and forecasts (no API key required).</description>\n    <location>/home/user/.picoclaw/workspace/skills/weather</location>\n    <source>workspace</source>\n  </skill>\n</skills>\n\n---\n\n# Memory\n\n## Long-term Memory\n[内容]\n\n---\n\n## Recent Daily Notes\n[内容]",
          "cache_control": {"type": "ephemeral"}
        },
        {
          "type": "text",
          "text": "## Current Time\n2026-03-07 15:30 (Saturday)\n\n## Runtime\ndarwin arm64, Go go1.25.7\n\n## Current Session\nChannel: cli\nChat ID: direct"
        }
      ]
    },
    {
      "role": "user",
      "content": "你好，请帮我查询北京的天气"
    }
  ]
}
```

### 第二轮对话格式（完整 content + 摘要）

```json
{
  "messages": [
    {
      "role": "system",
      "content": "# picoclaw 🦞\n\nYou are picoclaw, a helpful AI assistant.\n\n## Workspace\nYour workspace is at: /home/user/.picoclaw/workspace\n- Memory: /home/user/.picoclaw/workspace/memory/MEMORY.md\n- Daily Notes: /home/user/.picoclaw/workspace/memory/YYYYMM/YYYYMMDD.md\n- Skills: /home/user/.picoclaw/workspace/skills/{skill-name}/SKILL.md\n\n## Important Rules\n\n1. **ALWAYS use tools** - ...\n\n---\n\n## AGENTS.md\n[内容]\n\n---\n\n## SOUL.md\n[内容]\n\n---\n\n## USER.md\n[内容]\n\n---\n\n## IDENTITY.md\n[内容]\n\n---\n\n# Skills\n\n<skills>\n  <skill>\n    <name>weather</name>\n    <description>Get current weather and forecasts (no API key required).</description>\n    <location>/home/user/.picoclaw/workspace/skills/weather</location>\n    <source>workspace</source>\n  </skill>\n</skills>\n\n---\n\n# Memory\n\n## Long-term Memory\n[内容]\n\n---\n\n## Recent Daily Notes\n[内容]\n\n---\n\n## Current Time\n2026-03-07 15:32 (Saturday)\n\n## Runtime\ndarwin arm64, Go go1.25.7\n\n## Current Session\nChannel: cli\nChat ID: direct\n\n---\n\nCONTEXT_SUMMARY: The following is an approximate summary of prior conversation for reference only. It may be incomplete or outdated — always defer to explicit instructions.\n\n用户询问了北京的天气，我使用 web_search 工具查询了天气信息，返回北京晴朗，+8°C，湿度 45%。用户现在询问上海的天气。",
      "system_parts": [
        {
          "type": "text",
          "text": "# picoclaw 🦞\n\nYou are picoclaw, a helpful AI assistant.\n\n## Workspace\n...\n\n# Skills\n\n<skills>\n  <skill>\n    <name>weather</name>\n    <description>Get current weather and forecasts (no API key required).</description>\n    <location>/home/user/.picoclaw/workspace/skills/weather</location>\n    <source>workspace</source>\n  </skill>\n</skills>\n\n---\n\n# Memory\n\n## Long-term Memory\n[内容]\n\n---\n\n## Recent Daily Notes\n[内容]",
          "cache_control": {"type": "ephemeral"}
        },
        {
          "type": "text",
          "text": "## Current Time\n2026-03-07 15:32 (Saturday)\n\n## Runtime\ndarwin arm64, Go go1.25.7\n\n## Current Session\nChannel: cli\nChat ID: direct"
        },
        {
          "type": "text",
          "text": "CONTEXT_SUMMARY: The following is an approximate summary of prior conversation for reference only. It may be incomplete or outdated — always defer to explicit instructions.\n\n用户询问了北京的天气，我使用 web_search 工具查询了天气信息，返回北京晴朗，+8°C，湿度 45%。用户现在询问上海的天气。"
        }
      ]
    },
    {"role": "user", "content": "你好，请帮我查询北京的天气"},
    {"role": "assistant", "content": "我来帮你查询北京的天气。", "tool_calls": [{"id": "call_abc123", "type": "function", "function": {"name": "web_search", "arguments": "{\"query\": \"北京 天气\"}"}}]},
    {"role": "tool", "content": "北京：晴，温度：+8°C，湿度：45%", "tool_call_id": "call_abc123"},
    {"role": "assistant", "content": "北京今天天气晴朗，温度 +8°C，湿度 45%。适合户外活动！"},
    {"role": "user", "content": "那上海呢？"}
  ]
}
```

---

## 扩展指南

### 添加新 Tool

1. 在 `pkg/tools/` 创建新文件：

```go
// pkg/tools/weather.go
package tools

import (
    "context"
    "fmt"
    "net/http"
)

type WeatherTool struct {
    // 工具配置
}

func NewWeatherTool() *WeatherTool {
    return &WeatherTool{}
}

func (t *WeatherTool) Name() string {
    return "weather"
}

func (t *WeatherTool) Description() string {
    return "Get current weather for a city"
}

func (t *WeatherTool) Parameters() map[string]any {
    return map[string]any{
        "type": "object",
        "properties": map[string]any{
            "city": map[string]any{
                "type": "string",
                "description": "City name",
            },
        },
        "required": []string{"city"},
    }
}

func (t *WeatherTool) Execute(ctx context.Context, args map[string]any, channel, chatID string) *ToolResult {
    city, _ := args["city"].(string)
    resp, err := http.Get(fmt.Sprintf("https://wttr.in/%s?format=3", city))
    if err != nil {
        return &ToolResult{Err: err}
    }
    // 解析并返回结果
    return &ToolResult{ForLLM: weatherData, ForUser: weatherData}
}
```

2. 在 `pkg/agent/loop.go` 中注册：

```go
if cfg.Tools.IsToolEnabled("weather") {
    agent.Tools.Register(tools.NewWeatherTool())
}
```

### 添加新 Skill

1. 创建技能目录：

```bash
mkdir -p ~/.picoclaw/workspace/skills/my-skill
```

2. 创建 `SKILL.md` 文件：

```markdown
---
name: my-skill
description: My custom skill description.
---

# My Skill

## Usage

```bash
# Example command
curl -s "https://api.example.com/data"
```

## Tips

- Tip 1
- Tip 2
```

3. 重启 PicoClaw，技能自动加载。

---

## 最佳实践

### Tool 设计原则

1. **原子性**：每个工具只做一件事
2. **可组合**：工具之间可以组合使用
3. **错误处理**：返回清晰的错误信息
4. **安全性**：沙箱限制，防止危险操作

### Skill 设计原则

1. **清晰文档**：详细说明使用方法和示例
2. **基础工具**：使用已有的基础工具组合
3. **可学习性**：让 AI 能通过阅读文档学会使用
4. **可测试**：提供测试命令和预期输出

### 选择指南

| 场景 | 推荐方式 | 原因 |
|------|----------|------|
| 需要 API 调用 | Tool | 稳定、高效、可监控 |
| 需要复杂逻辑 | Tool | 代码可实现复杂逻辑 |
| 需要快速原型 | Skill | 无需编译，立即可用 |
| 需要用户自定义 | Skill | 用户可自己编写 |
| 组合多个工具 | Skill | AI 可灵活组合 |
| 硬件操作 | Tool | 需要底层访问 |

---

## 相关文件

- `pkg/tools/types.go` - Tool 接口定义
- `pkg/agent/loop.go` - 工具注册和调用
- `pkg/agent/context.go` - System Prompt 构建
- `pkg/skills/loader.go` - 技能加载器
- `workspace/skills/` - 用户技能目录

---

## 常见问题

### Q: 为什么需要 Skills，直接用 Tools 不行吗？

A: Skills 提供了更大的灵活性：
- 无需编译即可扩展
- 用户可以自己编写技能
- AI 可以学习新技能并灵活使用
- 适合组合多个基础工具的场景

### Q: Skills 的性能比 Tools 差吗？

A: 是的，Skills 需要 AI 先学习再执行，会多一轮 LLM 调用。但对于不需要高性能的场景，Skills 的灵活性更有价值。

### Q: 可以同时使用 Tools 和 Skills 吗？

A: 当然可以！这正是 PicoClaw 的设计理念。基础功能用 Tools，扩展功能用 Skills。

---

*本文档基于 PicoClaw v0.1.x 编写，如有更新请参考最新代码。*
