// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

// Package config 提供配置管理功能
// 本文件包含配置迁移逻辑，用于将旧版 ProvidersConfig 转换为新的 ModelList 格式

package config

import (
	"slices"
	"strings"
)

// buildModelWithProtocol 构建带协议前缀的模型字符串
// 如果模型已包含 "/"（表示已有协议前缀），则原样返回
// 否则添加协议前缀
//
// 参数：
// - protocol: 协议名称（如 "openai"、"anthropic"）
// - model: 模型标识符（如 "gpt-4"、"claude-sonnet-4.6"）
//
// 返回：
// - string: 带协议前缀的模型字符串（如 "openai/gpt-4"）
func buildModelWithProtocol(protocol, model string) string {
	if strings.Contains(model, "/") {
		// 模型已有协议前缀，原样返回
		return model
	}
	return protocol + "/" + model
}

// providerMigrationConfig 提供商迁移配置
// 定义如何将旧版 ProviderConfig 转换为新的 ModelConfig 格式
type providerMigrationConfig struct {
	// providerNames 是在 agents.defaults.provider 中可能使用的名称列表
	providerNames []string
	// protocol 是模型字段的协议前缀
	protocol string
	// buildConfig 从 ProviderConfig 创建 ModelConfig 的函数
	buildConfig func(p ProvidersConfig) (ModelConfig, bool)
}

// ConvertProvidersToModelList 将旧版 ProvidersConfig 转换为 ModelConfig 切片
// 用于向后兼容旧配置
// 它会尽可能保留用户在 agents.defaults.model 中配置的模型
//
// 参数：
// - cfg: 原始配置指针
//
// 返回：
// - []ModelConfig: 转换后的模型配置列表
func ConvertProvidersToModelList(cfg *Config) []ModelConfig {
	if cfg == nil {
		return nil
	}

	// 获取用户配置的提供商和模型
	userProvider := strings.ToLower(cfg.Agents.Defaults.Provider)
	userModel := cfg.Agents.Defaults.GetModelName()

	p := cfg.Providers

	var result []ModelConfig

	// 跟踪是否已应用旧版模型名称修复（仅用于第一个提供商）
	legacyModelNameApplied := false

	// 为每个提供商定义迁移规则
	migrations := []providerMigrationConfig{
		{
			providerNames: []string{"openai", "gpt"},
			protocol:      "openai",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.OpenAI.APIKey == "" && p.OpenAI.APIBase == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:      "openai",
					Model:          "openai/gpt-5.2",
					APIKey:         p.OpenAI.APIKey,
					APIBase:        p.OpenAI.APIBase,
					Proxy:          p.OpenAI.Proxy,
					RequestTimeout: p.OpenAI.RequestTimeout,
					AuthMethod:     p.OpenAI.AuthMethod,
				}, true
			},
		},
		{
			providerNames: []string{"anthropic", "claude"},
			protocol:      "anthropic",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.Anthropic.APIKey == "" && p.Anthropic.APIBase == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:      "anthropic",
					Model:          "anthropic/claude-sonnet-4.6",
					APIKey:         p.Anthropic.APIKey,
					APIBase:        p.Anthropic.APIBase,
					Proxy:          p.Anthropic.Proxy,
					RequestTimeout: p.Anthropic.RequestTimeout,
					AuthMethod:     p.Anthropic.AuthMethod,
				}, true
			},
		},
		{
			providerNames: []string{"litellm"},
			protocol:      "litellm",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.LiteLLM.APIKey == "" && p.LiteLLM.APIBase == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:      "litellm",
					Model:          "litellm/auto",
					APIKey:         p.LiteLLM.APIKey,
					APIBase:        p.LiteLLM.APIBase,
					Proxy:          p.LiteLLM.Proxy,
					RequestTimeout: p.LiteLLM.RequestTimeout,
				}, true
			},
		},
		{
			providerNames: []string{"openrouter"},
			protocol:      "openrouter",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.OpenRouter.APIKey == "" && p.OpenRouter.APIBase == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:      "openrouter",
					Model:          "openrouter/auto",
					APIKey:         p.OpenRouter.APIKey,
					APIBase:        p.OpenRouter.APIBase,
					Proxy:          p.OpenRouter.Proxy,
					RequestTimeout: p.OpenRouter.RequestTimeout,
				}, true
			},
		},
		{
			providerNames: []string{"groq"},
			protocol:      "groq",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.Groq.APIKey == "" && p.Groq.APIBase == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:      "groq",
					Model:          "groq/llama-3.1-70b-versatile",
					APIKey:         p.Groq.APIKey,
					APIBase:        p.Groq.APIBase,
					Proxy:          p.Groq.Proxy,
					RequestTimeout: p.Groq.RequestTimeout,
				}, true
			},
		},
		{
			providerNames: []string{"zhipu", "glm"},
			protocol:      "zhipu",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.Zhipu.APIKey == "" && p.Zhipu.APIBase == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:      "zhipu",
					Model:          "zhipu/glm-4",
					APIKey:         p.Zhipu.APIKey,
					APIBase:        p.Zhipu.APIBase,
					Proxy:          p.Zhipu.Proxy,
					RequestTimeout: p.Zhipu.RequestTimeout,
				}, true
			},
		},
		{
			providerNames: []string{"vllm"},
			protocol:      "vllm",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.VLLM.APIKey == "" && p.VLLM.APIBase == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:      "vllm",
					Model:          "vllm/auto",
					APIKey:         p.VLLM.APIKey,
					APIBase:        p.VLLM.APIBase,
					Proxy:          p.VLLM.Proxy,
					RequestTimeout: p.VLLM.RequestTimeout,
				}, true
			},
		},
		{
			providerNames: []string{"gemini", "google"},
			protocol:      "gemini",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.Gemini.APIKey == "" && p.Gemini.APIBase == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:      "gemini",
					Model:          "gemini/gemini-pro",
					APIKey:         p.Gemini.APIKey,
					APIBase:        p.Gemini.APIBase,
					Proxy:          p.Gemini.Proxy,
					RequestTimeout: p.Gemini.RequestTimeout,
				}, true
			},
		},
		{
			providerNames: []string{"nvidia"},
			protocol:      "nvidia",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.Nvidia.APIKey == "" && p.Nvidia.APIBase == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:      "nvidia",
					Model:          "nvidia/meta/llama-3.1-8b-instruct",
					APIKey:         p.Nvidia.APIKey,
					APIBase:        p.Nvidia.APIBase,
					Proxy:          p.Nvidia.Proxy,
					RequestTimeout: p.Nvidia.RequestTimeout,
				}, true
			},
		},
		{
			providerNames: []string{"ollama"},
			protocol:      "ollama",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.Ollama.APIKey == "" && p.Ollama.APIBase == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:      "ollama",
					Model:          "ollama/llama3",
					APIKey:         p.Ollama.APIKey,
					APIBase:        p.Ollama.APIBase,
					Proxy:          p.Ollama.Proxy,
					RequestTimeout: p.Ollama.RequestTimeout,
				}, true
			},
		},
		{
			providerNames: []string{"moonshot", "kimi"},
			protocol:      "moonshot",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.Moonshot.APIKey == "" && p.Moonshot.APIBase == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:      "moonshot",
					Model:          "moonshot/kimi",
					APIKey:         p.Moonshot.APIKey,
					APIBase:        p.Moonshot.APIBase,
					Proxy:          p.Moonshot.Proxy,
					RequestTimeout: p.Moonshot.RequestTimeout,
				}, true
			},
		},
		{
			providerNames: []string{"shengsuanyun"},
			protocol:      "shengsuanyun",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.ShengSuanYun.APIKey == "" && p.ShengSuanYun.APIBase == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:      "shengsuanyun",
					Model:          "shengsuanyun/auto",
					APIKey:         p.ShengSuanYun.APIKey,
					APIBase:        p.ShengSuanYun.APIBase,
					Proxy:          p.ShengSuanYun.Proxy,
					RequestTimeout: p.ShengSuanYun.RequestTimeout,
				}, true
			},
		},
		{
			providerNames: []string{"deepseek"},
			protocol:      "deepseek",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.DeepSeek.APIKey == "" && p.DeepSeek.APIBase == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:      "deepseek",
					Model:          "deepseek/deepseek-chat",
					APIKey:         p.DeepSeek.APIKey,
					APIBase:        p.DeepSeek.APIBase,
					Proxy:          p.DeepSeek.Proxy,
					RequestTimeout: p.DeepSeek.RequestTimeout,
				}, true
			},
		},
		{
			providerNames: []string{"cerebras"},
			protocol:      "cerebras",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.Cerebras.APIKey == "" && p.Cerebras.APIBase == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:      "cerebras",
					Model:          "cerebras/llama-3.3-70b",
					APIKey:         p.Cerebras.APIKey,
					APIBase:        p.Cerebras.APIBase,
					Proxy:          p.Cerebras.Proxy,
					RequestTimeout: p.Cerebras.RequestTimeout,
				}, true
			},
		},
		{
			providerNames: []string{"volcengine", "doubao"},
			protocol:      "volcengine",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.VolcEngine.APIKey == "" && p.VolcEngine.APIBase == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:      "volcengine",
					Model:          "volcengine/doubao-pro",
					APIKey:         p.VolcEngine.APIKey,
					APIBase:        p.VolcEngine.APIBase,
					Proxy:          p.VolcEngine.Proxy,
					RequestTimeout: p.VolcEngine.RequestTimeout,
				}, true
			},
		},
		{
			providerNames: []string{"github_copilot", "copilot"},
			protocol:      "github-copilot",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.GitHubCopilot.APIKey == "" && p.GitHubCopilot.APIBase == "" && p.GitHubCopilot.ConnectMode == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:   "github-copilot",
					Model:       "github-copilot/gpt-5.2",
					APIBase:     p.GitHubCopilot.APIBase,
					ConnectMode: p.GitHubCopilot.ConnectMode,
				}, true
			},
		},
		{
			providerNames: []string{"antigravity"},
			protocol:      "antigravity",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.Antigravity.APIKey == "" && p.Antigravity.AuthMethod == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:  "antigravity",
					Model:      "antigravity/gemini-2.0-flash",
					APIKey:     p.Antigravity.APIKey,
					AuthMethod: p.Antigravity.AuthMethod,
				}, true
			},
		},
		{
			providerNames: []string{"qwen", "tongyi"},
			protocol:      "qwen",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.Qwen.APIKey == "" && p.Qwen.APIBase == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:      "qwen",
					Model:          "qwen/qwen-max",
					APIKey:         p.Qwen.APIKey,
					APIBase:        p.Qwen.APIBase,
					Proxy:          p.Qwen.Proxy,
					RequestTimeout: p.Qwen.RequestTimeout,
				}, true
			},
		},
		{
			providerNames: []string{"mistral"},
			protocol:      "mistral",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.Mistral.APIKey == "" && p.Mistral.APIBase == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:      "mistral",
					Model:          "mistral/mistral-small-latest",
					APIKey:         p.Mistral.APIKey,
					APIBase:        p.Mistral.APIBase,
					Proxy:          p.Mistral.Proxy,
					RequestTimeout: p.Mistral.RequestTimeout,
				}, true
			},
		},
		{
			providerNames: []string{"avian"},
			protocol:      "avian",
			buildConfig: func(p ProvidersConfig) (ModelConfig, bool) {
				if p.Avian.APIKey == "" && p.Avian.APIBase == "" {
					return ModelConfig{}, false
				}
				return ModelConfig{
					ModelName:      "avian",
					Model:          "avian/deepseek/deepseek-v3.2",
					APIKey:         p.Avian.APIKey,
					APIBase:        p.Avian.APIBase,
					Proxy:          p.Avian.Proxy,
					RequestTimeout: p.Avian.RequestTimeout,
				}, true
			},
		},
	}

	// 处理每个提供商迁移
	for _, m := range migrations {
		mc, ok := m.buildConfig(p)
		if !ok {
			continue
		}

		// 检查这是否为用户配置的提供商
		if slices.Contains(m.providerNames, userProvider) && userModel != "" {
			// 使用户配置的模型而不是默认模型
			mc.Model = buildModelWithProtocol(m.protocol, userModel)
		} else if userProvider == "" && userModel != "" && !legacyModelNameApplied {
			// 旧版配置：没有明确的 provider 字段但指定了 model
			// 为第一个提供商使用 userModel 作为 ModelName，这样 GetModelConfig(model) 可以找到它
			// 这保持了与依赖隐式提供商选择的旧配置的向后兼容性
			mc.ModelName = userModel
			mc.Model = buildModelWithProtocol(m.protocol, userModel)
			legacyModelNameApplied = true
		}

		result = append(result, mc)
	}

	return result
}
