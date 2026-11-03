package production

import (
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hostports"
	"github.com/Hello-CTF/NexTerm/internal/hostprocs"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

func TestHostProcsAuditInput(t *testing.T) {
	code := 1
	in := hostProcsAuditInput(hostprocs.AuditEvent{
		SessionID: "s1", AssetID: "a1", Host: "web-1", PID: 42, Name: "node", Signal: "KILL",
		Command: "kill -KILL 42", Result: "failed", ExitCode: &code, Duration: 1500 * time.Millisecond, Output: "denied",
	})
	if in.SessionID == nil || *in.SessionID != "s1" || in.AssetID == nil || *in.AssetID != "a1" {
		t.Fatalf("ids: %+v", in)
	}
	if in.Source != "hosts" || in.Kind != "process_kill" {
		t.Errorf("source/kind: %q %q", in.Source, in.Kind)
	}
	if in.ExitCode == nil || *in.ExitCode != 1 || in.DurationMS == nil || *in.DurationMS != 1500 {
		t.Errorf("exit/duration: %+v", in)
	}
	payload, ok := in.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type %T", in.Payload)
	}
	want := map[string]any{"host": "web-1", "pid": 42, "name": "node", "signal": "KILL", "command": "kill -KILL 42", "result": "failed", "output": "denied"}
	for k, v := range want {
		if payload[k] != v {
			t.Errorf("payload[%s]=%v want %v", k, payload[k], v)
		}
	}
}

func TestHostProcsAuditInputWithoutIDs(t *testing.T) {
	in := hostProcsAuditInput(hostprocs.AuditEvent{PID: 5, Name: "x", Result: "refused"})
	if in.SessionID != nil || in.AssetID != nil || in.ExitCode != nil {
		t.Errorf("empty ids and missing exit code must stay nil: %+v", in)
	}
}

func TestHostProcsShellMapping(t *testing.T) {
	cases := map[hostports.Shell]hostprocs.Shell{
		hostports.ShellPOSIX: hostprocs.ShellPOSIX, hostports.ShellPowerShell: hostprocs.ShellPowerShell, hostports.ShellUnknown: hostprocs.ShellUnknown,
	}
	for in, want := range cases {
		if got := hostProcsShell(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
	if got := hostProcsShell(hostPortsShellForGOOS(session.Asset{Kind: session.KindWinRM}, "linux")); got != hostprocs.ShellPowerShell {
		t.Errorf("winrm: %q", got)
	}
}
