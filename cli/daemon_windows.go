//go:build windows

package cli

import (
	"os"
	"os/exec"
	"syscall"
)

// detach makes the daemon outlive the CLI.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000200 | 0x00000008} // CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS
}

// terminate stops the process (there are no signals to ask it to shut down).
func terminate(p *os.Process) error { return p.Kill() }
