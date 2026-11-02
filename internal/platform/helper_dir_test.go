package platform

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedHelperStateDirPerOS(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "user")
	abs := func(parts ...string) string {
		return filepath.Join(append([]string{string(filepath.Separator)}, parts...)...)
	}
	cases := []struct {
		name string
		goos string
		env  map[string]string
		want string
	}{
		{"linux default", "linux", nil, filepath.Join(home, ".local", "state", "nexterm", "supervisor")},
		{"freebsd default", "freebsd", nil, filepath.Join(home, ".local", "state", "nexterm", "supervisor")},
		{"linux xdg", "linux", map[string]string{"XDG_STATE_HOME": abs("var", "state")}, abs("var", "state", "nexterm", "supervisor")},
		{"linux relative xdg ignored", "linux", map[string]string{"XDG_STATE_HOME": "rel"}, filepath.Join(home, ".local", "state", "nexterm", "supervisor")},
		{"darwin", "darwin", map[string]string{"XDG_STATE_HOME": abs("x")}, filepath.Join(home, "Library", "Application Support", "NexTerm", "shared", "supervisor")},
	}
	for _, tc := range cases {
		got, err := SharedHelperStateDir(tc.goos, func(k string) string { return tc.env[k] }, home)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

func TestSharedHelperStateDirWindows(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("windows path semantics")
	}
	got, err := SharedHelperStateDir("windows", func(k string) string {
		if k == "LOCALAPPDATA" {
			return `C:\Users\u\AppData\Local`
		}
		return ""
	}, `C:\Users\u`)
	if err != nil || got != `C:\Users\u\AppData\Local\NexTerm\shared\supervisor` {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestSharedHelperStateDirWindowsFallsBackToHome(t *testing.T) {
	got, err := SharedHelperStateDir("windows", nil, filepath.Join("h", "u"))
	want := filepath.Join("h", "u", "AppData", "Local", "NexTerm", "shared", "supervisor")
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestSharedHelperStateDirMobileHasNone(t *testing.T) {
	for _, goos := range []string{"android", "ios"} {
		if _, err := SharedHelperStateDir(goos, nil, "/h"); !errors.Is(err, ErrNoSharedHelperDir) {
			t.Errorf("%s: err = %v", goos, err)
		}
	}
}

func TestSharedHelperStateDirNeedsHome(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		if _, err := SharedHelperStateDir(goos, nil, ""); err == nil {
			t.Errorf("%s: expected error without home", goos)
		}
	}
}

func TestSharedHelperStateDirIsOutsideAppDataDirs(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "user")
	desktop, err := desktopDataDir("linux", home)
	if err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(home, ".local", "share", "NexTerm")
	for _, goos := range []string{"linux", "darwin"} {
		shared, err := SharedHelperStateDir(goos, nil, home)
		if err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{desktop, agent} {
			if shared == dir || strings.HasPrefix(shared, dir+string(filepath.Separator)) {
				t.Errorf("%s: shared dir %q lies inside %q", goos, shared, dir)
			}
		}
	}
}
