//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detachForRestart puts cmd in its own session (setsid) so it survives the
// parent process's exit — see selectWorkspaceFunc for why this matters:
// without it, the child stays in the parent's process group and gets torn
// down along with it in many launch contexts (terminal job control,
// process-group signal delivery), leaving no server listening at all.
func detachForRestart(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
