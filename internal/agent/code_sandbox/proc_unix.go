//go:build unix

package code_sandbox

import (
	"os/exec"
	"syscall"
)

// ownGroup starts the child in a process group of its own, so that it and its workers can be killed together.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}

// killGroup kills the child's process group.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
