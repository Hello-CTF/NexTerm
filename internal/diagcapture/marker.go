// Package diagcapture implements a one-shot debug capture: when armed, the next launch records profiles,
// verbose redacted logs and runtime statistics into a folder the user can zip and send.
package diagcapture

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Hello-CTF/NexTerm/internal/platform"
)

const (
	markerName  = "debug-capture-next"
	captureRoot = "debug-capture"
	// EnvEnable runs a capture for one launch without touching the marker.
	EnvEnable = "NEXTERM_DEBUG_CAPTURE"
)

func markerPath(paths platform.Paths) string { return filepath.Join(paths.DataDir, markerName) }

// Root is the folder that holds every capture directory and packed archive.
func Root(paths platform.Paths) string { return filepath.Join(paths.LogDir, captureRoot) }

// Arm makes the next launch record a capture.
func Arm(paths platform.Paths) error {
	if paths.DataDir == "" {
		return errors.New("data directory is not configured")
	}
	if err := os.MkdirAll(paths.DataDir, 0o700); err != nil {
		return fmt.Errorf("arm debug capture: %w", err)
	}
	if err := os.WriteFile(markerPath(paths), []byte("armed\n"), 0o600); err != nil {
		return fmt.Errorf("arm debug capture: %w", err)
	}
	return nil
}

// Disarm cancels a pending capture; disarming when not armed is not an error.
func Disarm(paths platform.Paths) error {
	if err := os.Remove(markerPath(paths)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("disarm debug capture: %w", err)
	}
	return nil
}

func IsArmed(paths platform.Paths) bool {
	_, err := os.Stat(markerPath(paths))
	return err == nil
}
