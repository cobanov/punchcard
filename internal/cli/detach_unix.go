//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// detach puts the flush in a session of its own.
//
// Without this the child stays in the hook's process group, and an agent that
// tidies up after a hook by signalling the group would kill the sync mid-flight.
// A new session has no controlling terminal and no group to be caught by.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
