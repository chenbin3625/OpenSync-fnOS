package native

import (
	"errors"
	"os/exec"
	"syscall"
)

func setParentDeathSignal(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

func pauseProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func resumeProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGCONT); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
