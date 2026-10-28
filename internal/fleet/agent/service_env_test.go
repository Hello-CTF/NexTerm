package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envGetter(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func executableShell(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCaptureServiceEnvironment(t *testing.T) {
	zsh := executableShell(t, "zsh")
	account := executableShell(t, "acct-shell")
	lookup := func() string { return account }
	env := captureServiceEnvironment(envGetter(map[string]string{"SHELL": zsh, "PATH": "/opt/homebrew/bin:/usr/bin"}), lookup)
	if env.Shell != zsh || env.ShellFrom != "SHELL" || env.Path != "/opt/homebrew/bin:/usr/bin" {
		t.Fatalf("env = %+v", env)
	}
	nologin := executableShell(t, "nologin")
	for _, shell := range []string{"", "zsh", nologin, filepath.Join(t.TempDir(), "missing")} {
		env = captureServiceEnvironment(envGetter(map[string]string{"SHELL": shell}), lookup)
		if env.Shell != account || env.ShellFrom != "account" {
			t.Fatalf("SHELL=%q env = %+v, want account fallback", shell, env)
		}
	}
	env = captureServiceEnvironment(envGetter(map[string]string{"PATH": "rel/bin:/usr/bin"}), lookup)
	if env.Path != "/usr/bin" {
		t.Fatalf("relative entries must be dropped: %q", env.Path)
	}
	for _, path := range []string{"", "rel/bin", "/usr/bin\n/evil"} {
		if env = captureServiceEnvironment(envGetter(map[string]string{"PATH": path}), lookup); env.Path != "" || env.PathSource != "" {
			t.Fatalf("PATH=%q must be omitted: %+v", path, env)
		}
	}
}

func TestLaunchdPlistEnvironmentVariables(t *testing.T) {
	zsh := executableShell(t, "zsh")
	getenv := envGetter(map[string]string{"SHELL": zsh, "PATH": `/a&b:/c<d>:/e"f`})
	plist := renderLaunchdPlistEnv("/bin/nexterm", "/data", captureServiceEnvironment(getenv, nil))
	for _, expected := range []string{
		"<key>EnvironmentVariables</key>\n  <dict>\n",
		"<key>PATH</key>\n    <string>/a&amp;b:/c&lt;d&gt;:/e&quot;f</string>",
		"<key>SHELL</key>\n    <string>" + zsh + "</string>",
	} {
		if !strings.Contains(plist, expected) {
			t.Fatalf("plist missing %q:\n%s", expected, plist)
		}
	}
	bare := renderLaunchdPlistEnv("/bin/nexterm", "/data", captureServiceEnvironment(envGetter(nil), nil))
	if strings.Contains(bare, "EnvironmentVariables") {
		t.Fatalf("empty environment must be omitted:\n%s", bare)
	}
	onlyShell := renderLaunchdPlistEnv("/bin/nexterm", "/data", captureServiceEnvironment(envGetter(map[string]string{"SHELL": zsh}), nil))
	if !strings.Contains(onlyShell, "<key>SHELL</key>") || strings.Contains(onlyShell, "<key>PATH</key>") {
		t.Fatalf("empty PATH must be omitted:\n%s", onlyShell)
	}
}

func TestLaunchdInstallRecordsEnvironment(t *testing.T) {
	home := t.TempDir()
	account := executableShell(t, "acct-shell")
	runner := &fakeRunner{responses: map[string]string{
		"launchctl bootout gui/501/" + launchdLabel:                                                            "",
		"launchctl bootstrap gui/501 " + filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"): "",
		"launchctl enable gui/501/" + launchdLabel:                                                             "",
	}}
	manager := newLaunchdManagerEnv(runner, envGetter(map[string]string{"SHELL": "/usr/sbin/nologin", "PATH": "/opt/a&b:/usr/bin"}), home, "501")
	manager.lookupShell = func() string { return account }
	if err := manager.Install(context.Background(), "/bin/nexterm", "/data"); err != nil {
		t.Fatal(err)
	}
	env, ok := manager.RecordedEnvironment()
	if !ok || env.Shell != account || env.Path != "/opt/a&b:/usr/bin" {
		t.Fatalf("recorded = %+v ok=%t", env, ok)
	}
	if lines := strings.Join(env.describe(), "\n"); !strings.Contains(lines, account) || !strings.Contains(lines, "/opt/a&b:/usr/bin") {
		t.Fatalf("describe = %s", lines)
	}
}

func TestSystemdUnitEnvironmentLines(t *testing.T) {
	zsh := executableShell(t, "zsh")
	env := captureServiceEnvironment(envGetter(map[string]string{"SHELL": zsh, "PATH": `/opt/100%/bin:/a"b:/c\d`}), nil)
	unit := renderSystemdUnitEnv("/bin/nexterm", "/data", env)
	for _, expected := range []string{
		"Environment=\"SHELL=" + zsh + "\"\n",
		`Environment="PATH=/opt/100%%/bin:/a\"b:/c\\d"` + "\n",
	} {
		if !strings.Contains(unit, expected) {
			t.Fatalf("unit missing %q:\n%s", expected, unit)
		}
	}
	if strings.Index(unit, "Environment=") < strings.Index(unit, "[Service]") {
		t.Fatalf("Environment must be in [Service]:\n%s", unit)
	}
	if bare := renderSystemdUnitEnv("/bin/nexterm", "/data", ServiceEnvironment{}); strings.Contains(bare, "Environment=") {
		t.Fatalf("empty environment must be omitted:\n%s", bare)
	}
}

func TestSystemdInstallRecordsEnvironment(t *testing.T) {
	home := t.TempDir()
	zsh := executableShell(t, "zsh")
	runner := &fakeRunner{responses: map[string]string{
		"systemctl --user daemon-reload":                   "",
		"systemctl --user enable --now " + systemdUnitName: "",
	}}
	manager := newSystemdManager(runner, envGetter(map[string]string{"SHELL": zsh, "PATH": "/opt/100%/bin:/usr/bin"}), home).(*systemdManager)
	if err := manager.Install(context.Background(), "/bin/nexterm", "/data"); err != nil {
		t.Fatal(err)
	}
	env, ok := manager.RecordedEnvironment()
	if !ok || env.Shell != zsh || env.Path != "/opt/100%/bin:/usr/bin" {
		t.Fatalf("recorded = %+v ok=%t", env, ok)
	}
}
