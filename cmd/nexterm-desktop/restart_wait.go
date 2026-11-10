package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// restartWaitFlag carries the PID of the instance that relaunched the app after
// an update. The new process waits for it to exit before claiming the
// single-instance lock; otherwise Wails forwards the launch to the exiting
// instance and quits, leaving no window after the update.
const restartWaitFlag = "--restart-wait-pid"

const restartWaitTimeout = 15 * time.Second

// restartArgs returns the relaunch arguments that make the new process wait for
// the process with the given PID.
func restartArgs(args []string, pid int) []string {
	next := make([]string, 0, len(args)+1)
	next = append(next, args...)
	return append(next, restartWaitFlag+"="+strconv.Itoa(pid))
}

// stripRestartWaitPID removes every restart-wait flag from args and returns the
// last PID it names, or 0 when none is present.
func stripRestartWaitPID(args []string) ([]string, int, error) {
	cleaned := make([]string, 0, len(args))
	pid := 0
	for index := 0; index < len(args); index++ {
		argument := args[index]
		var value string
		switch {
		case argument == restartWaitFlag:
			index++
			if index >= len(args) {
				return nil, 0, fmt.Errorf("option %s requires a value", restartWaitFlag)
			}
			value = args[index]
		case strings.HasPrefix(argument, restartWaitFlag+"="):
			value = strings.TrimPrefix(argument, restartWaitFlag+"=")
		default:
			cleaned = append(cleaned, argument)
			continue
		}
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return nil, 0, fmt.Errorf("invalid %s value %q", restartWaitFlag, value)
		}
		pid = parsed
	}
	return cleaned, pid, nil
}
