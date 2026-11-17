package execbatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/platform/psquote"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// recordingTransport records every command it receives and answers through reply.
type recordingTransport struct {
	*fakeTransport
	mu       sync.Mutex
	commands []string
}

func newRecordingTransport(reply func(command string) (base.ExecResult, error)) *recordingTransport {
	transport := &recordingTransport{}
	transport.fakeTransport = newFakeTransport(func(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
		transport.mu.Lock()
		transport.commands = append(transport.commands, command)
		transport.mu.Unlock()
		return reply(command)
	})
	return transport
}

func (t *recordingTransport) sent() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.commands...)
}

func exitReply(code int, stdout string) func(string) (base.ExecResult, error) {
	return func(string) (base.ExecResult, error) {
		return base.ExecResult{ExitCode: &code, Stdout: stdout}, nil
	}
}

// windowsSSHReply answers the platform probe like a Windows OpenSSH host and every other command with success.
func windowsSSHReply(command string) (base.ExecResult, error) {
	code := 0
	if command == windowsProbeCommand {
		return base.ExecResult{ExitCode: &code, Stdout: windowsProbeMarker}, nil
	}
	return base.ExecResult{ExitCode: &code}, nil
}

// linuxSSHReply answers like a POSIX host without PowerShell.
func linuxSSHReply(command string) (base.ExecResult, error) {
	if command == windowsProbeCommand {
		return base.ExecResult{Stderr: "sh: powershell: not found"}, &base.ExitError{Code: 127}
	}
	code := 0
	return base.ExecResult{ExitCode: &code}, nil
}

func execTarget(id string, transport base.Transport, exec TargetExec) Target {
	target := targetFor(id, transport)
	target.Exec = exec
	return target
}

func TestNormalizeShell(t *testing.T) {
	for _, test := range []struct {
		in   Shell
		want Shell
		ok   bool
	}{
		{"", ShellPOSIX, true}, {ShellPOSIX, ShellPOSIX, true}, {ShellPowerShell, ShellPowerShell, true},
		{ShellCmd, ShellCmd, true}, {"bash", "", false}, {"PowerShell", "", false},
	} {
		got, err := NormalizeShell(test.in)
		if got != test.want || (err == nil) != test.ok {
			t.Errorf("NormalizeShell(%q) = %q, %v", test.in, got, err)
		}
		if !test.ok && !errors.Is(err, ErrInvalid) {
			t.Errorf("NormalizeShell(%q) error = %v, want ErrInvalid", test.in, err)
		}
	}
}

func TestShellSupportMatrix(t *testing.T) {
	want := map[TargetExec]map[Shell]string{
		ExecPOSIX:             {ShellPOSIX: ShellSupported, ShellPowerShell: ShellUnsupported, ShellCmd: ShellUnsupported},
		ExecSSH:               {ShellPOSIX: ShellSupported, ShellPowerShell: ShellDetect, ShellCmd: ShellDetect},
		ExecWindowsSSH:        {ShellPOSIX: ShellSupported, ShellPowerShell: ShellSupported, ShellCmd: ShellSupported},
		ExecPowerShellScript:  {ShellPOSIX: ShellUnsupported, ShellPowerShell: ShellSupported, ShellCmd: ShellSupported},
		ExecPowerShellCommand: {ShellPOSIX: ShellUnsupported, ShellPowerShell: ShellSupported, ShellCmd: ShellSupported},
	}
	for exec, shells := range want {
		for shell, support := range shells {
			got := shellSupport(exec, shell)
			if got.Support != support {
				t.Errorf("shellSupport(%q, %q) = %q, want %q", exec, shell, got.Support, support)
			}
			if got.Support != ShellSupported && got.Reason == "" {
				t.Errorf("shellSupport(%q, %q) has no reason", exec, shell)
			}
		}
	}
	if platform := capabilityFor(Target{Exec: ExecSSH}).Platform; platform != PlatformUnknown {
		t.Fatalf("ssh platform = %q", platform)
	}
}

func TestCommandPlanPowerShellForms(t *testing.T) {
	command := `Get-ChildItem 'C:\O''Brien' | Select-Object -First 1`
	script := commandPlan(ShellPowerShell, ExecPowerShellScript, command)
	if script.skip != "" || !strings.Contains(script.command, "\n"+command+"\n") || strings.Contains(script.command, "exit $global:LASTEXITCODE") {
		t.Fatalf("WinRM plan = %+v", script)
	}
	withExit := commandPlan(ShellPowerShell, ExecPowerShellCommand, command)
	if !strings.HasPrefix(withExit.command, script.command) || !strings.HasSuffix(withExit.command, "\nexit $global:LASTEXITCODE") {
		t.Fatalf("-Command plan = %q", withExit.command)
	}
	encoded := commandPlan(ShellPowerShell, ExecWindowsSSH, command)
	if !strings.HasPrefix(encoded.command, "powershell -NoProfile -NonInteractive -EncodedCommand ") {
		t.Fatalf("SSH plan = %q", encoded.command)
	}
	decoded, err := psquote.DecodeCommand(encoded.command)
	if err != nil || decoded != withExit.command {
		t.Fatalf("decoded SSH plan = %q, %v; want the -Command body", decoded, err)
	}
	// The exit code mapping keeps the native exit code and reports cmdlet failures as 1.
	for _, want := range []string{"$global:LASTEXITCODE = $null\n", "if ($?) { $global:LASTEXITCODE = 0 } elseif (-not $global:LASTEXITCODE) { $global:LASTEXITCODE = 1 }"} {
		if !strings.Contains(script.command, want) {
			t.Fatalf("PowerShell body lacks %q:\n%s", want, script.command)
		}
	}
	if posix := commandPlan(ShellPOSIX, ExecPOSIX, "uname -a"); posix.command != "uname -a" {
		t.Fatalf("POSIX plan changed the command: %q", posix.command)
	}
}

func TestCommandPlanCmdQuotesThroughPowerShell(t *testing.T) {
	command := `dir "C:\Program Files" & echo it's 100%`
	plan := commandPlan(ShellCmd, ExecPowerShellCommand, command)
	invocation := "& cmd.exe /d /s /c " + psquote.Quote(" "+command)
	if !strings.Contains(plan.command, invocation) {
		t.Fatalf("cmd plan = %q, want %q", plan.command, invocation)
	}
	literal := strings.TrimPrefix(invocation, "& cmd.exe /d /s /c ")
	if value, ok := psquote.Unquote(literal); !ok || value != " "+command {
		t.Fatalf("cmd literal round trip = %q, %v", value, ok)
	}
	if !strings.Contains(plan.command, "$PSNativeCommandArgumentPassing = 'Legacy'") {
		t.Fatal("cmd plan must pin legacy native argument passing")
	}
	decoded, err := psquote.DecodeCommand(commandPlan(ShellCmd, ExecWindowsSSH, command).command)
	if err != nil || decoded != plan.command {
		t.Fatalf("encoded cmd plan = %q, %v", decoded, err)
	}
}

func TestCommandPlanSkipsCommandsTooLongForWindows(t *testing.T) {
	long := strings.Repeat("a", 4000)
	if plan := commandPlan(ShellPowerShell, ExecWindowsSSH, long); plan.skip == "" || plan.command != "" {
		t.Fatalf("long SSH plan = %+v, want a skip", plan)
	}
	if plan := commandPlan(ShellPowerShell, ExecPowerShellCommand, long); plan.skip != "" {
		t.Fatalf("a 4000-byte -Command plan fits: %+v", plan)
	}
	if plan := commandPlan(ShellPowerShell, ExecPowerShellCommand, strings.Repeat("a", MaxCommandBytes)); plan.skip == "" {
		t.Fatal("a 64 KiB -Command plan exceeds the CreateProcess limit")
	}
}

func TestExecuteSkipsTargetsThatCannotRunTheShell(t *testing.T) {
	database := openExecBatchHistoryStore(t)
	auditor := &fakeAuditor{}
	linux := newRecordingTransport(exitReply(0, "linux"))
	winrm := newRecordingTransport(exitReply(0, "win"))
	resolver := &fakeResolver{targets: map[string]Target{
		"linux": execTarget("linux", linux, ExecPOSIX),
		"winrm": execTarget("winrm", winrm, ExecPowerShellScript),
	}}
	service := New(Config{Resolver: resolver, Auditor: auditor, History: database})

	batch, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "posix-mixed", SessionIDs: []string{"winrm", "linux"}, Command: "uname -a"})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Shell != ShellPOSIX || batch.Results[0].Status != StatusSkipped || batch.Results[0].Error == "" || batch.Results[1].Status != StatusSuccess {
		t.Fatalf("posix batch = %+v", batch)
	}
	if len(winrm.sent()) != 0 || len(linux.sent()) != 1 || linux.sent()[0] != "uname -a" {
		t.Fatalf("sent winrm=%q linux=%q", winrm.sent(), linux.sent())
	}
	progress, targets, ok := service.BatchProgress("owner-a", "posix-mixed")
	if !ok || progress.Skipped != 1 || progress.Succeeded != 1 || progress.Failed != 0 || targets[0].Status != StatusSkipped || targets[0].Error == "" {
		t.Fatalf("progress = %+v %+v", progress, targets)
	}

	history, err := database.ExecBatchHistoryGet(execBatchHistoryContext("owner-a"), "posix-mixed")
	if err != nil {
		t.Fatal(err)
	}
	if history.Batch.Shell != store.ExecBatchShellPOSIX || history.Batch.Status != store.ExecBatchStatusSuccess || history.Batch.TargetCount != 2 {
		t.Fatalf("history batch = %+v", history.Batch)
	}
	if history.Results[0].Status != string(StatusSkipped) || history.Results[0].ErrorKind != store.ExecBatchErrorShellUnsupported {
		t.Fatalf("history skipped row = %+v", history.Results[0])
	}

	batch, err = service.Execute(t.Context(), "owner-a", Request{BatchID: "ps-mixed", SessionIDs: []string{"winrm", "linux"}, Command: "Get-Date", Shell: ShellPowerShell})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Shell != ShellPowerShell || batch.Results[0].Status != StatusSuccess || batch.Results[1].Status != StatusSkipped {
		t.Fatalf("powershell batch = %+v", batch)
	}
	if sent := winrm.sent(); len(sent) != 1 || !strings.Contains(sent[0], "\nGet-Date\n") {
		t.Fatalf("winrm received %q", sent)
	}
	if len(linux.sent()) != 1 {
		t.Fatalf("linux received a PowerShell command: %q", linux.sent())
	}
	history, err = database.ExecBatchHistoryGet(execBatchHistoryContext("owner-a"), "ps-mixed")
	if err != nil {
		t.Fatal(err)
	}
	if history.Batch.Shell != store.ExecBatchShellPowerShell || history.Batch.CommandHash != shellCommandDigest(ShellPowerShell, "Get-Date") {
		t.Fatalf("powershell history = %+v", history.Batch)
	}

	auditor.mu.Lock()
	defer auditor.mu.Unlock()
	if len(auditor.records) != 2 {
		t.Fatalf("audit records = %d, want one per executed target", len(auditor.records))
	}
	for index, want := range []Shell{ShellPOSIX, ShellPowerShell} {
		payload, _ := json.Marshal(auditor.records[index].Payload)
		var decoded struct {
			Shell Shell `json:"shell"`
		}
		_ = json.Unmarshal(payload, &decoded)
		if decoded.Shell != want {
			t.Fatalf("audit %d shell = %q, want %q (%s)", index, decoded.Shell, want, payload)
		}
	}
}

func TestExecuteAllSkippedRunsNothingAndRecordsFailure(t *testing.T) {
	database := openExecBatchHistoryStore(t)
	linux := newRecordingTransport(exitReply(0, ""))
	service := New(Config{Resolver: &fakeResolver{targets: map[string]Target{"linux": execTarget("linux", linux, ExecPOSIX)}}, History: database})
	batch, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "cmd-linux", SessionIDs: []string{"linux"}, Command: "ver", Shell: ShellCmd})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != 1 || batch.Results[0].Status != StatusSkipped || len(linux.sent()) != 0 {
		t.Fatalf("batch = %+v sent = %q", batch, linux.sent())
	}
	history, err := database.ExecBatchHistoryGet(execBatchHistoryContext("owner-a"), "cmd-linux")
	if err != nil {
		t.Fatal(err)
	}
	if history.Batch.Status != store.ExecBatchStatusFailed || history.Batch.Shell != store.ExecBatchShellCmd {
		t.Fatalf("history = %+v", history.Batch)
	}
}

func TestExecuteProbesSSHHostsForWindowsShells(t *testing.T) {
	windows := newRecordingTransport(windowsSSHReply)
	linux := newRecordingTransport(linuxSSHReply)
	resolver := &fakeResolver{targets: map[string]Target{
		"win": execTarget("win", windows, ExecSSH),
		"lin": execTarget("lin", linux, ExecSSH),
	}}
	service := New(Config{Resolver: resolver})
	batch, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "probe", SessionIDs: []string{"win", "lin"}, Command: "ver", Shell: ShellCmd})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Results[0].Status != StatusSuccess || batch.Results[1].Status != StatusSkipped || !strings.Contains(batch.Results[1].Error, "不是 Windows") {
		t.Fatalf("results = %+v", batch.Results)
	}
	sent := windows.sent()
	if len(sent) != 2 || sent[0] != windowsProbeCommand {
		t.Fatalf("windows host received %q, want the probe then the command", sent)
	}
	decoded, err := psquote.DecodeCommand(sent[1])
	if err != nil || !strings.Contains(decoded, "& cmd.exe /d /s /c ' ver'") {
		t.Fatalf("windows command = %q, %v", decoded, err)
	}
	if sent := linux.sent(); len(sent) != 1 || sent[0] != windowsProbeCommand {
		t.Fatalf("linux host received %q, want only the probe", sent)
	}

	// POSIX batches keep sending SSH targets the command as written, without probing.
	batch, err = service.Execute(t.Context(), "owner-a", Request{BatchID: "posix-ssh", SessionIDs: []string{"win"}, Command: "hostname"})
	if err != nil || batch.Results[0].Status != StatusSuccess {
		t.Fatalf("posix batch = %+v, %v", batch, err)
	}
	if sent := windows.sent(); sent[len(sent)-1] != "hostname" {
		t.Fatalf("posix command = %q", sent[len(sent)-1])
	}
}

func TestExecuteRawPowerShellTargetsSendTheCommandUnchanged(t *testing.T) {
	winrm := newRecordingTransport(exitReply(0, ""))
	service := New(Config{Resolver: &fakeResolver{targets: map[string]Target{"w": execTarget("w", winrm, ExecPowerShellScript)}}})
	command := psquote.ShortCommand("Get-Service")
	batch, err := service.Execute(WithPowerShellTargets(t.Context()), "owner-a", Request{BatchID: "raw", SessionIDs: []string{"w"}, Command: command})
	if err != nil || batch.Results[0].Status != StatusSuccess {
		t.Fatalf("raw batch = %+v, %v", batch, err)
	}
	if sent := winrm.sent(); len(sent) != 1 || sent[0] != command {
		t.Fatalf("raw command = %q", sent)
	}
}

func TestProductionChallengeCoversOnlyRunnableTargetsAndBindsShell(t *testing.T) {
	linux := newRecordingTransport(exitReply(0, ""))
	winrm := newRecordingTransport(exitReply(0, ""))
	prodLinux := execTarget("linux", linux, ExecPOSIX)
	prodLinux.Environment = "production"
	prodWin := execTarget("win", winrm, ExecPowerShellScript)
	prodWin.Environment = "production"
	service := New(Config{Resolver: &fakeResolver{targets: map[string]Target{"linux": prodLinux, "win": prodWin}}, Auditor: &confirmationAuditor{}})

	request := Request{BatchID: "prod", SessionIDs: []string{"linux", "win"}, Command: "hostname", Shell: ShellPowerShell}
	_, err := service.Execute(t.Context(), "owner-a", request)
	var required *ConfirmationRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("err = %v, want confirmation required", err)
	}
	if len(required.ProductionTargets) != 1 || required.ProductionTargets[0].SessionID != "win" {
		t.Fatalf("production targets = %+v, want only the target that runs", required.ProductionTargets)
	}
	if required.CommandHash == commandDigest("hostname") {
		t.Fatal("a PowerShell challenge reused the POSIX command hash")
	}

	// A confirmation for the PowerShell batch does not authorize the same text under cmd.
	confirmation := &ProductionConfirmation{Confirmed: true, TargetSetHash: required.TargetSetHash, CommandHash: required.CommandHash, EnvironmentNames: required.RequiredEnvironmentNames}
	cmdRequest := request
	cmdRequest.Shell = ShellCmd
	cmdRequest.ProductionConfirmation = confirmation
	if _, err := service.Execute(t.Context(), "owner-a", cmdRequest); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("cmd with a PowerShell confirmation = %v", err)
	}
	request.ProductionConfirmation = confirmation
	batch, err := service.Execute(t.Context(), "owner-a", request)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Results[0].Status != StatusSkipped || batch.Results[1].Status != StatusSuccess || len(linux.sent()) != 0 {
		t.Fatalf("confirmed batch = %+v", batch.Results)
	}
}

func TestRerunKeepsTheShell(t *testing.T) {
	request, err := RerunRequest(Request{Command: "Get-Date", Shell: ShellPowerShell}, []Result{{SessionID: "a", Status: StatusExitError}, {SessionID: "b", Status: StatusSkipped}}, "rerun-1")
	if err != nil {
		t.Fatal(err)
	}
	if request.Shell != ShellPowerShell || len(request.SessionIDs) != 1 || request.SessionIDs[0] != "a" {
		t.Fatalf("rerun request = %+v, want the shell kept and the skipped target left out", request)
	}
}

func TestTargetCapabilitiesReportsResolveErrors(t *testing.T) {
	resolver := &fakeResolver{
		targets: map[string]Target{"w": execTarget("w", successTransport(), ExecPowerShellCommand)},
		errors:  map[string]error{"gone": ErrDisconnected},
	}
	capabilities, err := New(Config{Resolver: resolver}).TargetCapabilities(t.Context(), []string{"w", "gone"})
	if err != nil {
		t.Fatal(err)
	}
	if len(capabilities) != 2 || capabilities[0].Platform != PlatformWindows || capabilities[1].Error == "" || capabilities[1].Shells == nil {
		t.Fatalf("capabilities = %+v", capabilities)
	}
}
