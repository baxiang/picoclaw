package commands

import (
	"fmt"
	"strings"
)

// SubCommand 定义父命令内的单个子命令
type SubCommand struct {
	Name        string   // 子命令名称
	Description string   // 子命令描述
	ArgsUsage   string   // 可选的参数用法说明，例如 "<session-id>"
	Handler     Handler  // 子命令处理器
}

// Definition 是斜杠命令的单一事实来源元数据和行为契约
//
// 设计说明（阶段 1）：
//   - 每个渠道都从此类型读取命令形状，而不是维护本地副本
//   - 可见性是全局的：所有定义都被认为对所有渠道可用
//   - 平台菜单注册（例如 Telegram BotCommand）也从这个相同的定义派生
//     这样 UI 标签和运行时行为保持一致
type Definition struct {
	Name        string       // 命令名称
	Description string       // 命令描述
	Usage       string       // 用法说明（用于简单命令；当 SubCommands 设置时被忽略）
	Aliases     []string     // 命令别名
	SubCommands []SubCommand // 可选的子命令列表；设置后，Executor 路由到子命令处理器
	Handler     Handler      // 用于没有子命令的简单命令
}

// EffectiveUsage 返回用法字符串
// 当存在子命令时，它会根据子命令名称自动生成
// 这样元数据和行为就不会偏离
//
// 返回：
// - 格式化的用法字符串，例如 "/command [sub1|sub2]"
func (d Definition) EffectiveUsage() string {
	if len(d.SubCommands) == 0 {
		return d.Usage
	}
	names := make([]string, 0, len(d.SubCommands))
	for _, sc := range d.SubCommands {
		name := sc.Name
		if sc.ArgsUsage != "" {
			name += " " + sc.ArgsUsage
		}
		names = append(names, name)
	}
	return fmt.Sprintf("/%s [%s]", d.Name, strings.Join(names, "|"))
}
