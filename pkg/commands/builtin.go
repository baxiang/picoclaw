package commands

// BuiltinDefinitions 返回所有内置命令定义
// 每个命令组都在各自的 cmd_*.go 文件中定义
// 定义是无状态的——运行时依赖通过执行时传入的 Runtime 参数提供
//
// 返回：
// - 内置命令定义列表
func BuiltinDefinitions() []Definition {
	return []Definition{
		startCommand(),
		helpCommand(),
		showCommand(),
		listCommand(),
		switchCommand(),
		checkCommand(),
	}
}
