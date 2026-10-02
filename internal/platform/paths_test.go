package platform

import (
	"path/filepath"
	"testing"
)

func TestDesktopDataDirPreservesExistingOSPaths(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "user")
	for _, test := range []struct {
		goos string
		env  map[string]string
		want string
	}{
		{goos: "darwin", want: filepath.Join(home, "Library", "Application Support", "NexTerm")},
		{goos: "windows", env: map[string]string{"APPDATA": filepath.Join(home, "Roaming")}, want: filepath.Join(home, "Roaming", "NexTerm")},
		{goos: "windows", want: filepath.Join(home, "AppData", "Roaming", "NexTerm")},
		{goos: "linux", env: map[string]string{"XDG_DATA_HOME": filepath.Join(home, "xdg")}, want: filepath.Join(home, "xdg", "NexTerm")},
		{goos: "linux", want: filepath.Join(home, ".local", "share", "NexTerm")},
	} {
		getenv := func(key string) string { return test.env[key] }
		got, err := desktopDataDir(test.goos, home, getenv)
		if err != nil {
			t.Fatalf("%s: %v", test.goos, err)
		}
		if got != test.want {
			t.Fatalf("%s = %q, want %q", test.goos, got, test.want)
		}
	}
}

func TestServerDataDirPrecedence(t *testing.T) {
	workingDir := filepath.Join(string(filepath.Separator), "work")
	environment := map[string]string{"NEXTERM_DATA_DIR": filepath.Join(workingDir, "env")}
	getenv := func(key string) string { return environment[key] }
	for _, test := range []struct {
		name       string
		override   string
		lazyCatVar bool
		want       string
	}{
		{name: "flag", override: "/flag", lazyCatVar: true, want: "/flag"},
		{name: "environment", lazyCatVar: true, want: filepath.Join(workingDir, "env")},
		{name: "lazycat", lazyCatVar: true, want: filepath.Join(string(filepath.Separator), "lzcapp", "var", "nexterm")},
		{name: "working directory", want: filepath.Join(workingDir, "data")},
	} {
		if test.name == "flag" || test.name == "environment" {
			getenv = func(key string) string { return environment[key] }
		} else {
			getenv = func(string) string { return "" }
		}
		got, err := serverDataDir(test.override, workingDir, test.lazyCatVar, getenv)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if got != test.want {
			t.Fatalf("%s = %q, want %q", test.name, got, test.want)
		}
	}
}
