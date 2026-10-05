//go:build windows

package server

import "golang.org/x/sys/windows"

func diskUsageRate(path string) (float64, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free, total uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &free, &total, nil); err != nil {
		return 0, err
	}
	if total == 0 {
		return 0, nil
	}
	return float64(total-free) / float64(total), nil
}
