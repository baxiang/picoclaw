// Package channels 提供通讯渠道功能
// 本文件包含渠道注册表实现

package channels

import (
	"sync"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

// ChannelFactory 渠道工厂函数类型
// 从配置和消息总线创建渠道实例
// 每个渠道子包通过 init() 注册一个或多个工厂
type ChannelFactory func(cfg *config.Config, bus *bus.MessageBus) (Channel, error)

var (
	factoriesMu sync.RWMutex        // 保护工厂映射的读写锁
	factories   = map[string]ChannelFactory{} // 渠道工厂注册表
)

// RegisterFactory 注册渠道工厂
// 由子包的 init() 函数调用
//
// 参数：
// - name: 渠道名称
// - f: 工厂函数
func RegisterFactory(name string, f ChannelFactory) {
	factoriesMu.Lock()
	defer factoriesMu.Unlock()
	factories[name] = f
}

// getFactory 根据名称查找渠道工厂
//
// 参数：
// - name: 渠道名称
//
// 返回：
// - ChannelFactory: 工厂函数
// - bool: 是否找到
func getFactory(name string) (ChannelFactory, bool) {
	factoriesMu.RLock()
	defer factoriesMu.RUnlock()
	f, ok := factories[name]
	return f, ok
}
