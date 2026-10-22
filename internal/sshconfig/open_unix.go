//go:build !windows

package sshconfig

import "os"

// openSharedDelete opens path for reading — config parses and one-shot
// identity-key reads; a plain open already provides the Unix semantics these
// readers rely on (unlink and rename never block on open readers), so no
// sharing flags are needed off Windows.
func openSharedDelete(path string) (*os.File, error) {
	return os.Open(path)
}
