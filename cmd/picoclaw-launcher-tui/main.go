// Package main PicoClaw Launcher TUI
// 提供基于终端用户界面（TUI）的 PicoClaw 启动器
package main

import (
	"fmt"
	"os"

	"github.com/sipeed/picoclaw/cmd/picoclaw-launcher-tui/internal/ui"
)

// main PicoClaw Launcher TUI 主函数
// 启动 TUI 界面并处理错误
func main() {
	if err := ui.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
