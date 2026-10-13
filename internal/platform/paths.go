package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Paths struct {
	DataDir      string
	LogDir       string
	LogFile      string
	DatabaseFile string
}

func NewPaths(dataDir string) Paths {
	return Paths{
		DataDir:      dataDir,
		LogDir:       filepath.Join(dataDir, "logs"),
		LogFile:      filepath.Join(dataDir, "logs", "nexterm.log"),
		DatabaseFile: filepath.Join(dataDir, "data.db"),
	}
}

func DesktopPaths(override string) (Paths, error) {
	if override != "" {
		return NewPaths(override), nil
	}
	home, err := os.UserHomeDir()
	if err != nil && runtime.GOOS != "android" {
		return Paths{}, err
	}
	if runtime.GOOS == "android" {
		if filesDir, ok := androidFilesDir(); ok {
			home = filesDir
		}
	}
	dataDir, err := desktopDataDir(runtime.GOOS, home)
	if err != nil {
		return Paths{}, err
	}
	return NewPaths(dataDir), nil
}

// Android app processes have no HOME; the package name is the process name, so the private directory path can be derived deterministically.
func androidFilesDir() (string, bool) {
	raw, err := os.ReadFile("/proc/self/cmdline")
	if err != nil {
		return "", false
	}
	name := string(raw)
	if i := strings.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return "/data/data/" + name + "/files", true
}

func ServerPaths(override string) (Paths, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return Paths{}, err
	}
	lazyCatVar := false
	if info, statErr := os.Stat("/lzcapp/var"); statErr == nil {
		lazyCatVar = info.IsDir()
	}
	dataDir, err := serverDataDir(override, workingDir, lazyCatVar, os.Getenv)
	if err != nil {
		return Paths{}, err
	}
	return NewPaths(dataDir), nil
}

func (p Paths) Prepare() error {
	if p.DataDir == "" {
		return fmt.Errorf("data directory is empty")
	}
	if err := os.MkdirAll(p.DataDir, 0o700); err != nil {
		return err
	}
	return os.MkdirAll(p.LogDir, 0o700)
}

func desktopDataDir(goos, home string) (string, error) {
	switch goos {
	case "android":
		if home == "" {
			return "", fmt.Errorf("home directory is empty")
		}
		return filepath.Join(home, "nexterm"), nil
	case "darwin", "linux", "windows", "ios":
		if home == "" {
			return "", fmt.Errorf("home directory is empty")
		}
		if goos == "ios" {
			return filepath.Join(home, "Library", "Application Support", "nexterm"), nil
		}
		return filepath.Join(home, ".config", "nexterm"), nil
	default:
		return "", fmt.Errorf("unsupported desktop OS %s", goos)
	}
}

func serverDataDir(override, workingDir string, lazyCatVar bool, getenv func(string) string) (string, error) {
	if override != "" {
		return override, nil
	}
	if value := getenv("NEXTERM_DATA_DIR"); value != "" {
		return value, nil
	}
	if lazyCatVar {
		return filepath.Join(string(filepath.Separator), "lzcapp", "var", "nexterm"), nil
	}
	if workingDir == "" {
		return "", fmt.Errorf("working directory is empty")
	}
	return filepath.Join(workingDir, "data"), nil
}
