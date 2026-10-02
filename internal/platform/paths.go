package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	if err != nil {
		return Paths{}, err
	}
	dataDir, err := desktopDataDir(runtime.GOOS, home, os.Getenv)
	if err != nil {
		return Paths{}, err
	}
	return NewPaths(dataDir), nil
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

func desktopDataDir(goos, home string, getenv func(string) string) (string, error) {
	switch goos {
	case "darwin":
		if home == "" {
			return "", fmt.Errorf("home directory is empty")
		}
		return filepath.Join(home, "Library", "Application Support", "NexTerm"), nil
	case "windows":
		base := getenv("APPDATA")
		if base == "" && home != "" {
			base = filepath.Join(home, "AppData", "Roaming")
		}
		if base == "" {
			return "", fmt.Errorf("APPDATA is not set")
		}
		return filepath.Join(base, "NexTerm"), nil
	case "linux":
		base := getenv("XDG_DATA_HOME")
		if base == "" && home != "" {
			base = filepath.Join(home, ".local", "share")
		}
		if base == "" {
			return "", fmt.Errorf("XDG_DATA_HOME is not set and home directory is empty")
		}
		return filepath.Join(base, "NexTerm"), nil
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
