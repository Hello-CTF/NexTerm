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
