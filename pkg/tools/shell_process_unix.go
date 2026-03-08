//go:build !windows

// Package tools 提供 AI 工具的实现
// 本文件实现 Unix 平台的 Shell 进程管理
package tools

import (
	"os/exec"
	"syscall"
)

// prepareCommandForTermination 为命令终止做准备
// 设置进程组标志，以便可以终止整个进程树
func prepareCommandForTermination(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	pid := cmd.Process.Pid
	if pid <= 0 {
		return nil
	}

	// Kill the entire process group spawned by the shell command.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	// Fallback kill on the shell process itself.
	_ = cmd.Process.Kill()
	return nil
}
