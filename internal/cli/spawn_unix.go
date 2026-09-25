//go:build !windows

package cli

import (
	"os"
	"os/exec"
	"syscall"
)

func detachCommand(exe string, args []string, cwd, logPath string) (*exec.Cmd, error) {
	cmd := exec.Command(exe, args...)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if logPath != "" {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		cmd.Stdout = f
		cmd.Stderr = f
	}
	return cmd, nil
}
