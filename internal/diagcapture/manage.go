package diagcapture

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/Hello-CTF/NexTerm/internal/platform"
)

// ErrCaptureActive is returned when asked to delete the capture that is still recording.
var ErrCaptureActive = errors.New("the capture is still recording")

// Delete removes one capture folder and its packed archive. The folder must pass ResolveCapture, and the
// folder that is still recording (activeDir) is refused.
func Delete(paths platform.Paths, dir, activeDir string) error {
	resolved, err := ResolveCapture(paths, dir)
	if err != nil {
		return err
	}
	if activeDir != "" && filepath.Clean(activeDir) == resolved {
		return ErrCaptureActive
	}
	return removeCapture(resolved)
}

// Clear removes every capture folder and archive except the one still recording, and returns how many
// folders it removed. Entries that are not plain folders directly under the capture root are left alone.
func Clear(paths platform.Paths, activeDir string) (int, error) {
	entries, err := List(paths)
	if err != nil {
		return 0, err
	}
	removed := 0
	var firstErr error
	for _, entry := range entries {
		resolved, err := ResolveCapture(paths, entry.Path)
		if err != nil {
			continue
		}
		if activeDir != "" && filepath.Clean(activeDir) == resolved {
			continue
		}
		if err := removeCapture(resolved); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}

func removeCapture(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.Remove(dir + ".zip"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
