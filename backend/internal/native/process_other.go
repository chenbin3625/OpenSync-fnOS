//go:build !linux

package native

import "os/exec"

func setParentDeathSignal(cmd *exec.Cmd) {}
