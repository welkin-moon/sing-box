//go:build windows

package core

import (
	"os/exec"
	"syscall"
)

func HideProcessWindow(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
