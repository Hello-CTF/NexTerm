//go:build unix && !linux

package supervisor

import "fmt"

// openHelperTarget holds the helper by pid: these systems give no process
// handle, so a forced kill verifies the helper again first.
func openHelperTarget(pid int, reverify helperReverify) (helperTarget, error) {
	return &pidTarget{pid: pid, reverify: reverify}, nil
}

// helperProcessExecutable is unknown on these systems, so a failed
// replacement is reported without restarting the older helper.
func helperProcessExecutable(int) (string, error) {
	return "", fmt.Errorf("%w: the executable of a helper process is not readable on this platform", ErrUnsupported)
}

// helperProcessStartTime is not read on these systems; consent then names
// the helper by pid, protocol and state directory only.
func helperProcessStartTime(int) (uint64, error) {
	return 0, fmt.Errorf("%w: process start time is not read on this platform", ErrUnsupported)
}
