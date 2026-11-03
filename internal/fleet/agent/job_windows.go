//go:build windows

package agent

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var procIsProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

// currentProcessInJob reports whether this process belongs to any job object.
func currentProcessInJob() (bool, error) {
	self, err := windows.GetCurrentProcess()
	if err != nil {
		return false, err
	}
	var result int32
	r, _, callErr := procIsProcessInJob.Call(uintptr(self), 0, uintptr(unsafe.Pointer(&result)))
	if r == 0 {
		return false, callErr
	}
	return result != 0, nil
}
