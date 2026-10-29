package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type recordedCommand struct {
	name string
	args []string
}

type fakeRunner struct {
	commands  []recordedCommand
	responses map[string]string
	errors    map[string]error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.commands = append(f.commands, recordedCommand{name: name, args: args})
	key := name + " " + strings.Join(args, " ")
	if err, ok := f.errors[key]; ok {
		return f.responses[key], err
	}
	if response, ok := f.responses[key]; ok {
		return response, nil
	}
	return "", fmt.Errorf("unexpected command %s", key)
}

func (f *fakeRunner) ran(prefix string) bool {
	for _, command := range f.commands {
		if strings.HasPrefix(command.name+" "+strings.Join(command.args, " "), prefix) {
			return true
		}
	}
	return false
}

func realExitError(t *testing.T) error {
	t.Helper()
	command := exec.Command("sh", "-c", "exit 3")
	if runtime.GOOS == "windows" {
		command = exec.Command("cmd", "/c", "exit 3")
	}
	err := command.Run()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("expected exit error, got %v", err)
	}
	return err
}

func TestSystemdManagerInstallStatusUninstall(t *testing.T) {
	home := t.TempDir()
	runner := &fakeRunner{
		responses: map[string]string{
			"systemctl --user daemon-reload":                    "",
			"systemctl --user enable --now " + systemdUnitName:  "",
			"systemctl --user disable --now " + systemdUnitName: "",
		},
		errors: map[string]error{
			"systemctl --user is-enabled " + systemdUnitName: realExitError(t),
			"systemctl --user is-active " + systemdUnitName:  realExitError(t),
		},
	}
	runner.responses["systemctl --user is-enabled "+systemdUnitName] = "Failed to get unit file state: No such file or directory\n"
	runner.responses["systemctl --user is-active "+systemdUnitName] = "inactive\n"
	manager := NewServiceManager("linux", runner, func(string) string { return "" }, home)
	ctx := context.Background()

	status := manager.Status(ctx)
	if status.Installed || status.Enabled || status.Active || status.LastError != "" {
		t.Fatalf("status before install = %+v", status)
	}
	if err := manager.Install(ctx, "/opt/nexterm/nexterm-desktop", "/var/lib/nexterm"); err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", systemdUnitName))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"ExecStart=\"/opt/nexterm/nexterm-desktop\" run --data-dir \"/var/lib/nexterm\"", "Restart=on-failure", "KillMode=process", "WantedBy=default.target", "RestartPreventExitStatus=3 4"} {
		if !strings.Contains(string(unit), expected) {
			t.Fatalf("unit missing %q:\n%s", expected, unit)
		}
	}
	if strings.Contains(string(unit), " agent ") {
		t.Fatalf("unit must not carry the removed agent subcommand:\n%s", unit)
	}
	if !runner.ran("systemctl --user daemon-reload") || !runner.ran("systemctl --user enable --now") {
		t.Fatalf("install commands = %+v", runner.commands)
	}
	runner.errors = map[string]error{}
	runner.responses["systemctl --user is-enabled "+systemdUnitName] = "enabled\n"
	runner.responses["systemctl --user is-active "+systemdUnitName] = "active\n"
	status = manager.Status(ctx)
	if !status.Installed || !status.Enabled || !status.Active || status.LastError != "" {
		t.Fatalf("status after install = %+v", status)
	}
	if err := manager.Uninstall(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", systemdUnitName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unit file still present: %v", err)
	}
	if !runner.ran("systemctl --user disable --now") {
		t.Fatal("uninstall must disable the unit")
	}
}

func TestSystemdManagerReportsOfflineHonestly(t *testing.T) {
	home := t.TempDir()
	runner := &fakeRunner{
		responses: map[string]string{
			"systemctl --user is-enabled " + systemdUnitName: "Failed to connect to bus: No such file or directory\n",
		},
		errors: map[string]error{
			"systemctl --user is-enabled " + systemdUnitName: realExitError(t),
		},
	}
	manager := newSystemdManager(runner, func(string) string { return "" }, home)
	unitPath := filepath.Join(home, ".config", "systemd", "user", systemdUnitName)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}
	status := manager.Status(context.Background())
	if !status.Installed {
		t.Fatal("unit file on disk must count as installed")
	}
	if status.Enabled || status.Active {
		t.Fatalf("offline manager must not claim enabled/active: %+v", status)
	}
	if !strings.Contains(status.LastError, "Failed to connect to bus") {
		t.Fatalf("offline manager must surface the real failure output, got %+v", status)
	}
}

func TestSystemdManagerDisabledUnitIsNotAnError(t *testing.T) {
	home := t.TempDir()
	runner := &fakeRunner{
		responses: map[string]string{
			"systemctl --user is-enabled " + systemdUnitName: "disabled\n",
			"systemctl --user is-active " + systemdUnitName:  "inactive\n",
		},
		errors: map[string]error{
			"systemctl --user is-enabled " + systemdUnitName: realExitError(t),
		},
	}
	manager := newSystemdManager(runner, func(string) string { return "" }, home)
	unitPath := filepath.Join(home, ".config", "systemd", "user", systemdUnitName)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}
	status := manager.Status(context.Background())
	if !status.Installed || status.Enabled || status.Active {
		t.Fatalf("disabled unit status = %+v", status)
	}
	if status.LastError != "" {
		t.Fatalf("disabled unit must not be an error: %+v", status)
	}
}

func TestSystemdUnitEscapesSpecialCharacters(t *testing.T) {
	unit := renderSystemdUnit("/opt/with space/nexterm", "/data/100%/nexterm")
	if !strings.Contains(unit, `ExecStart="/opt/with space/nexterm" run --data-dir "/data/100%%/nexterm"`) {
		t.Fatalf("escaped unit =\n%s", unit)
	}
}

func TestSystemdUnitIncludesCommandPrefix(t *testing.T) {
	unit := renderSystemdUnit("/opt/nexterm/nexterm-server", "/var/lib/nexterm", "agent")
	if !strings.Contains(unit, `ExecStart="/opt/nexterm/nexterm-server" agent run --data-dir "/var/lib/nexterm"`) {
		t.Fatalf("prefixed unit =\n%s", unit)
	}
}

func TestPrefixedSystemdManagerInstallsAgentSubcommand(t *testing.T) {
	home := t.TempDir()
	runner := &fakeRunner{responses: map[string]string{
		"systemctl --user daemon-reload":                   "",
		"systemctl --user enable --now " + systemdUnitName: "",
	}}
	manager := NewServiceManagerWithPrefix("linux", runner, func(string) string { return "" }, home, []string{"agent"})
	if err := manager.Install(context.Background(), "/opt/nexterm/nexterm-server", "/var/lib/nexterm"); err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", systemdUnitName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), `ExecStart="/opt/nexterm/nexterm-server" agent run --data-dir "/var/lib/nexterm"`) {
		t.Fatalf("server-embedded unit =\n%s", unit)
	}
}

func TestLaunchdManagerInstallStatusUninstall(t *testing.T) {
	home := t.TempDir()
	runner := &fakeRunner{responses: map[string]string{
		"launchctl bootout gui/501/" + launchdLabel: "",
	}}
	manager := newLaunchdManager(runner, home, "501")
	ctx := context.Background()
	bootstrapKey := "launchctl bootstrap gui/501 " + filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
	runner.responses[bootstrapKey] = ""
	runner.responses["launchctl enable gui/501/"+launchdLabel] = ""
	if err := manager.Install(ctx, "/Applications/NexTerm.app/nexterm", "/Users/dev/nexterm"); err != nil {
		t.Fatal(err)
	}
	plist, err := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"<string>com.nexterm.agent</string>", "<string>run</string>", "<string>/Users/dev/nexterm</string>", "<key>RunAtLoad</key>"} {
		if !strings.Contains(string(plist), expected) {
			t.Fatalf("plist missing %q:\n%s", expected, plist)
		}
	}
	if strings.Contains(string(plist), "<string>agent</string>") {
		t.Fatalf("plist must not carry the removed agent subcommand:\n%s", plist)
	}
	arguments := "<string>/Applications/NexTerm.app/nexterm</string>\n    <string>run</string>\n    <string>--data-dir</string>\n    <string>/Users/dev/nexterm</string>"
	if !strings.Contains(string(plist), arguments) {
		t.Fatalf("plist ProgramArguments must be <exe> run --data-dir <dir>:\n%s", plist)
	}
	if !runner.ran("launchctl bootstrap gui/501") || !runner.ran("launchctl enable gui/501/com.nexterm.agent") {
		t.Fatalf("install commands = %+v", runner.commands)
	}
	runner.responses["launchctl print gui/501/"+launchdLabel] = "com.nexterm.agent = {\n\tstate = running\n\tpid = 4242\n}\n"
	runner.responses["launchctl print-disabled gui/501"] = "disabled services = {\n\t\"com.apple.mdm.agent\" => disabled\n}\n"
	status := manager.Status(ctx)
	if !status.Installed || !status.Enabled || !status.Active || status.LastError != "" {
		t.Fatalf("status after install = %+v", status)
	}
	runner.responses["launchctl print gui/501/"+launchdLabel] = "com.nexterm.agent = {\n\tstate = not running\n}\n"
	status = manager.Status(ctx)
	if !status.Installed || !status.Enabled || status.Active {
		t.Fatalf("loaded but not running must not report active: %+v", status)
	}
	runner.responses["launchctl print gui/501/"+launchdLabel] = "com.nexterm.agent = {\n\tstate = running\n}\n"
	runner.responses["launchctl print-disabled gui/501"] = "disabled services = {\n\t\"com.nexterm.agent\" => disabled\n}\n"
	status = manager.Status(ctx)
	if !status.Active || status.Enabled {
		t.Fatalf("disabled launchd service must not report enabled: %+v", status)
	}
	if err := manager.Uninstall(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plist still present: %v", err)
	}
}

func TestLaunchdManagerReportsFailuresHonestly(t *testing.T) {
	home := t.TempDir()
	runner := &fakeRunner{}
	manager := newLaunchdManager(runner, home, "501")
	if err := os.MkdirAll(filepath.Join(home, "Library", "LaunchAgents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner.errors = map[string]error{"launchctl print gui/501/" + launchdLabel: realExitError(t)}
	runner.responses = map[string]string{
		"launchctl print gui/501/" + launchdLabel: "Could not find service \"com.nexterm.agent\" in domain for user gui: 501\n",
		"launchctl print-disabled gui/501":        "disabled services = {\n}\n",
	}
	status := manager.Status(context.Background())
	if !status.Installed || status.Active || status.LastError != "" {
		t.Fatalf("missing service must be a normal inactive result: %+v", status)
	}
	runner.responses["launchctl print gui/501/"+launchdLabel] = "Bootstrap failed: 5: Input/output error\n"
	status = manager.Status(context.Background())
	if status.LastError == "" || !strings.Contains(status.LastError, "Bootstrap failed") {
		t.Fatalf("real launchctl failure must surface in LastError: %+v", status)
	}
}

func TestLaunchdDisabledParsesBothFormats(t *testing.T) {
	cases := map[string]bool{
		"disabled services = {\n\t\"com.nexterm.agent\" => disabled\n}":        true,
		"disabled services = {\n\t\"com.nexterm.agent\" => enabled\n}":         false,
		"disabled services = {\n\t\"com.nexterm.agent\" => true\n}":            true,
		"disabled services = {\n\t\"com.nexterm.agent\" => false\n}":           false,
		"disabled services = {\n\t\"com.apple.mdm.agent\" => disabled\n}":      false,
		"disabled services = {\n\t\"com.nexterm.agent.helper\" => disabled\n}": false,
		"disabled services = {\n}":                                             false,
	}
	for output, expected := range cases {
		if got := launchdDisabled(output); got != expected {
			t.Errorf("launchdDisabled(%q) = %v, want %v", output, got, expected)
		}
	}
}

func TestLaunchdManagerMontereyDisabledFormat(t *testing.T) {
	home := t.TempDir()
	runner := &fakeRunner{responses: map[string]string{
		"launchctl bootout gui/501/" + launchdLabel: "",
	}}
	manager := newLaunchdManager(runner, home, "501")
	ctx := context.Background()
	bootstrapKey := "launchctl bootstrap gui/501 " + filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
	runner.responses[bootstrapKey] = ""
	runner.responses["launchctl enable gui/501/"+launchdLabel] = ""
	if err := manager.Install(ctx, "/Applications/NexTerm.app/nexterm", "/Users/dev/nexterm"); err != nil {
		t.Fatal(err)
	}
	runner.responses["launchctl print gui/501/"+launchdLabel] = "com.nexterm.agent = {\n\tstate = running\n}\n"
	runner.responses["launchctl print-disabled gui/501"] = "disabled services = {\n\t\"com.nexterm.agent\" => true\n}\n"
	status := manager.Status(ctx)
	if !status.Active || status.Enabled {
		t.Fatalf("Monterey `=> true` override must report disabled: %+v", status)
	}
	if err := manager.Install(ctx, "/Applications/NexTerm.app/nexterm", "/Users/dev/nexterm"); err != nil {
		t.Fatal(err)
	}
	if !runner.ran("launchctl enable gui/501/" + launchdLabel) {
		t.Fatal("autostart repair must re-enable the service")
	}
	runner.responses["launchctl print-disabled gui/501"] = "disabled services = {\n\t\"com.nexterm.agent\" => false\n}\n"
	status = manager.Status(ctx)
	if !status.Enabled || !status.Active {
		t.Fatalf("Monterey `=> false` override must report enabled after repair: %+v", status)
	}
}

func TestLaunchdPlistKeepsFatalExitsDead(t *testing.T) {
	plist := renderLaunchdPlist("/Applications/NexTerm.app/nexterm", "/Users/dev/nexterm")
	if strings.Contains(plist, "<key>KeepAlive</key>\n  <true/>") {
		t.Fatal("unconditional KeepAlive would relaunch revoked, protocol-mismatched, and single-instance-guard exits")
	}
	for _, expected := range []string{
		"<key>KeepAlive</key>\n  <dict>\n    <key>Crashed</key>\n    <true/>\n  </dict>",
		"<key>RunAtLoad</key>\n  <true/>",
	} {
		if !strings.Contains(plist, expected) {
			t.Fatalf("plist missing %q:\n%s", expected, plist)
		}
	}
}

func TestLaunchdPlistIncludesCommandPrefix(t *testing.T) {
	plist := renderLaunchdPlist("/Applications/NexTerm.app/nexterm-server", "/Users/dev/nexterm", "agent")
	arguments := "<string>/Applications/NexTerm.app/nexterm-server</string>\n    <string>agent</string>\n    <string>run</string>\n    <string>--data-dir</string>\n    <string>/Users/dev/nexterm</string>"
	if !strings.Contains(plist, arguments) {
		t.Fatalf("server-embedded plist =\n%s", plist)
	}
}

func TestUnsupportedPlatformManagerIsHonest(t *testing.T) {
	manager := NewServiceManager("plan9", &fakeRunner{}, func(string) string { return "" }, t.TempDir())
	if err := manager.Install(context.Background(), "exe", "dir"); !errors.Is(err, ErrServiceUnsupported) {
		t.Fatalf("Install = %v", err)
	}
	status := manager.Status(context.Background())
	if status.Installed || status.Enabled || status.Active || status.LastError == "" {
		t.Fatalf("status = %+v", status)
	}
}

func TestRunCLIWithServicePrefixThreadsPrefix(t *testing.T) {
	var seen []string
	restore := cliNewManager
	cliNewManager = func(func(string) string) ServiceManager {
		seen = append([]string(nil), cliServicePrefix...)
		return &fakeManager{}
	}
	t.Cleanup(func() { cliNewManager = restore })
	if code := RunCLIWithServicePrefix([]string{"install", "--data-dir", t.TempDir()}, []string{"agent"}, nil, io.Discard, io.Discard); code != ExitOK {
		t.Fatalf("prefixed install = %d", code)
	}
	if len(seen) != 1 || seen[0] != "agent" {
		t.Fatalf("service prefix seen by manager factory = %q, want [agent]", seen)
	}
	if cliServicePrefix != nil {
		t.Fatalf("cliServicePrefix must be restored after RunCLIWithServicePrefix, got %q", cliServicePrefix)
	}
	seen = nil
	if code := RunCLI([]string{"install", "--data-dir", t.TempDir()}, nil, io.Discard, io.Discard); code != ExitOK {
		t.Fatalf("plain install = %d", code)
	}
	if len(seen) != 0 {
		t.Fatalf("plain RunCLI must not set a service prefix, got %q", seen)
	}
}
