//go:build windows

package cli

import (
	"os/exec"
	"syscall"
)

// detach is the Windows spelling of the same idea: the child leaves the
// parent's console and its process group, so nothing cleaning up after the hook
// takes the flush down with it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008, // DETACHED_PROCESS
	}
}
