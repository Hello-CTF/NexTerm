//go:build windows

package main

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

// waitForProcessExit reports whether the process with the given PID exited
// within timeout.
func waitForProcessExit(pid int, timeout time.Duration) bool {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// OpenProcess rejects a PID that no longer names a process.
		return errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer windows.CloseHandle(handle)
	event, err := windows.WaitForSingleObject(handle, uint32(timeout.Milliseconds()))
	return err == nil && event == windows.WAIT_OBJECT_0
}
