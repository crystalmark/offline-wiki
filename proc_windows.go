//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// Hide the console windows of the helper processes.
func setProcAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
