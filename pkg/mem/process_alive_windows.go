//go:build windows

package mem

import "golang.org/x/sys/windows"

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err == nil {
		_ = windows.CloseHandle(handle)
		return true
	}
	// Access denied means that the process exists but cannot be inspected by
	// this account. Treat it as alive so another mem process never steals its run.
	return err == windows.ERROR_ACCESS_DENIED
}
