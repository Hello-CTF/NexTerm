package supervisor

import (
	"fmt"
	"path/filepath"
)

// PrepareStateDir creates a helper state directory private to this user, as
// ConnectHelper does, so files can be placed in it before a helper starts.
func PrepareStateDir(stateDir string) error {
	if stateDir == "" {
		return fmt.Errorf("%w: helper state directory is required", ErrInvalidInput)
	}
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return fmt.Errorf("%w: helper state directory: %v", ErrInvalidInput, err)
	}
	if err := ensurePrivateDir(absolute); err != nil {
		return fmt.Errorf("supervisor helper state directory: %w", err)
	}
	return nil
}
