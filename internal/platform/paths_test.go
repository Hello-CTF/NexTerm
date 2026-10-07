package platform

import (
	"path/filepath"
	"testing"
)

func TestDesktopDataDirDefaultsToConfigHome(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "user")
	want := filepath.Join(home, ".config", "nexterm")
	for _, goos := range []string{"darwin", "linux", "windows"} {
		got, err := desktopDataDir(goos, home)
		if err != nil {
			t.Fatalf("%s: %v", goos, err)
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", goos, got, want)
		}
	}
}

func TestDesktopDataDirRequiresHome(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		if _, err := desktopDataDir(goos, ""); err == nil {
			t.Fatalf("%s: expected error for empty home", goos)
		}
	}
}

func TestDesktopDataDirRejectsUnsupportedOS(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "user")
	if _, err := desktopDataDir("plan9", home); err == nil {
		t.Fatal("expected error for unsupported OS")
	}
}

func TestDesktopPathsOverrideWins(t *testing.T) {
	override := filepath.Join(string(filepath.Separator), "custom")
	got, err := DesktopPaths(override)
	if err != nil {
		t.Fatal(err)
	}
	if want := NewPaths(override); got != want {
		t.Fatalf("DesktopPaths(%q) = %+v, want %+v", override, got, want)
	}
}

func TestNewPathsDerivesLogsAndDatabaseUnderDataDir(t *testing.T) {
	dataDir := filepath.Join(string(filepath.Separator), "home", "user", ".config", "nexterm")
	got := NewPaths(dataDir)
	if got.LogDir != filepath.Join(dataDir, "logs") {
		t.Fatalf("LogDir = %q", got.LogDir)
	}
	if got.LogFile != filepath.Join(dataDir, "logs", "nexterm.log") {
		t.Fatalf("LogFile = %q", got.LogFile)
	}
	if got.DatabaseFile != filepath.Join(dataDir, "data.db") {
		t.Fatalf("DatabaseFile = %q", got.DatabaseFile)
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
