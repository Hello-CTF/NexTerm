package services

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/platform/psquote"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files")

func readTestdata(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

func TestWindowsCommandsMatchGolden(t *testing.T) {
	var out strings.Builder
	write := func(name, command string) {
		script, err := psquote.DecodeCommand(command)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out.WriteString("=== " + name + "\n" + script + "\n")
	}
	write("list", WindowsListCommand())
	for _, action := range []string{ActionStatus, ActionStart, ActionStop, ActionRestart, ActionSetAutomatic, ActionSetManual, ActionSetDisabled} {
		command, err := WindowsActionCommand(action, "Spooler")
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		write(action, command)
	}
	path := filepath.Join("testdata", "windows_commands.golden")
	if *updateGolden {
		if err := os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if want := readTestdata(t, "windows_commands.golden"); out.String() != want {
		t.Fatalf("Windows commands changed; rerun with -update if intended:\n%s", out.String())
	}
}

func TestWindowsActionCommandUsesTheRequestedCmdlet(t *testing.T) {
	for action, cmdlet := range map[string]string{
		ActionStart: "Start-Service", ActionStop: "Stop-Service", ActionRestart: "Restart-Service",
		ActionSetAutomatic: "-StartupType Automatic", ActionSetManual: "-StartupType Manual", ActionSetDisabled: "-StartupType Disabled",
	} {
		command, err := WindowsActionCommand(action, "Spooler")
		if err != nil {
			t.Fatal(err)
		}
		script, _ := psquote.DecodeCommand(command)
		if !strings.Contains(script, cmdlet) {
			t.Errorf("%s script lacks %q:\n%s", action, cmdlet, script)
		}
	}
}

func TestWindowsActionCommandBindsNameAsLiteral(t *testing.T) {
	command, err := WindowsActionCommand(ActionStop, "My'Svc")
	if err != nil {
		t.Fatal(err)
	}
	script, _ := psquote.DecodeCommand(command)
	if !strings.Contains(script, "$name='My''Svc'\n") {
		t.Fatalf("name not quoted as a literal:\n%s", script)
	}
}

func TestWindowsActionCommandRejectsInvalidInput(t *testing.T) {
	for _, name := range []string{"", "a\nb", "a*", "a?b", "[x]", "a/b", `a\b`, "a`b", strings.Repeat("x", 257)} {
		if _, err := WindowsActionCommand(ActionStop, name); !errors.Is(err, ErrInvalid) {
			t.Errorf("name %q error = %v, want ErrInvalid", name, err)
		}
	}
	for _, action := range []string{ActionReload, ActionEnable, ActionDisable, "format-disk", ""} {
		if _, err := WindowsActionCommand(action, "Spooler"); !errors.Is(err, ErrInvalid) {
			t.Errorf("action %q error = %v, want ErrInvalid", action, err)
		}
	}
}

func TestParseWindowsServices(t *testing.T) {
	units, ok := ParseWindowsServices(readTestdata(t, "windows_list.txt"))
	if !ok || len(units) != 4 {
		t.Fatalf("units = %+v, ok = %v", units, ok)
	}
	want := []ServiceUnit{
		{Name: "Spooler", Active: "Running", StartMode: "Auto", PID: 2412, Description: "Print Spooler"},
		{Name: "wuauserv", Active: "Stopped", StartMode: "Manual", Description: "Windows Update"},
		{Name: "BITS", Active: "Running", StartMode: "Delayed", PID: 1180, Description: "Background Intelligent Transfer Service"},
		{Name: "Fax", Active: "Stopped", StartMode: "Disabled", Description: "傳真 \"Fax\""},
	}
	for i := range want {
		if units[i] != want[i] {
			t.Errorf("unit %d = %+v, want %+v", i, units[i], want[i])
		}
	}
}

func TestParseWindowsServicesAcceptsUnwrappedSingleRecord(t *testing.T) {
	units, ok := ParseWindowsServices(readTestdata(t, "windows_list_single.txt"))
	if !ok || len(units) != 1 || units[0].Name != "Spooler" {
		t.Fatalf("units = %+v, ok = %v", units, ok)
	}
}

func TestParseWindowsServicesRejectsForeignOutput(t *testing.T) {
	for _, output := range []string{"", "nginx.service loaded active running nginx\n", windowsServicesSentinel + "\nnot json", windowsServicesSentinel + "\n[{\"name\":\"a\"}"} {
		if units, ok := ParseWindowsServices(output); ok || units == nil || len(units) != 0 {
			t.Errorf("output %q = %+v, %v", output, units, ok)
		}
	}
	// A JSON null (a host with no services) is an empty list, not an error.
	if units, ok := ParseWindowsServices(windowsServicesSentinel + "\n[]"); !ok || len(units) != 0 {
		t.Errorf("empty array = %+v, %v", units, ok)
	}
}

func TestWindowsHostResultClassifiesFailures(t *testing.T) {
	three := 3
	one := 1
	cases := []struct {
		name   string
		result execbatch.Result
		list   bool
		want   string
	}{
		{"not found", execbatch.Result{Status: execbatch.StatusExitError, ExitCode: &three, Stderr: "service not found: x"}, false, "not_found"},
		{"denied", execbatch.Result{Status: execbatch.StatusExitError, ExitCode: &one, Stderr: "Access is denied"}, false, "permission"},
		{"no powershell", execbatch.Result{Status: execbatch.StatusExitError, ExitCode: &one, Stderr: "'powershell' is not recognized as an internal or external command"}, true, "powershell"},
		{"timeout", execbatch.Result{Status: execbatch.StatusTimeout}, true, "timeout"},
		{"transport", execbatch.Result{Status: execbatch.StatusTransportError}, true, "transport"},
		{"garbage", execbatch.Result{Status: execbatch.StatusSuccess, Stdout: "hello"}, true, "output"},
	}
	for _, c := range cases {
		host := windowsHostResult(c.result, c.list)
		if host.FailureKind != c.want || host.Platform != PlatformWindows {
			t.Errorf("%s: kind = %q platform = %q, want %q", c.name, host.FailureKind, host.Platform, c.want)
		}
	}
}

func platformByPrefix(_ context.Context, _ string, sessionID string) (string, error) {
	if strings.HasPrefix(sessionID, "win") {
		return PlatformWindows, nil
	}
	return PlatformSystemd, nil
}

func TestServiceRoutesWindowsSessionsToPowerShell(t *testing.T) {
	executor := &fakeExecutor{batch: execbatch.Batch{Results: []execbatch.Result{
		{SessionID: "win1", Status: execbatch.StatusSuccess, Stdout: readTestdata(t, "windows_list.txt")},
	}}}
	service := New(executor).WithPlatformResolver(platformByPrefix)
	listed, err := service.List(t.Context(), "owner", ListRequest{BatchID: "l", SessionIDs: []string{"win1"}, Sudo: true})
	if err != nil {
		t.Fatal(err)
	}
	if executor.requests[0].Command != WindowsListCommand() {
		t.Fatalf("list command = %q", executor.requests[0].Command)
	}
	if host := listed.Hosts[0]; host.Platform != PlatformWindows || len(host.Units) != 4 {
		t.Fatalf("host = %+v", host)
	}
	executor.batch.Results[0].Stdout = "Spooler: Running"
	acted, err := service.Action(t.Context(), "owner", ActionRequest{BatchID: "a", SessionIDs: []string{"win1"}, Unit: "Spooler", Action: ActionRestart})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := WindowsActionCommand(ActionRestart, "Spooler")
	if executor.requests[1].Command != want || acted.Hosts[0].Platform != PlatformWindows || acted.Hosts[0].FailureKind != "" {
		t.Fatalf("action request = %+v, result = %+v", executor.requests[1], acted)
	}
}

func TestServiceKeepsSystemdForOtherSessions(t *testing.T) {
	executor := &fakeExecutor{batch: execbatch.Batch{Results: []execbatch.Result{
		{SessionID: "linux", Status: execbatch.StatusSuccess, Stdout: "nginx.service loaded active running nginx\n"},
	}}}
	listed, err := New(executor).WithPlatformResolver(platformByPrefix).List(t.Context(), "owner", ListRequest{BatchID: "l", SessionIDs: []string{"linux"}})
	if err != nil {
		t.Fatal(err)
	}
	if executor.requests[0].Command != SystemdListCommand(false) || listed.Hosts[0].Platform != PlatformSystemd {
		t.Fatalf("request = %+v, result = %+v", executor.requests[0], listed)
	}
}

func TestServiceRefusesMixedPlatformBatchesAndWindowsOnlyActionsOnSystemd(t *testing.T) {
	executor := &fakeExecutor{}
	service := New(executor).WithPlatformResolver(platformByPrefix)
	_, err := service.Action(t.Context(), "owner", ActionRequest{BatchID: "m", SessionIDs: []string{"win1", "linux"}, Unit: "x", Action: ActionStop})
	if !errors.Is(err, ErrInvalid) || len(executor.requests) != 0 {
		t.Fatalf("mixed batch error = %v, requests = %d", err, len(executor.requests))
	}
	_, err = service.Action(t.Context(), "owner", ActionRequest{BatchID: "s", SessionIDs: []string{"linux"}, Unit: "x", Action: ActionSetManual})
	if !errors.Is(err, ErrInvalid) || len(executor.requests) != 0 {
		t.Fatalf("set-manual on systemd error = %v", err)
	}
	denied := errors.New("forbidden")
	_, err = New(executor).WithPlatformResolver(func(context.Context, string, string) (string, error) { return "", denied }).
		List(t.Context(), "owner", ListRequest{BatchID: "d", SessionIDs: []string{"win1"}})
	if !errors.Is(err, denied) || len(executor.requests) != 0 {
		t.Fatalf("resolver error = %v", err)
	}
}
