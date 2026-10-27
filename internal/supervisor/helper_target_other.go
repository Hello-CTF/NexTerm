//go:build unix && !linux

package supervisor

// openHelperTarget holds the helper by pid: these systems give no process
// handle, so a forced kill verifies the helper again first.
func openHelperTarget(pid int, reverify helperReverify) (helperTarget, error) {
	return &pidTarget{pid: pid, reverify: reverify}, nil
}
