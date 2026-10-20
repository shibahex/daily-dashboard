package anki

import (
	"os/exec"
	"syscall"
)

// startDetached starts a program in its own session, so it keeps running if the server stops.
func startDetached(program string) error {
	cmd := exec.Command(program)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
