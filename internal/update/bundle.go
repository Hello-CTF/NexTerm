package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// directoryWritable reports whether this process can create files in directory.
func directoryWritable(directory string) error {
	file, err := os.CreateTemp(directory, ".nexterm-writecheck-")
	if err != nil {
		return err
	}
	name := file.Name()
	return errors.Join(file.Close(), os.Remove(name))
}

// appBundleOf returns the .app bundle that contains a macOS executable at <bundle>/Contents/MacOS/<name>, or ""
// when the executable is not inside one.
func appBundleOf(executable string) string {
	macos := filepath.Dir(filepath.Clean(executable))
	contents := filepath.Dir(macos)
	bundle := filepath.Dir(contents)
	if filepath.Base(macos) != "MacOS" || filepath.Base(contents) != "Contents" || !strings.HasSuffix(bundle, ".app") {
		return ""
	}
	return bundle
}

// macInstallTarget picks where a macOS update is installed: the bundle the app runs from, so an app kept outside
// /Applications or renamed stays where it is, unless that copy runs from a disk image, from App Translocation (a
// quarantined app macOS runs from a random read-only path) or from a directory this user cannot write; those
// fall back to /Applications/<appName>.
func macInstallTarget(executable, appName string, writable func(string) error) string {
	fallback := filepath.Join("/Applications", appName)
	bundle := appBundleOf(executable)
	if bundle == "" || strings.HasPrefix(bundle, "/Volumes/") || strings.Contains(bundle, "/AppTranslocation/") {
		return fallback
	}
	if writable(filepath.Dir(bundle)) != nil {
		return fallback
	}
	return bundle
}

// replaceBundle copies the new bundle into a staging directory next to target with copyTo, then swaps it in by
// renaming. The previous bundle is removed only after the new one is in place; when the swap fails, the previous
// bundle is put back, and kept in the staging directory if even that fails.
func replaceBundle(target string, copyTo func(staged string) error) error {
	staging, err := os.MkdirTemp(filepath.Dir(target), ".nexterm-update-")
	if err != nil {
		return err
	}
	retain := false
	defer func() {
		if !retain {
			_ = os.RemoveAll(staging)
		}
	}()
	staged := filepath.Join(staging, filepath.Base(target))
	if err := copyTo(staged); err != nil {
		return err
	}
	previous := filepath.Join(staging, "previous")
	hadPrevious := false
	if _, err := os.Lstat(target); err == nil {
		if err := os.Rename(target, previous); err != nil {
			return err
		}
		hadPrevious = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(staged, target); err != nil {
		if hadPrevious {
			if restoreErr := os.Rename(previous, target); restoreErr != nil {
				retain = true
				return errors.Join(err, restoreErr)
			}
		}
		return err
	}
	return nil
}
