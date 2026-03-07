// Package commands 提供内置命令系统
// 支持斜杠命令（如 /help、/switch、/show 等）
// 命令定义是全局的，所有渠道共享同一套命令集
package commands

// Registry 命令注册表
// 存储所有注册的命令定义并建立名称到索引的映射
type Registry struct {
	defs  []Definition       // 命令定义列表
	index map[string]int     // 命令名称/别名到索引的映射
}

// NewRegistry 创建命令注册表
// 存储用于分发和可选平台注册的标准命令集
//
// 参数：
// - defs: 命令定义列表
//
// 返回：
// - 初始化好的 Registry 指针
func NewRegistry(defs []Definition) *Registry {
	stored := make([]Definition, len(defs))
	copy(stored, defs)

	index := make(map[string]int, len(stored)*2)
	for i, def := range stored {
		registerCommandName(index, def.Name, i)
		for _, alias := range def.Aliases {
			registerCommandName(index, alias, i)
		}
	}

	return &Registry{defs: stored, index: index}
}

// Definitions 返回所有注册的命令定义
// 命令可用性是全局的，不再按渠道划分
//
// 返回：
// - 命令定义列表的副本
func (r *Registry) Definitions() []Definition {
	out := make([]Definition, len(r.defs))
	copy(out, r.defs)
	return out
}

// Lookup 通过标准化的命令名称或别名查找命令定义
//
// 参数：
// - name: 命令名称或别名
//
// 返回：
// - Definition: 命令定义
// - bool: 是否找到
func (r *Registry) Lookup(name string) (Definition, bool) {
	key := normalizeCommandName(name)
	if key == "" {
		return Definition{}, false
	}
	idx, ok := r.index[key]
	if !ok {
		return Definition{}, false
	}
	return r.defs[idx], true
}

func registerCommandName(index map[string]int, name string, defIndex int) {
	key := normalizeCommandName(name)
	if key == "" {
		return
	}
	if _, exists := index[key]; exists {
		return
	}
	index[key] = defIndex
}
