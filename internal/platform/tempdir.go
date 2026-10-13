package platform

import (
	"os"
	"path/filepath"
	"runtime"
)

func TempDir() string {
	fallback := os.TempDir()
	dir := tempDir(runtime.GOOS, fallback, userHomeDir(), androidFilesDir)
	if dir != fallback {
		_ = os.MkdirAll(dir, 0o700)
	}
	return dir
}

func tempDir(goos, fallback, home string, androidFiles func() (string, bool)) string {
	switch goos {
	case "android":
		if dir, ok := androidFiles(); ok {
			return filepath.Join(dir, "tmp")
		}
	case "ios":
		if home != "" {
			return filepath.Join(home, "tmp")
		}
	}
	return fallback
}

func userHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
