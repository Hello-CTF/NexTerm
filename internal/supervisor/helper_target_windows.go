//go:build windows

package supervisor

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// handleTarget holds the helper through a process handle opened while the
// verified pipe connection was open; waits and termination address that
// process even after its pid is reused.
type handleTarget struct {
	handle windows.Handle
}

func openHelperTarget(pid int, _ helperReverify) (helperTarget, error) {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return nil, err
	}
	return &handleTarget{handle: handle}, nil
}

// terminate ends the helper at once: a Windows helper has no graceful stop
// request, and ending it closes its pseudoconsoles, which ends the processes
// attached to them.
func (t *handleTarget) terminate() error {
	if t.exited() {
		return nil
	}
	return windows.TerminateProcess(t.handle, 1)
}

func (t *handleTarget) exited() bool {
	event, err := windows.WaitForSingleObject(t.handle, 0)
	return err == nil && event == windows.WAIT_OBJECT_0
}

func (t *handleTarget) wait(ctx context.Context, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for {
		event, err := windows.WaitForSingleObject(t.handle, 50)
		if err == nil && event == windows.WAIT_OBJECT_0 {
			return true
		}
		if err != nil || ctx.Err() != nil || time.Now().After(deadline) {
			return false
		}
	}
}

func (t *handleTarget) kill(context.Context) error {
	return t.terminate()
}

func (t *handleTarget) close() {
	_ = windows.CloseHandle(t.handle)
}

// helperProcessExecutable is not read on Windows yet, so a failed
// replacement is reported without restarting the older helper.
func helperProcessExecutable(int) (string, error) {
	return "", fmt.Errorf("%w: the executable of a helper process is not read on Windows", ErrUnsupported)
}

// helperProcessStartTime is the process creation time (a FILETIME), which
// tells a reused pid from the original.
func helperProcessStartTime(pid int) (uint64, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return 0, err
	}
	return uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime), nil
}
