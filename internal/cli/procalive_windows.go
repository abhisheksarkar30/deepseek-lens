//go:build windows

package cli

import (
	"syscall"
	"unsafe"
)

// isProcessAlive reports dead only for the definite no-such-process result
// (ERROR_INVALID_PARAMETER). Access denied and every other error are alive.
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	kernel := syscall.NewLazyDLL("kernel32.dll")
	openProcess := kernel.NewProc("OpenProcess")
	getExit := kernel.NewProc("GetExitCodeProcess")
	closeHandle := kernel.NewProc("CloseHandle")
	const processQueryLimited = 0x1000
	h, _, err := openProcess.Call(uintptr(processQueryLimited), 0, uintptr(pid))
	if h == 0 {
		if errno, ok := err.(syscall.Errno); ok && errno == 87 { // ERROR_INVALID_PARAMETER
			return false
		}
		return true
	}
	defer closeHandle.Call(h)
	var code uint32
	r, _, _ := getExit.Call(h, uintptr(unsafe.Pointer(&code)))
	if r == 0 {
		return true
	}
	const stillActive = 259
	return code == stillActive
}
