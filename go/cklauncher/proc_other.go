//go:build !windows

package cklauncher

import "os/exec"

func setHideWindow(_ *exec.Cmd) {}
