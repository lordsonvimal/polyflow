//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// detachForRestart is the Windows counterpart to the unix version: it puts
// cmd in a new process group (CREATE_NEW_PROCESS_GROUP) so it isn't torn
// down alongside the parent when the parent's console/job is closed.
func detachForRestart(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
