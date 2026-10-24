//go:build linux

package diagcapture

import (
	"bytes"
	"os"
	"strconv"
)

func currentRSSBytes() int64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := bytes.Fields(data)
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseInt(string(fields[1]), 10, 64)
	if err != nil || pages < 0 {
		return 0
	}
	return pages * int64(os.Getpagesize())
}

func openFDCount() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0
	}
	return len(entries)
}
