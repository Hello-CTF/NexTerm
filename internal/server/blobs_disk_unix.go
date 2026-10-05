//go:build unix

package server

import "syscall"

func diskUsageRate(path string) (float64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Blocks == 0 {
		return 0, nil
	}
	return float64(stat.Blocks-stat.Bfree) / float64(stat.Blocks), nil
}
