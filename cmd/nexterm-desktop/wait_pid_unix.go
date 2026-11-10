//go:build !windows

package main

import (
	"errors"
	"syscall"
	"time"
)

const waitPIDPollInterval = 50 * time.Millisecond

// waitForProcessExit reports whether the process with the given PID exited
// within timeout.
func waitForProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		time.Sleep(min(waitPIDPollInterval, remaining))
	}
}
