package hostprocs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type fakeRunner struct {
	mu    sync.Mutex
	calls []string
	reply func(command string) (base.ExecResult, error)
}

func (f *fakeRunner) Exec(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, command)
	f.mu.Unlock()
	return f.reply(command)
}

func (f *fakeRunner) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type recAuditor struct {
	mu     sync.Mutex
	events []AuditEvent
	err    error
}

func (r *recAuditor) Audit(_ context.Context, e AuditEvent) error {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
	return r.err
}

func (r *recAuditor) all() []AuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]AuditEvent(nil), r.events...)
}

func exit(code int) *int { return &code }

func ok(stdout string) (base.ExecResult, error) {
	return base.ExecResult{Stdout: stdout, ExitCode: exit(0)}, nil
}

func newSvc(runner Runner, shell Shell, aud Auditor) *Service {
	return NewService(func(context.Context, string) (Target, error) {
		return Target{Runner: runner, Shell: shell, AssetID: "asset-1", Host: "web-1"}, nil
	}, aud)
}

func TestListUnknownShellFallsBackToWindowsAndCaches(t *testing.T) {
	win := fixtureString(t, "windows_single_nouser.txt")
	r := &fakeRunner{reply: func(cmd string) (base.ExecResult, error) {
		if strings.HasPrefix(cmd, "powershell -NoProfile -NonInteractive -EncodedCommand ") {
			return ok(win)
		}
		return base.ExecResult{Stdout: "'LC_ALL' is not recognized\r\n", ExitCode: exit(1)}, nil
	}}
	s := newSvc(r, ShellUnknown, nil)
	res, err := s.List(context.Background(), "s1")
	if err != nil || res.Platform != "windows" || len(res.Processes) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	before := len(r.commands())
	if _, err := s.List(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if got := r.commands()[before:]; len(got) != 1 || !strings.HasPrefix(got[0], "powershell") {
		t.Errorf("cached windows platform must skip the POSIX script: %v", got)
	}
	s.Forget("s1")
	n := len(r.commands())
	_, _ = s.List(context.Background(), "s1")
	if len(r.commands())-n != 2 {
		t.Errorf("after Forget the POSIX script runs again: %v", r.commands()[n:])
	}
}

func TestListPOSIXShellNeverRunsWindows(t *testing.T) {
	r := &fakeRunner{reply: func(string) (base.ExecResult, error) { return ok("garbage") }}
	_, err := newSvc(r, ShellPOSIX, nil).List(context.Background(), "s1")
	var ie *ipc.Error
	if !errors.As(err, &ie) || ie.Code != ipc.CodeUnsupported || len(r.commands()) != 1 {
		t.Errorf("err=%v calls=%v", err, r.commands())
	}
}

func TestListPOSIXNoPS(t *testing.T) {
	r := &fakeRunner{reply: func(string) (base.ExecResult, error) { return ok(sentinelPrefix + "none\n") }}
	_, err := newSvc(r, ShellPOSIX, nil).List(context.Background(), "s1")
	if err == nil || !strings.Contains(err.Error(), "ps 不可用") {
		t.Errorf("err=%v", err)
	}
}

func TestListWindowsShellRunsOnlyPowerShell(t *testing.T) {
	r := &fakeRunner{reply: func(string) (base.ExecResult, error) { return ok(fixtureString(t, "windows_multi.txt")) }}
	res, err := newSvc(r, ShellPowerShell, nil).List(context.Background(), "s1")
	if err != nil || res.Platform != "windows" || len(r.commands()) != 1 {
		t.Fatalf("%+v %v %v", res, err, r.commands())
	}
}

func fixtureString(t *testing.T, name string) string { return fixture(t, name) }

func killReply(psOut string, killExit int) func(string) (base.ExecResult, error) {
	return func(cmd string) (base.ExecResult, error) {
		switch {
		case strings.HasPrefix(cmd, "kill "):
			return base.ExecResult{Stdout: "", Stderr: "kill: denied", ExitCode: exit(killExit)}, nil
		case strings.Contains(cmd, "ps -p"):
			return ok(psOut)
		}
		return base.ExecResult{ExitCode: exit(1)}, nil
	}
}

func TestKillRejectsInvalidPIDs(t *testing.T) {
	r := &fakeRunner{reply: killReply("node\n", 0)}
	aud := &recAuditor{}
	s := newSvc(r, ShellPOSIX, aud)
	for _, pid := range []int{0, -1, 1, -2147483648, 2147483648, 1 << 40} {
		_, err := s.Kill(context.Background(), KillArgs{SessionID: "s", PID: pid, Name: "node"})
		var ie *ipc.Error
		if !errors.As(err, &ie) || ie.Code != ipc.CodeBadParam {
			t.Errorf("pid %d: %v", pid, err)
		}
	}
	if _, err := s.Kill(context.Background(), KillArgs{SessionID: "s", PID: 5, Name: ""}); err == nil {
		t.Error("empty name accepted")
	}
	w := newSvc(r, ShellPowerShell, aud)
	for _, pid := range []int{0, 4} {
		if _, err := w.Kill(context.Background(), KillArgs{SessionID: "s", PID: pid, Name: "System"}); err == nil {
			t.Errorf("windows pid %d accepted", pid)
		}
	}
	if len(r.commands()) != 0 || len(aud.all()) != 0 {
		t.Errorf("nothing may run or be audited: %v %v", r.commands(), aud.all())
	}
}

func TestKillDryRunRunsNothing(t *testing.T) {
	r := &fakeRunner{reply: killReply("node\n", 0)}
	aud := &recAuditor{}
	res, err := newSvc(r, ShellPOSIX, aud).Kill(context.Background(), KillArgs{SessionID: "s", PID: 4242, Name: "node", DryRun: true})
	if err != nil || res.Command != "kill -TERM 4242" || res.Signal != "TERM" || res.Ran {
		t.Fatalf("%+v %v", res, err)
	}
	res, _ = newSvc(r, ShellPOSIX, aud).Kill(context.Background(), KillArgs{SessionID: "s", PID: 4242, Name: "node", Force: true, DryRun: true})
	if res.Command != "kill -KILL 4242" || res.Signal != "KILL" {
		t.Errorf("%+v", res)
	}
	w := newSvc(r, ShellPowerShell, aud)
	a, _ := w.Kill(context.Background(), KillArgs{SessionID: "s", PID: 10, Name: "a.exe", DryRun: true})
	b, _ := w.Kill(context.Background(), KillArgs{SessionID: "s", PID: 10, Name: "a.exe", Force: true, DryRun: true})
	if a.Command != "taskkill /PID 10" || b.Command != "taskkill /PID 10 /F" || a.Signal != "TERM" || b.Signal != "KILL" {
		t.Errorf("%+v %+v", a, b)
	}
	if len(r.commands()) != 0 || len(aud.all()) != 0 {
		t.Errorf("dry run must not run or audit: %v %v", r.commands(), aud.all())
	}
}

func TestKillNameMismatchRefuses(t *testing.T) {
	for _, psOut := range []string{"/usr/bin/other --x\n", ""} {
		r := &fakeRunner{reply: killReply(psOut, 0)}
		aud := &recAuditor{}
		_, err := newSvc(r, ShellPOSIX, aud).Kill(context.Background(), KillArgs{SessionID: "s", PID: 77, Name: "node"})
		var ie *ipc.Error
		if !errors.As(err, &ie) || ie.Code != ipc.CodeNotFound {
			t.Errorf("err=%v", err)
		}
		for _, c := range r.commands() {
			if strings.HasPrefix(c, "kill ") {
				t.Errorf("kill ran: %q", c)
			}
		}
		ev := aud.all()
		if len(ev) != 1 || ev[0].Result != "refused" || ev[0].PID != 77 || ev[0].Name != "node" {
			t.Errorf("audit: %+v", ev)
		}
	}
}

func TestKillSuccessAndFailureAudit(t *testing.T) {
	for _, tc := range []struct {
		code   int
		ok     bool
		result string
	}{{0, true, "ok"}, {1, false, "failed"}} {
		r := &fakeRunner{reply: killReply("/usr/bin/node app.js\n", tc.code)}
		aud := &recAuditor{}
		res, err := newSvc(r, ShellPOSIX, aud).Kill(context.Background(), KillArgs{SessionID: "s1", PID: 99, Name: "node", Force: true})
		if err != nil || !res.Ran || res.OK != tc.ok || res.ExitCode == nil || *res.ExitCode != tc.code || res.Command != "kill -KILL 99" {
			t.Fatalf("%+v %v", res, err)
		}
		ev := aud.all()
		if len(ev) != 1 {
			t.Fatalf("audit: %+v", ev)
		}
		e := ev[0]
		if e.Result != tc.result || e.SessionID != "s1" || e.AssetID != "asset-1" || e.Host != "web-1" || e.PID != 99 || e.Name != "node" || e.Signal != "KILL" || e.Command != "kill -KILL 99" || e.ExitCode == nil || *e.ExitCode != tc.code {
			t.Errorf("event: %+v", e)
		}
		if res.AuditError != "" {
			t.Errorf("audit error: %q", res.AuditError)
		}
	}
}

func TestKillAuditFailureIsVisible(t *testing.T) {
	r := &fakeRunner{reply: killReply("node\n", 0)}
	res, err := newSvc(r, ShellPOSIX, &recAuditor{err: errors.New("db down")}).Kill(context.Background(), KillArgs{SessionID: "s", PID: 99, Name: "node"})
	if err != nil || !res.OK || res.AuditError == "" {
		t.Errorf("%+v %v", res, err)
	}
}

func TestKillWindowsRecheckAndRun(t *testing.T) {
	r := &fakeRunner{reply: func(cmd string) (base.ExecResult, error) {
		if strings.HasPrefix(cmd, "tasklist") {
			return ok("\"Chrome.exe\",\"4120\",\"Console\",\"1\",\"100 K\"\r\n")
		}
		return ok("SUCCESS: sent termination signal")
	}}
	res, err := newSvc(r, ShellPowerShell, nil).Kill(context.Background(), KillArgs{SessionID: "s", PID: 4120, Name: "chrome.exe"})
	if err != nil || !res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	calls := r.commands()
	if len(calls) != 2 || calls[0] != `tasklist /FI "PID eq 4120" /FO CSV /NH` || calls[1] != "taskkill /PID 4120" {
		t.Errorf("%v", calls)
	}
}

func TestKillUnknownShellProbes(t *testing.T) {
	r := &fakeRunner{reply: func(cmd string) (base.ExecResult, error) {
		switch {
		case strings.HasPrefix(cmd, "printf"):
			return ok(sentinelPrefix + "posix\n")
		case strings.Contains(cmd, "ps -p"):
			return ok("node\n")
		}
		return ok("")
	}}
	res, err := newSvc(r, ShellUnknown, nil).Kill(context.Background(), KillArgs{SessionID: "s", PID: 9, Name: "node", DryRun: true})
	if err != nil || res.Command != "kill -TERM 9" {
		t.Errorf("%+v %v", res, err)
	}
}
