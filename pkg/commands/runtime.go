package commands

import "github.com/sipeed/picoclaw/pkg/config"

// Runtime 为命令处理器提供运行时依赖
// 它由 agent 循环按请求构建，因此每个请求的状态（如会话作用域）
// 可以与长生命周期的回调（如 GetModelInfo）共存
type Runtime struct {
	Config             *config.Config                    // 全局配置
	GetModelInfo       func() (name, provider string)    // 获取当前模型信息
	ListAgentIDs       func() []string                   // 列出所有代理 ID
	ListDefinitions    func() []Definition               // 列出所有命令定义
	GetEnabledChannels func() []string                   // 获取启用的渠道列表
	SwitchModel        func(value string) (oldModel string, err error) // 切换模型
	SwitchChannel      func(value string) error          // 切换渠道
}
