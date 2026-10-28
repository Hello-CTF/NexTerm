//go:build !windows

package localhelper

// stageExecutable runs the helper from the installed executable: Unix systems
// replace a running binary by renaming over it, and a macOS app bundle's
// signature covers the executable only inside the bundle.
func stageExecutable(_ string, executable string) (string, error) {
	return executable, nil
}
