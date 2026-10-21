//go:build !windows

package sshconfig

import "os"

// openSharedDelete opens path for parsing; a plain open already provides the
// Unix semantics the parser relies on (unlink and rename never block on open
// readers), so no sharing flags are needed off Windows.
func openSharedDelete(path string) (*os.File, error) {
	return os.Open(path)
}
