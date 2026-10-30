package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// MobileDownloadPrefix marks a local path the frontend cannot pick itself: the Wails mobile shells have no save dialog.
const MobileDownloadPrefix = "nexterm-mobile-download:"

// MobileDownloadPath resolves a MobileDownloadPrefix reference to a file inside the app download directory.
// ok is false when path carries no prefix or the host is not a mobile shell.
func MobileDownloadPath(path string) (string, bool, error) {
	name, found := strings.CutPrefix(path, MobileDownloadPrefix)
	if !found || (runtime.GOOS != "android" && runtime.GOOS != "ios") {
		return "", false, nil
	}
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || name == "." || name == ".." || name == string(filepath.Separator) {
		name = "download"
	}
	dir, err := mobileDownloadDir()
	if err != nil {
		return "", true, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", true, err
	}
	return filepath.Join(dir, name), true, nil
}

// iOS keeps Documents visible in the Files app (UIFileSharingEnabled); Android uses the app-specific external
// directory, which needs no storage permission, and falls back to the private files directory.
func mobileDownloadDir() (string, error) {
	if runtime.GOOS == "android" {
		if filesDir, ok := androidFilesDir(); ok {
			pkg := filepath.Base(filepath.Dir(filesDir))
			external := filepath.Join("/storage/emulated/0/Android/data", pkg, "files", "Download")
			if err := os.MkdirAll(external, 0o755); err == nil {
				return external, nil
			}
			return filepath.Join(filesDir, "Download"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Documents"), nil
}
