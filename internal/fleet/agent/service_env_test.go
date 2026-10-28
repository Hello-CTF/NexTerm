package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/shellenv"
)

const pollutedPlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.nexterm.agent</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/polluted/bin:/usr/bin</string>
    <key>SHELL</key>
    <string>/bin/bash</string>
  </dict>
</dict>
</plist>
`

func TestPlistAndUnitDoNotRecordInstallerEnvironment(t *testing.T) {
	t.Setenv("SHELL", "/bin/bash")
	t.Setenv("PATH", "/polluted/bin:/usr/bin")
	plist := renderLaunchdPlist("/bin/nexterm", "/data")
	unit := renderSystemdUnit("/bin/nexterm", "/data")
	for name, text := range map[string]string{"plist": plist, "unit": unit} {
		for _, forbidden := range []string{"SHELL", "PATH", "polluted", "Environment"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s must not record %q:\n%s", name, forbidden, text)
			}
		}
	}
}

func TestLaunchdReinstallOverwritesPollutedEnvironment(t *testing.T) {
	home := t.TempDir()
	plistPath := filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plistPath, []byte(pollutedPlist), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{responses: map[string]string{
		"launchctl bootout gui/501/" + launchdLabel: "",
		"launchctl bootstrap gui/501 " + plistPath:  "",
		"launchctl enable gui/501/" + launchdLabel:  "",
	}}
	manager := newLaunchdManagerEnv(runner, nil, home, "501")
	before, ok := manager.RecordedEnvironment()
	if !ok || before.Shell != "/bin/bash" || before.Path != "/polluted/bin:/usr/bin" {
		t.Fatalf("fixture not read: %+v", before)
	}
	if lines := strings.Join(before.describe(), "\n"); !strings.Contains(lines, "不再采用") {
		t.Fatalf("legacy values must be labelled non-authoritative: %s", lines)
	}
	if err := manager.Install(context.Background(), "/bin/nexterm", "/data"); err != nil {
		t.Fatal(err)
	}
	after, ok := manager.RecordedEnvironment()
	if !ok || after.Shell != "" || after.Path != "" {
		t.Fatalf("reinstall must drop recorded values: %+v", after)
	}
}

func TestSystemdReinstallOverwritesPollutedEnvironment(t *testing.T) {
	home := t.TempDir()
	unitPath := filepath.Join(home, ".config", "systemd", "user", systemdUnitName)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	old := "[Service]\nEnvironment=\"SHELL=/bin/bash\"\nEnvironment=\"PATH=/polluted/bin\"\n"
	if err := os.WriteFile(unitPath, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{responses: map[string]string{
		"systemctl --user daemon-reload":                   "",
		"systemctl --user enable --now " + systemdUnitName: "",
	}}
	manager := newSystemdManager(runner, envGetter(nil), home).(*systemdManager)
	if before, _ := manager.RecordedEnvironment(); before.Shell != "/bin/bash" || before.Path != "/polluted/bin" {
		t.Fatalf("fixture not read: %+v", before)
	}
	if err := manager.Install(context.Background(), "/bin/nexterm", "/data"); err != nil {
		t.Fatal(err)
	}
	if after, _ := manager.RecordedEnvironment(); after.Shell != "" || after.Path != "" {
		t.Fatalf("reinstall must drop recorded values: %+v", after)
	}
}

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

func TestApplyShellFlag(t *testing.T) {
	dir := t.TempDir()
	zsh := executableShell(t, "zsh")
	if message, err := applyShellFlag(dir, ""); err != nil || message != "" {
		t.Fatalf("empty flag must be a no-op: %q %v", message, err)
	}
	if _, err := applyShellFlag(dir, "zsh"); err == nil {
		t.Fatal("relative shell must be rejected")
	}
	if _, err := applyShellFlag(dir, executableShell(t, "nologin")); err == nil {
		t.Fatal("nologin must be rejected")
	}
	if _, err := applyShellFlag(dir, zsh); err != nil {
		t.Fatal(err)
	}
	if got := shellenv.ReadShellFile(shellFile(dir)); got != zsh {
		t.Fatalf("shell file = %q", got)
	}
	if _, err := applyShellFlag(dir, "auto"); err != nil {
		t.Fatal(err)
	}
	if got := shellenv.ReadShellFile(shellFile(dir)); got != "" {
		t.Fatalf("auto must clear the setting, got %q", got)
	}
}

func TestStatusReportsChosenShellAndSource(t *testing.T) {
	dir := t.TempDir()
	zsh := executableShell(t, "zsh")
	if _, err := applyShellFlag(dir, zsh); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printRuntimeShell(&out, dir, envGetter(map[string]string{"SHELL": "/bin/bash", "PATH": "/usr/bin"}), 200*time.Millisecond)
	text := out.String()
	if !strings.Contains(text, "新终端 shell: "+zsh+" (来源: explicit)") {
		t.Fatalf("status output:\n%s", text)
	}
}
