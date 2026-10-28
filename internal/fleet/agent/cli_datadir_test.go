package agent

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveDataDirPrecedenceAndPlatformDefaults(t *testing.T) {
	home := func() (string, error) { return filepath.FromSlash("/home/u"), nil }
	noHome := func() (string, error) { return "", errors.New("no home") }
	tests := []struct {
		name  string
		flag  string
		goos  string
		env   map[string]string
		home  func() (string, error)
		want  string
		fails bool
	}{
		{name: "flag wins", flag: "/f", goos: "linux", env: map[string]string{"NEXTERM_DATA_DIR": "/e"}, home: home, want: "/f"},
		{name: "env over default", goos: "linux", env: map[string]string{"NEXTERM_DATA_DIR": "/e"}, home: home, want: "/e"},
		{name: "linux default", goos: "linux", home: home, want: filepath.Join("/home/u", ".local", "share", "NexTerm")},
		{name: "darwin default", goos: "darwin", home: home, want: filepath.Join("/home/u", ".local", "share", "NexTerm")},
		{name: "xdg default", goos: "linux", env: map[string]string{"XDG_DATA_HOME": "/xdg"}, home: home, want: filepath.Join("/xdg", "NexTerm")},
		{name: "windows localappdata", goos: "windows", env: map[string]string{"LOCALAPPDATA": `C:\L`}, home: home, want: filepath.Join(`C:\L`, "NexTerm")},
		{name: "windows fallback", goos: "windows", home: home, want: filepath.Join("/home/u", "AppData", "Local", "NexTerm")},
		{name: "no home fails", goos: "linux", home: noHome, fails: true},
		{name: "explicit flag needs no home", flag: "/f", goos: "linux", home: noHome, want: "/f"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveDataDirFor(tc.flag, tc.goos, func(k string) string { return tc.env[k] }, tc.home)
			if tc.fails {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestCLIStatusMissingDataDirMessage(t *testing.T) {
	stubCLIManager(t)
	missing := filepath.Join(t.TempDir(), "absent")
	getenv := func(k string) string {
		if k == "NEXTERM_DATA_DIR" {
			return missing
		}
		return ""
	}
	var stdout, stderr bytes.Buffer
	if code := RunCLI([]string{"status"}, getenv, &stdout, &stderr); code != ExitError {
		t.Fatalf("code = %d", code)
	}
	want := "未找到设备代理的数据目录：" + missing + "。设备可能尚未接入。"
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCLIStatusExplicitDataDirStillWorks(t *testing.T) {
	stubCLIManager(t)
	var stdout, stderr bytes.Buffer
	if code := RunCLI([]string{"status", "--data-dir", t.TempDir()}, func(string) string { return "" }, &stdout, &stderr); code != ExitOK {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "未注册") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}
