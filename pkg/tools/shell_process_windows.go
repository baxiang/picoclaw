//go:build windows

// Package tools 提供 AI 工具的实现
// 本文件实现 Windows 平台的 Shell 进程管理
package tools

import (
	"os/exec"
	"strconv"
)

// prepareCommandForTermination 为命令终止做准备（Windows 无操作）
func prepareCommandForTermination(cmd *exec.Cmd) {
	// no-op on Windows
}

func terminateProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	pid := cmd.Process.Pid
	if pid <= 0 {
		return nil
	}

	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
	_ = cmd.Process.Kill()
	return nil
}
