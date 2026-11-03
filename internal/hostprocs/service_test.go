package hostprocs

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"

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
	mu          sync.Mutex
	events      []AuditEvent
	owners      []string
	ctxErrs     []error
	hasDeadline []bool
	err         error
}

func (r *recAuditor) Audit(ctx context.Context, e AuditEvent) error {
	_, deadline := ctx.Deadline()
	r.mu.Lock()
	r.events = append(r.events, e)
	r.owners = append(r.owners, ipc.OwnerID(ctx))
	r.ctxErrs = append(r.ctxErrs, ctx.Err())
	r.hasDeadline = append(r.hasDeadline, deadline)
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

// posixID is the recheck output of a procps host.
func posixID(ppid int, started, args string) string {
	return sentinelIDPS + "\n" + strconv.Itoa(ppid) + " " + started + "\n" + sentinelIDArgs + "\n" + args + "\n"
}

func busyboxID(ppid int, args string) string {
	return sentinelIDBusybox + "\n" + strconv.Itoa(ppid) + "\n" + args + "\n"
}

func windowsID(name string, ppid int, started string) string {
	return fmt.Sprintf("%s\r\n[{\"name\":%q,\"ppid\":%d,\"started\":%q}]\r\n", sentinelIDWindows, name, ppid, started)
}

const (
	started1 = "Tue Nov 3 07:44:03 2026"
	started2 = "Tue Nov 3 09:00:00 2026"
)

func killReply(idOut string, killExit int) func(string) (base.ExecResult, error) {
	return func(cmd string) (base.ExecResult, error) {
		switch {
		case strings.HasPrefix(cmd, "kill "):
			return base.ExecResult{Stdout: "", Stderr: "kill: denied", ExitCode: exit(killExit)}, nil
		case strings.Contains(cmd, "ps -p"):
			return ok(idOut)
		}
		return base.ExecResult{ExitCode: exit(1)}, nil
	}
}

func killedCommands(r *fakeRunner) []string {
	var out []string
	for _, c := range r.commands() {
		if strings.HasPrefix(c, "kill ") || strings.HasPrefix(c, "taskkill") {
			out = append(out, c)
		}
	}
	return out
}

func ipcCode(err error) ipc.Code {
	var ie *ipc.Error
	if errors.As(err, &ie) {
		return ie.Code
	}
	return ""
}

func TestKillRejectsInvalidInput(t *testing.T) {
	r := &fakeRunner{reply: killReply(posixID(1, started1, "node"), 0)}
	aud := &recAuditor{}
	s := newSvc(r, ShellPOSIX, aud)
	for _, pid := range []int{0, -1, 1, -2147483648, 2147483648, 1 << 40} {
		_, err := s.Kill(context.Background(), ProcessKillArgs{SessionID: "s", PID: pid, Name: "node"})
		if ipcCode(err) != ipc.CodeBadParam {
			t.Errorf("pid %d: %v", pid, err)
		}
	}
	bad := []ProcessKillArgs{
		{SessionID: "s", PID: 5, Name: ""},
		{SessionID: "s", PID: 5, Name: strings.Repeat("n", 1025)},
		{SessionID: "s", PID: 5, Name: "node", Started: strings.Repeat("s", 1025)},
		{SessionID: "s", PID: 5, Name: "node", PPID: -1},
	}
	for i, a := range bad {
		if _, err := s.Kill(context.Background(), a); ipcCode(err) != ipc.CodeBadParam {
			t.Errorf("case %d: %v", i, err)
		}
	}
	// exactly 1 KiB is fine
	if _, err := s.Kill(context.Background(), ProcessKillArgs{SessionID: "s", PID: 5, Name: strings.Repeat("n", 1024), DryRun: true}); err != nil {
		t.Errorf("1 KiB name: %v", err)
	}
	if len(r.commands()) != 0 || len(aud.all()) != 0 {
		t.Errorf("nothing may run or be audited: %v %v", r.commands(), aud.all())
	}
}

func TestKillWindowsPID4RefusedAndAudited(t *testing.T) {
	r := &fakeRunner{reply: func(string) (base.ExecResult, error) { return ok("") }}
	aud := &recAuditor{}
	w := newSvc(r, ShellPowerShell, aud)
	if _, err := w.Kill(context.Background(), ProcessKillArgs{SessionID: "s", PID: 4, Name: "System", DryRun: true}); ipcCode(err) != ipc.CodeBadParam {
		t.Errorf("dry run: %v", err)
	}
	if len(aud.all()) != 0 {
		t.Errorf("dry run is not audited: %+v", aud.all())
	}
	if _, err := w.Kill(context.Background(), ProcessKillArgs{SessionID: "s", PID: 4, Name: "System"}); ipcCode(err) != ipc.CodeBadParam {
		t.Errorf("pid 4: %v", err)
	}
	ev := aud.all()
	if len(ev) != 1 || ev[0].Result != "refused" || ev[0].PID != 4 || ev[0].Name != "System" {
		t.Errorf("audit: %+v", ev)
	}
	if len(r.commands()) != 0 {
		t.Errorf("nothing may run: %v", r.commands())
	}
}

func TestKillDryRunRunsNothing(t *testing.T) {
	r := &fakeRunner{reply: killReply(posixID(1, started1, "node"), 0)}
	aud := &recAuditor{}
	res, err := newSvc(r, ShellPOSIX, aud).Kill(context.Background(), ProcessKillArgs{SessionID: "s", PID: 4242, Name: "node", DryRun: true})
	if err != nil || res.Command != "kill -TERM 4242" || res.Signal != "TERM" || res.Ran {
		t.Fatalf("%+v %v", res, err)
	}
	res, _ = newSvc(r, ShellPOSIX, aud).Kill(context.Background(), ProcessKillArgs{SessionID: "s", PID: 4242, Name: "node", Force: true, DryRun: true})
	if res.Command != "kill -KILL 4242" || res.Signal != "KILL" {
		t.Errorf("%+v", res)
	}
	w := newSvc(r, ShellPowerShell, aud)
	a, _ := w.Kill(context.Background(), ProcessKillArgs{SessionID: "s", PID: 10, Name: "a.exe", DryRun: true})
	b, _ := w.Kill(context.Background(), ProcessKillArgs{SessionID: "s", PID: 10, Name: "a.exe", Force: true, DryRun: true})
	if a.Command != "taskkill /PID 10" || b.Command != "taskkill /PID 10 /F" || a.Signal != "TERM" || b.Signal != "KILL" {
		t.Errorf("%+v %+v", a, b)
	}
	if len(r.commands()) != 0 || len(aud.all()) != 0 {
		t.Errorf("dry run must not run or audit: %v %v", r.commands(), aud.all())
	}
}

func TestKillIdentityMismatchRefuses(t *testing.T) {
	args := ProcessKillArgs{SessionID: "s", PID: 77, Name: "node", PPID: 5, Started: started1}
	cases := map[string]string{
		"other name":         posixID(5, started1, "/usr/bin/other --x"),
		"gone":               "",
		"other ppid":         posixID(6, started1, "node"),
		"other start":        posixID(5, started2, "node"),
		"busybox name":       busyboxID(5, "other"),
		"busybox ppid":       busyboxID(9, "node"),
		"empty args":         posixID(5, started1, ""),
		"garbage":            "something else\n",
		"name prefix only":   posixID(5, started1, "node2 --x"),
		"case differs posix": posixID(5, started1, "Node"),
	}
	for name, out := range cases {
		r := &fakeRunner{reply: killReply(out, 0)}
		aud := &recAuditor{}
		_, err := newSvc(r, ShellPOSIX, aud).Kill(context.Background(), args)
		if ipcCode(err) != ipc.CodeNotFound {
			t.Errorf("%s: err=%v", name, err)
		}
		if len(killedCommands(r)) != 0 {
			t.Errorf("%s: kill ran", name)
		}
		ev := aud.all()
		if len(ev) != 1 || ev[0].Result != "refused" || ev[0].PID != 77 || ev[0].Name != "node" {
			t.Errorf("%s: audit %+v", name, ev)
		}
	}
}

func TestKillIdentityAccepts(t *testing.T) {
	cases := []struct {
		name string
		out  string
		args ProcessKillArgs
	}{
		{"procps", posixID(5, started1, "/usr/bin/node app.js"), ProcessKillArgs{PPID: 5, Started: started1}},
		{"request without start time", posixID(5, started1, "node"), ProcessKillArgs{PPID: 5}},
		{"busybox compares name and ppid only", busyboxID(5, "/usr/bin/node app.js"), ProcessKillArgs{PPID: 5, Started: started1}},
		{"busybox list without start time", busyboxID(5, "node"), ProcessKillArgs{PPID: 5}},
	}
	for _, tc := range cases {
		r := &fakeRunner{reply: killReply(tc.out, 0)}
		a := tc.args
		a.SessionID, a.PID, a.Name = "s", 9, "node"
		res, err := newSvc(r, ShellPOSIX, nil).Kill(context.Background(), a)
		if err != nil || !res.OK || len(killedCommands(r)) != 1 {
			t.Errorf("%s: %+v %v", tc.name, res, err)
		}
	}
}

func TestKillSuccessAndFailureAudit(t *testing.T) {
	for _, tc := range []struct {
		code   int
		ok     bool
		result string
	}{{0, true, "ok"}, {1, false, "failed"}} {
		r := &fakeRunner{reply: killReply(posixID(1, started1, "/usr/bin/node app.js"), tc.code)}
		aud := &recAuditor{}
		ctx := ipc.WithUserID(context.Background(), "owner-1")
		res, err := newSvc(r, ShellPOSIX, aud).Kill(ctx, ProcessKillArgs{SessionID: "s1", PID: 99, Name: "node", PPID: 1, Started: started1, Force: true})
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
		if aud.owners[0] != "owner-1" || !aud.hasDeadline[0] {
			t.Errorf("audit context: owner=%q deadline=%v", aud.owners[0], aud.hasDeadline[0])
		}
		if res.AuditError != "" {
			t.Errorf("audit error: %q", res.AuditError)
		}
	}
}

func TestKillAuditSurvivesCancelledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(ipc.WithUserID(context.Background(), "owner-1"))
	r := &fakeRunner{reply: func(cmd string) (base.ExecResult, error) {
		if strings.HasPrefix(cmd, "kill ") {
			cancel()
			return base.ExecResult{ExitCode: exit(0)}, nil
		}
		return ok(posixID(1, started1, "node"))
	}}
	aud := &recAuditor{}
	if _, err := newSvc(r, ShellPOSIX, aud).Kill(ctx, ProcessKillArgs{SessionID: "s", PID: 99, Name: "node", PPID: 1}); err != nil {
		t.Fatal(err)
	}
	if len(aud.all()) != 1 || aud.ctxErrs[0] != nil || aud.owners[0] != "owner-1" {
		t.Errorf("audit ctx: %+v %v %v", aud.all(), aud.ctxErrs, aud.owners)
	}
}

func TestKillAuditFailureIsVisible(t *testing.T) {
	failing := &recAuditor{err: errors.New("db down")}
	args := ProcessKillArgs{SessionID: "s", PID: 99, Name: "node", PPID: 1}

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	r := &fakeRunner{reply: killReply(posixID(1, started1, "node"), 0)}
	res, err := newSvc(r, ShellPOSIX, failing).WithLogger(logger).Kill(context.Background(), args)
	if err != nil || !res.OK || res.AuditError == "" {
		t.Errorf("%+v %v", res, err)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "db down") {
		t.Errorf("warn log missing: %q", logs.String())
	}

	// refused, recheck error and platform refusals carry the audit failure in the returned error
	refused := &fakeRunner{reply: killReply(posixID(1, started1, "other"), 0)}
	broken := &fakeRunner{reply: func(string) (base.ExecResult, error) { return base.ExecResult{}, errors.New("link down") }}
	for name, svc := range map[string]*Service{
		"refused": newSvc(refused, ShellPOSIX, failing),
		"error":   newSvc(broken, ShellPOSIX, failing),
	} {
		_, err := svc.Kill(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "审计记录写入失败：db down") {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err = newSvc(&fakeRunner{reply: func(string) (base.ExecResult, error) { return ok("") }}, ShellPowerShell, failing).
		Kill(context.Background(), ProcessKillArgs{SessionID: "s", PID: 4, Name: "System"})
	if err == nil || !strings.Contains(err.Error(), "审计记录写入失败：db down") {
		t.Errorf("pid 4: %v", err)
	}
	// no failure, no note
	_, err = newSvc(refused, ShellPOSIX, &recAuditor{}).Kill(context.Background(), args)
	if err == nil || strings.Contains(err.Error(), "审计记录写入失败") {
		t.Errorf("unexpected note: %v", err)
	}
}

func windowsKillReply(idOut string) func(string) (base.ExecResult, error) {
	return func(cmd string) (base.ExecResult, error) {
		if strings.HasPrefix(cmd, "taskkill") {
			return ok("SUCCESS: sent termination signal")
		}
		return ok(idOut)
	}
}

func TestKillWindowsRecheckAndRun(t *testing.T) {
	const started = "2026-11-03T05:00:00.0000000Z"
	r := &fakeRunner{reply: windowsKillReply(windowsID("Chrome.EXE", 600, started))}
	res, err := newSvc(r, ShellPowerShell, nil).Kill(context.Background(), ProcessKillArgs{SessionID: "s", PID: 4120, Name: "chrome.exe", PPID: 600, Started: started})
	if err != nil || !res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	calls := r.commands()
	if len(calls) != 2 || calls[1] != "taskkill /PID 4120" {
		t.Fatalf("%v", calls)
	}
	script := decodePowerShell(t, calls[0])
	if !strings.Contains(script, `ProcessId=4120`) || !strings.Contains(script, "Win32_Process") || strings.Contains(calls[0], "tasklist") {
		t.Errorf("recheck script: %s", script)
	}
}

func TestKillWindowsIdentityMismatchRefuses(t *testing.T) {
	const started = "2026-11-03T05:00:00.0000000Z"
	args := ProcessKillArgs{SessionID: "s", PID: 4120, Name: "chrome.exe", PPID: 600, Started: started}
	for name, out := range map[string]string{
		"other name":     windowsID("firefox.exe", 600, started),
		"prefix of name": windowsID("chrome.exe.bak", 600, started),
		"other ppid":     windowsID("chrome.exe", 601, started),
		"other start":    windowsID("chrome.exe", 600, "2026-11-03T06:00:00.0000000Z"),
		"gone":           sentinelIDWindows + "\r\n[]",
		"wrapper gone":   sentinelIDWindows + "\r\n" + `{"value":[],"Count":0}`,
	} {
		r := &fakeRunner{reply: windowsKillReply(out)}
		aud := &recAuditor{}
		_, err := newSvc(r, ShellPowerShell, aud).Kill(context.Background(), args)
		if ipcCode(err) != ipc.CodeNotFound || len(killedCommands(r)) != 0 {
			t.Errorf("%s: %v %v", name, err, r.commands())
		}
		if ev := aud.all(); len(ev) != 1 || ev[0].Result != "refused" {
			t.Errorf("%s: audit %+v", name, ev)
		}
	}
	// a wrapper-shaped answer for a live process is accepted
	r := &fakeRunner{reply: windowsKillReply(sentinelIDWindows + "\n" + `{"value":{"name":"chrome.exe","ppid":600,"started":"` + started + `"},"Count":1}`)}
	if res, err := newSvc(r, ShellPowerShell, nil).Kill(context.Background(), args); err != nil || !res.OK {
		t.Errorf("wrapper: %+v %v", res, err)
	}
}

func TestKillWindowsCriticalProcessRefused(t *testing.T) {
	for _, real := range []string{"csrss.exe", "LSASS.EXE", "winlogon", "services.exe", "smss.exe", "wininit.exe"} {
		r := &fakeRunner{reply: windowsKillReply(windowsID(real, 4, ""))}
		aud := &recAuditor{}
		// the confirmed name is irrelevant: the real name found by the recheck decides
		_, err := newSvc(r, ShellPowerShell, aud).Kill(context.Background(), ProcessKillArgs{SessionID: "s", PID: 612, Name: real, PPID: 4})
		if ipcCode(err) != ipc.CodeBadParam || len(killedCommands(r)) != 0 {
			t.Errorf("%s: %v %v", real, err, r.commands())
		}
		if ev := aud.all(); len(ev) != 1 || ev[0].Result != "refused" || ev[0].PID != 612 {
			t.Errorf("%s: audit %+v", real, ev)
		}
	}
	r := &fakeRunner{reply: windowsKillReply(windowsID("csrss.exe", 4, ""))}
	_, err := newSvc(r, ShellPowerShell, nil).Kill(context.Background(), ProcessKillArgs{SessionID: "s", PID: 612, Name: "notepad.exe", PPID: 4})
	if ipcCode(err) != ipc.CodeBadParam || len(killedCommands(r)) != 0 {
		t.Errorf("PID reuse by a critical process: %v", err)
	}
}

func TestKillMultibyteNameRoundTrip(t *testing.T) {
	// The listing and the recheck read the same process; both run under one locale prefix.
	table := fixture(t, "ps_procps.txt")
	r := &fakeRunner{reply: func(cmd string) (base.ExecResult, error) {
		switch {
		case strings.HasPrefix(cmd, "if out=$("+posixLocale+"ps -A"):
			return ok(table)
		case strings.Contains(cmd, "ps -p 1200"):
			return ok(posixID(1, "Tue Nov 3 08:05:07 2026", "/opt/数据同步/sync-服务 --x"))
		case strings.HasPrefix(cmd, "kill "):
			return ok("")
		}
		return base.ExecResult{ExitCode: exit(1)}, nil
	}}
	s := newSvc(r, ShellPOSIX, nil)
	list, err := s.List(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	p := byPID(list.Processes)[1200]
	if p.ArgName != "sync-服务" {
		t.Fatalf("list: %+v", p)
	}
	res, err := s.Kill(context.Background(), ProcessKillArgs{SessionID: "s", PID: p.PID, Name: p.ArgName, PPID: p.PPID, Started: p.Started})
	if err != nil || !res.OK {
		t.Fatalf("kill: %+v %v", res, err)
	}
	for _, c := range r.commands() {
		if (strings.Contains(c, "ps -A") || strings.Contains(c, "ps -p")) && strings.Count(c, "ps ") != strings.Count(c, posixLocale+"ps ") {
			t.Errorf("every ps call must run under the shared locale: %s", c)
		}
	}
}

func TestKillUnknownShellProbes(t *testing.T) {
	r := &fakeRunner{reply: func(cmd string) (base.ExecResult, error) {
		switch {
		case strings.HasPrefix(cmd, "printf"):
			return ok(sentinelPrefix + "posix\n")
		case strings.Contains(cmd, "ps -p"):
			return ok(posixID(1, started1, "node"))
		}
		return ok("")
	}}
	res, err := newSvc(r, ShellUnknown, nil).Kill(context.Background(), ProcessKillArgs{SessionID: "s", PID: 9, Name: "node", DryRun: true})
	if err != nil || res.Command != "kill -TERM 9" {
		t.Errorf("%+v %v", res, err)
	}
}

func decodePowerShell(t *testing.T, command string) string {
	t.Helper()
	const prefix = "powershell -NoProfile -NonInteractive -EncodedCommand "
	if !strings.HasPrefix(command, prefix) {
		t.Fatalf("not an encoded PowerShell command: %.60s", command)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(command, prefix))
	if err != nil {
		t.Fatal(err)
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	return string(utf16.Decode(units))
}

func TestWindowsListScriptSkipsAggregateCounters(t *testing.T) {
	script := decodePowerShell(t, windowsListCommand)
	if !strings.Contains(script, "_Total") || !strings.Contains(script, "Idle") {
		t.Errorf("the listing must skip the _Total and Idle perf counters")
	}
}
