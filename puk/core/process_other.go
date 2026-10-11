//go:build !windows

package core

import "os/exec"

func HideProcessWindow(cmd *exec.Cmd) {}
