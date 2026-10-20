//go:build darwin

package terminal

import "syscall"

func nativeProcessCPUTime() (float64, bool) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, false
	}
	return float64(usage.Utime.Nano()+usage.Stime.Nano()) / 1e9, true
}
