//go:build !windows

package server

import (
	"fmt"
	"os"
)

func validateDBPasswordFilePermissions(passwordFile string, info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("database password file %s must have 0600 permissions (got %04o)", passwordFile, info.Mode().Perm())
	}
	return nil
}
