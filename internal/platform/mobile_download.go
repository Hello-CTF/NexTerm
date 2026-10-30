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
	return mobileDownloadPath(path, runtime.GOOS, androidFilesDir, os.UserHomeDir)
}

func mobileDownloadPath(path, goos string, androidFiles func() (string, bool), userHome func() (string, error)) (string, bool, error) {
	name, found := strings.CutPrefix(path, MobileDownloadPrefix)
	if !found || (goos != "android" && goos != "ios") {
		return "", false, nil
	}
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || name == "." || name == ".." || name == string(filepath.Separator) {
		name = "download"
	}
	dir, err := mobileDownloadDir(goos, androidFiles, userHome)
	if err != nil {
		return "", true, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", true, err
	}
	return filepath.Join(dir, name), true, nil
}

// androidExternalDataRoot is a variable so tests can redirect the app-specific external directory.
var androidExternalDataRoot = "/storage/emulated/0/Android/data"

// iOS keeps Documents visible in the Files app (UIFileSharingEnabled); Android uses the app-specific external
// directory, which needs no storage permission, and falls back to the private files directory.
func mobileDownloadDir(goos string, androidFiles func() (string, bool), userHome func() (string, error)) (string, error) {
	if goos == "android" {
		if filesDir, ok := androidFiles(); ok {
			pkg := filepath.Base(filepath.Dir(filesDir))
			external := filepath.Join(androidExternalDataRoot, pkg, "files", "Download")
			if err := os.MkdirAll(external, 0o755); err == nil {
				return external, nil
			}
			return filepath.Join(filesDir, "Download"), nil
		}
	}
	home, err := userHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Documents"), nil
}
