//go:build !windows

package cli

import "syscall"

// isProcessAlive reports dead only on ESRCH. EPERM and every other error are alive.
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	if err == syscall.ESRCH {
		return false
	}
	return true
}
