//go:build linux

package pty

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func readUnixProcessIdentity(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", fmt.Errorf("read process start identity for %d: %w", pid, err)
	}
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return "", fmt.Errorf("parse process start identity for %d", pid)
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) <= 19 {
		return "", fmt.Errorf("parse process start identity for %d", pid)
	}
	started, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return "", fmt.Errorf("parse process start identity for %d: %w", pid, err)
	}
	return strconv.FormatUint(started, 10), nil
}
