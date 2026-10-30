//go:build !windows

package cli

import (
	"os"
	"os/exec"
	"syscall"
)

// detach makes the daemon outlive the CLI: its own session, so that closing the terminal does not stop it.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// terminate asks the process to shut down gracefully.
func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
