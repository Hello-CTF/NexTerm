package production

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/hostports"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestHostPortsShellSelection(t *testing.T) {
	cases := []struct {
		name  string
		asset session.Asset
		goos  string
		want  hostports.Shell
	}{
		{"winrm", session.Asset{Kind: session.KindWinRM}, "linux", hostports.ShellPowerShell},
		{"local linux", session.Asset{Kind: session.KindLocal}, "linux", hostports.ShellPOSIX},
		{"local windows", session.Asset{Kind: session.KindLocal}, "windows", hostports.ShellPowerShell},
		{"device defaults to posix", session.Asset{Kind: session.KindDevice}, "windows", hostports.ShellPOSIX},
		{"ssh unknown", session.Asset{Kind: session.KindSSH}, "linux", hostports.ShellUnknown},
		{"ssh pwsh", session.Asset{Kind: session.KindSSH, Options: map[string]any{"shell": `C:\Program Files\PowerShell\7\pwsh.exe`}}, "linux", hostports.ShellPowerShell},
		{"ssh bash", session.Asset{Kind: session.KindSSH, Options: map[string]any{"shell": "/bin/bash"}}, "linux", hostports.ShellUnknown},
	}
	for _, tc := range cases {
		if got := hostPortsShellForGOOS(tc.asset, tc.goos); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestTerminalOutputAge(t *testing.T) {
	tabs := []session.TabInfo{
		{SessionID: "a", LastOutputAt: 10, LastOutputMSAgo: 5000},
		{SessionID: "a", LastOutputAt: 20, LastOutputMSAgo: 800},
		{SessionID: "a"},
		{SessionID: "b", LastOutputAt: 30, LastOutputMSAgo: 1},
	}
	if age, ok := terminalOutputAge(tabs, "a"); !ok || age != 800*time.Millisecond {
		t.Fatalf("age = %v %v", age, ok)
	}
	if _, ok := terminalOutputAge(tabs, "c"); ok {
		t.Fatal("unknown session reported an age")
	}
	if _, ok := terminalOutputAge([]session.TabInfo{{SessionID: "a"}}, "a"); ok {
		t.Fatal("tab without output reported an age")
	}
}

type recordingAuditor struct {
	inputs []store.AuditInput
	ctxs   []context.Context
}

func (r *recordingAuditor) AuditInsert(ctx context.Context, input store.AuditInput) error {
	r.inputs = append(r.inputs, input)
	r.ctxs = append(r.ctxs, ctx)
	return nil
}

type auditRunner struct{ result base.ExecResult }

func (r auditRunner) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return r.result, nil
}

func TestHostPortsProbeAuditRecordsExecutionOnly(t *testing.T) {
	zero := 0
	auditor := &recordingAuditor{}
	service := hostports.NewService(func(context.Context, string) (hostports.Target, error) {
		return hostports.Target{Runner: auditRunner{base.ExecResult{Stdout: "ok", ExitCode: &zero}}, Shell: hostports.ShellPOSIX, AssetID: "asset-9"}, nil
	})
	service.SetAudit(hostPortsAudit(auditor))
	ctx := ipc.WithUserID(t.Context(), "user-7")
	req := hostports.ProbeRequest{Method: "http", URL: "http://example.com/?a=1&b=2"}

	if _, err := service.Probe(ctx, hostports.ProbeArgs{SessionID: "s1", ProbeRequest: req, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if len(auditor.inputs) != 0 {
		t.Fatalf("dry run audited: %+v", auditor.inputs)
	}
	result, err := service.Probe(ctx, hostports.ProbeArgs{SessionID: "s1", ProbeRequest: req})
	if err != nil || len(auditor.inputs) != 1 {
		t.Fatalf("err=%v inputs=%d", err, len(auditor.inputs))
	}
	in := auditor.inputs[0]
	if in.SessionID == nil || *in.SessionID != "s1" || in.AssetID == nil || *in.AssetID != "asset-9" ||
		in.Kind != "port_probe" || in.Source != "user" || in.ExitCode == nil || *in.ExitCode != 0 || in.DurationMS == nil {
		t.Fatalf("input = %+v", in)
	}
	if got := ipc.OwnerID(auditor.ctxs[0]); got != "user-7" {
		t.Fatalf("audit owner = %q", got)
	}
	raw, err := json.Marshal(in.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["command"] != result.Command || payload["method"] != "http" || payload["url"] != req.URL || payload["ok"] != true {
		t.Fatalf("payload = %v", payload)
	}
}

func TestForgetHostPortsOnStatus(t *testing.T) {
	var forgotten []string
	forget := func(id string) { forgotten = append(forgotten, id) }
	for _, status := range []session.Status{session.StatusConnected, session.StatusConnecting} {
		forgetHostPortsOnStatus(forget, session.StatusEvent{SessionID: "s", Status: status})
	}
	if len(forgotten) != 0 {
		t.Fatalf("forgot a live session: %v", forgotten)
	}
	forgetHostPortsOnStatus(forget, session.StatusEvent{SessionID: "s", Status: session.StatusDisconnected})
	forgetHostPortsOnStatus(nil, session.StatusEvent{SessionID: "s", Status: session.StatusFailed})
	if len(forgotten) != 1 || forgotten[0] != "s" {
		t.Fatalf("forgotten = %v", forgotten)
	}
}

type failingAuditor struct{}

func (failingAuditor) AuditInsert(context.Context, store.AuditInput) error {
	return errors.New("disk full")
}

func TestHostPortsAuditTruncatesOnRuneBoundary(t *testing.T) {
	zero := 0
	auditor := &recordingAuditor{}
	// The 512-byte cut falls inside a three-byte character.
	stdout := "a" + strings.Repeat("好", 400)
	service := hostports.NewService(func(context.Context, string) (hostports.Target, error) {
		return hostports.Target{Runner: auditRunner{base.ExecResult{Stdout: stdout, ExitCode: &zero}}, Shell: hostports.ShellPOSIX}, nil
	})
	service.SetAudit(hostPortsAudit(auditor))
	req := hostports.ProbeRequest{Method: "tcp", Host: "example.com", Port: 22}
	if _, err := service.Probe(t.Context(), hostports.ProbeArgs{SessionID: "s", ProbeRequest: req}); err != nil || len(auditor.inputs) != 1 {
		t.Fatalf("err=%v inputs=%d", err, len(auditor.inputs))
	}
	payload, ok := auditor.inputs[0].Payload.(hostPortsAuditPayload)
	if !ok || !utf8.ValidString(payload.Output) || payload.Output == "" || len(payload.Output) > maxHostPortsAuditOutput {
		t.Fatalf("output valid=%v len=%d", utf8.ValidString(payload.Output), len(payload.Output))
	}
}

func TestHostPortsAuditRecordsScriptForCmdHosts(t *testing.T) {
	zero := 0
	auditor := &recordingAuditor{}
	service := hostports.NewService(func(context.Context, string) (hostports.Target, error) {
		return hostports.Target{Runner: auditRunner{base.ExecResult{ExitCode: &zero}}, Shell: hostports.ShellUnknown}, nil
	})
	service.SetAudit(hostPortsAudit(auditor))
	req := hostports.ProbeRequest{Method: "tcp", Host: "example.com", Port: 22}
	result, err := service.Probe(t.Context(), hostports.ProbeArgs{SessionID: "s", ProbeRequest: req})
	if err != nil || result.Script == "" || len(auditor.inputs) != 1 {
		t.Fatalf("err=%v script=%q inputs=%d", err, result.Script, len(auditor.inputs))
	}
	payload, ok := auditor.inputs[0].Payload.(hostPortsAuditPayload)
	if !ok || payload.Script != result.Script {
		t.Fatalf("payload = %+v", auditor.inputs[0].Payload)
	}
}

func TestHostPortsAuditFailureIsLogged(t *testing.T) {
	zero := 0
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	service := hostports.NewService(func(context.Context, string) (hostports.Target, error) {
		return hostports.Target{Runner: auditRunner{base.ExecResult{Stdout: "ok", ExitCode: &zero}}, Shell: hostports.ShellPOSIX}, nil
	})
	service.SetAudit(hostPortsAuditWithLogger(failingAuditor{}, logger))
	req := hostports.ProbeRequest{Method: "tcp", Host: "example.com", Port: 22}
	result, err := service.Probe(t.Context(), hostports.ProbeArgs{SessionID: "s", ProbeRequest: req})
	if err != nil || !result.OK {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	logged := buf.String()
	if !strings.Contains(logged, "level=WARN") || !strings.Contains(logged, "disk full") {
		t.Fatalf("log = %q", logged)
	}
}
