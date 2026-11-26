//go:build !linux

package native

import "os/exec"

func setParentDeathSignal(cmd *exec.Cmd) {}

func pauseProcess(int) error  { return nil }
func resumeProcess(int) error { return nil }
