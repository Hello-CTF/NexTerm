//go:build linux || darwin

package diagcapture

import (
	"runtime"
	"syscall"
)

func readProcessUsage() processUsage {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return processUsage{}
	}
	maxRSS := int64(ru.Maxrss)
	if runtime.GOOS != "darwin" {
		maxRSS *= 1024 // Linux reports kilobytes, Darwin bytes
	}
	return processUsage{
		UserSec:     float64(ru.Utime.Sec) + float64(ru.Utime.Usec)/1e6,
		SystemSec:   float64(ru.Stime.Sec) + float64(ru.Stime.Usec)/1e6,
		MaxRSSBytes: maxRSS,
	}
}
