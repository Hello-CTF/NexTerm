package production

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type detectedShellTransport struct {
	*execBatchTestTransport
	shell string
}

func (t detectedShellTransport) DetectedShell() string { return t.shell }

func TestExecBatchTargetExecPerSessionKind(t *testing.T) {
	plain := newExecBatchTestTransport(nil)
	for _, test := range []struct {
		name      string
		kind      string
		options   any
		transport base.Transport
		goos      string
		want      execbatch.TargetExec
	}{
		{name: "winrm", kind: session.KindWinRM, transport: plain, goos: "linux", want: execbatch.ExecPowerShellScript},
		{name: "local on windows", kind: session.KindLocal, transport: plain, goos: "windows", want: execbatch.ExecPowerShellCommand},
		{name: "local on linux", kind: session.KindLocal, transport: plain, goos: "linux", want: execbatch.ExecPOSIX},
		{name: "device without platform", kind: session.KindDevice, transport: plain, goos: "windows", want: execbatch.ExecPOSIX},
		{name: "ssh unknown", kind: session.KindSSH, transport: plain, goos: "windows", want: execbatch.ExecSSH},
		{name: "ssh with powershell option", kind: session.KindSSH, options: map[string]any{"shell": `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`}, transport: plain, goos: "linux", want: execbatch.ExecWindowsSSH},
		{name: "ssh with detected cmd", kind: session.KindSSH, transport: detectedShellTransport{plain, "cmd.exe"}, goos: "linux", want: execbatch.ExecWindowsSSH},
		{name: "ssh with detected bash", kind: session.KindSSH, transport: detectedShellTransport{plain, "bash"}, goos: "linux", want: execbatch.ExecSSH},
	} {
		got := execBatchTargetExec(test.kind, session.Asset{Kind: test.kind, Options: test.options}, test.transport, test.goos)
		if got != test.want {
			t.Errorf("%s: exec = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestExecBatchIPCRunsPowerShellOnWinRMAndReportsShells(t *testing.T) {
	commands := make(chan string, 4)
	transport := newExecBatchTestTransport(func(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
		commands <- command
		code := 0
		return base.ExecResult{ExitCode: &code, Stdout: "ok"}, nil
	})
	manager := newExecBatchTestManager(t, transport)
	owner := ipc.WithUserID(t.Context(), "owner-a")
	winrm, err := manager.ConnectFor(owner, session.Asset{ID: "asset-win", Name: "win", Kind: session.KindWinRM})
	if err != nil {
		t.Fatal(err)
	}
	service := newExecBatchIPCService(manager, execBatchTestAssets(nil), nil)
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}

	shells := decodeExecBatchTest[[]execbatch.TargetCapability](t, dispatchExecBatchTest(dispatcher, owner, "exec_batch_target_shells", map[string]any{"sessionIds": []string{winrm.ID, "missing"}}))
	if len(shells) != 2 || shells[0].Platform != execbatch.PlatformWindows || shells[1].Error == "" {
		t.Fatalf("target shells = %+v", shells)
	}
	supports := map[execbatch.Shell]string{}
	for _, support := range shells[0].Shells {
		supports[support.Shell] = support.Support
	}
	if supports[execbatch.ShellPOSIX] != execbatch.ShellUnsupported || supports[execbatch.ShellPowerShell] != execbatch.ShellSupported || supports[execbatch.ShellCmd] != execbatch.ShellSupported {
		t.Fatalf("winrm shell support = %+v", supports)
	}

	batch := decodeExecBatchTest[execbatch.Batch](t, dispatchExecBatchTest(dispatcher, owner, "exec_batch_execute", execbatch.Request{
		BatchID: "ps", SessionIDs: []string{winrm.ID}, Command: "Get-Date", Shell: execbatch.ShellPowerShell,
	}))
	if batch.Shell != execbatch.ShellPowerShell || len(batch.Results) != 1 || batch.Results[0].Status != execbatch.StatusSuccess {
		t.Fatalf("batch = %+v", batch)
	}
	if command := <-commands; !strings.Contains(command, "\nGet-Date\n") || !strings.Contains(command, "$global:LASTEXITCODE") {
		t.Fatalf("WinRM command = %q, want the PowerShell body around the command", command)
	}

	response := dispatchExecBatchTest(dispatcher, owner, "exec_batch_execute", execbatch.Request{
		BatchID: "bad-shell", SessionIDs: []string{winrm.ID}, Command: "x", Shell: "fish",
	})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("unknown shell = %+v", response)
	}
}
