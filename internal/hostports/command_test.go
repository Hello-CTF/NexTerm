package hostports

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestParseListOutputReadsSentinel(t *testing.T) {
	out := "@@nexterm-ports ss\n" + fixture(t, "ss_ltnpH.txt")
	listeners, format, err := ParseListOutput(out)
	if err != nil || format != FormatSS || len(listeners) == 0 {
		t.Fatalf("got %v %q %v", listeners, format, err)
	}
	if _, _, err := ParseListOutput("sh: ss: not found\n"); !errors.Is(err, ErrNoSentinel) {
		t.Fatalf("missing sentinel err = %v", err)
	}
	if _, _, err := ParseListOutput("@@nexterm-ports none\n"); err == nil || errors.Is(err, ErrNoSentinel) {
		t.Fatalf("none err = %v", err)
	}
	if _, _, err := ParseListOutput("@@nexterm-ports bogus\nx"); err == nil {
		t.Fatal("bogus format accepted")
	}
}

// runListScript runs the generated script under /bin/sh with only the named fake tools on PATH. Each fake
// prints its name, its arguments and the LC_ALL it was started with.
func runListScript(t *testing.T, withProcess bool, uname string, tools ...string) string {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("uname", "echo "+uname)
	for _, tool := range tools {
		write(tool, "echo \"tool="+tool+" args=$* LC_ALL=$LC_ALL\"")
	}
	cmd := exec.Command("/bin/sh", "-c", ListCommand(withProcess))
	cmd.Env = []string{"PATH=" + dir}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return string(out)
}

func TestListCommandRunsTheExpectedTool(t *testing.T) {
	cases := []struct {
		name        string
		withProcess bool
		uname       string
		tools       []string
		want        []string
	}{
		{"ss", false, "Linux", []string{"ss", "netstat"}, []string{"@@nexterm-ports ss\n", "tool=ss args=-ltnH LC_ALL=C"}},
		{"ss with processes", true, "Linux", []string{"ss", "netstat"}, []string{"@@nexterm-ports ss\n", "tool=ss args=-ltnpH LC_ALL=C"}},
		{"only netstat", false, "Linux", []string{"netstat"}, []string{"@@nexterm-ports netstat-linux\n", "tool=netstat args=-ltn LC_ALL=C"}},
		{"only netstat with processes", true, "Linux", []string{"netstat"}, []string{"@@nexterm-ports netstat-linux\n", "tool=netstat args=-ltnp LC_ALL=C"}},
		{"macOS netstat", false, "Darwin", []string{"netstat", "lsof"}, []string{"@@nexterm-ports netstat-bsd\n", "tool=netstat args=-anv -p tcp LC_ALL=C"}},
		{"macOS lsof", true, "Darwin", []string{"netstat", "lsof"}, []string{"@@nexterm-ports lsof\n", "tool=lsof args=-nP -iTCP -sTCP:LISTEN"}},
		{"nothing", false, "Linux", nil, []string{"@@nexterm-ports none\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := runListScript(t, tc.withProcess, tc.uname, tc.tools...)
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Fatalf("output lacks %q:\n%s", want, out)
				}
			}
			if strings.Count(out, sentinelPrefix) != 1 {
				t.Fatalf("expected exactly one sentinel:\n%s", out)
			}
		})
	}
}

// fakeRunner answers by predicate and records every command.
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

func (f *fakeRunner) reset() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

func windowsHost(t *testing.T) *fakeRunner {
	en := fixture(t, "netstat_windows_en.txt")
	tasks := fixture(t, "tasklist_csv.txt")
	return &fakeRunner{reply: func(command string) (base.ExecResult, error) {
		switch {
		case strings.HasPrefix(command, "netstat -ano -p tcpv6"):
			return base.ExecResult{}, nil
		case strings.HasPrefix(command, "netstat -ano"):
			return base.ExecResult{Stdout: en}, nil
		case strings.HasPrefix(command, "tasklist"):
			return base.ExecResult{Stdout: tasks}, nil
		}
		zero := 1
		return base.ExecResult{Stderr: "'sh' is not recognized", ExitCode: &zero}, nil
	}}
}

func resolverFor(target Target) Resolver {
	return func(context.Context, string) (Target, error) { return target, nil }
}

func TestServiceFallsBackToWindowsAndCachesPlatform(t *testing.T) {
	runner := windowsHost(t)
	service := NewService(resolverFor(Target{Runner: runner, Shell: ShellUnknown}))
	result, err := service.List(t.Context(), "s1", true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Platform != "windows" || result.Format != string(FormatNetstatWindows) || len(result.Listeners) == 0 {
		t.Fatalf("result = %+v", result)
	}
	var named bool
	for _, l := range result.Listeners {
		named = named || l.Process == "postgres.exe"
	}
	if !named {
		t.Fatalf("no process names: %+v", result.Listeners)
	}
	first := runner.commands()
	if !strings.Contains(first[0], "@@nexterm-ports") {
		t.Fatalf("POSIX probe must run first: %v", first)
	}
	runner.reset()
	if _, err := service.List(t.Context(), "s1", false); err != nil {
		t.Fatal(err)
	}
	for _, c := range runner.commands() {
		if strings.Contains(c, "@@nexterm-ports") {
			t.Fatalf("cached platform still probes POSIX: %v", runner.commands())
		}
	}
	service.Forget("s1")
	runner.reset()
	if _, err := service.List(t.Context(), "s1", false); err != nil {
		t.Fatal(err)
	}
	if c := runner.commands(); len(c) == 0 || !strings.Contains(c[0], "@@nexterm-ports") {
		t.Fatalf("Forget did not clear the cache: %v", c)
	}
}

func TestServicePOSIXHost(t *testing.T) {
	runner := &fakeRunner{reply: func(string) (base.ExecResult, error) {
		return base.ExecResult{Stdout: "@@nexterm-ports ss\n" + fixture(t, "ss_ltnpH.txt")}, nil
	}}
	service := NewService(resolverFor(Target{Runner: runner, Shell: ShellUnknown}))
	result, err := service.List(t.Context(), "s", true)
	if err != nil || result.Platform != "posix" || result.Format != "ss" {
		t.Fatalf("%+v %v", result, err)
	}
	if n := len(runner.commands()); n != 1 {
		t.Fatalf("commands = %d", n)
	}
}

func TestServiceListSkipsWhileTerminalIsActive(t *testing.T) {
	runner := &fakeRunner{reply: func(string) (base.ExecResult, error) {
		return base.ExecResult{Stdout: "@@nexterm-ports ss\n" + fixture(t, "ss_ltnH.txt")}, nil
	}}
	ago := 500 * time.Millisecond
	known := true
	target := Target{Runner: runner, Shell: ShellPOSIX, LastOutputAgo: func() (time.Duration, bool) { return ago, known }}
	service := NewService(resolverFor(target))
	result, err := service.ListWithQuiet(t.Context(), "s", false, 2*time.Second)
	if err != nil || !result.Skipped || len(runner.commands()) != 0 {
		t.Fatalf("active terminal: %+v %v calls=%v", result, err, runner.commands())
	}
	ago = 3 * time.Second
	result, err = service.ListWithQuiet(t.Context(), "s", false, 2*time.Second)
	if err != nil || result.Skipped || len(result.Listeners) == 0 || len(runner.commands()) != 1 {
		t.Fatalf("quiet terminal: %+v %v", result, err)
	}
	known = false
	ago = 0
	runner.reset()
	if result, err = service.ListWithQuiet(t.Context(), "s", false, 2*time.Second); err != nil || result.Skipped || len(runner.commands()) != 1 {
		t.Fatalf("unknown output time must not skip: %+v %v", result, err)
	}
	ago = 0
	known = true
	runner.reset()
	if result, err = service.ListWithQuiet(t.Context(), "s", false, 0); err != nil || result.Skipped {
		t.Fatalf("quiet 0 must not skip: %+v %v", result, err)
	}
}

func TestServiceListErrors(t *testing.T) {
	service := NewService(func(context.Context, string) (Target, error) { return Target{}, errors.New("denied") })
	if _, err := service.List(t.Context(), "s", false); err == nil {
		t.Fatal("resolver error swallowed")
	}
	runner := &fakeRunner{reply: func(string) (base.ExecResult, error) { return base.ExecResult{Stdout: "nothing useful"}, nil }}
	service = NewService(resolverFor(Target{Runner: runner, Shell: ShellUnknown}))
	if _, err := service.List(t.Context(), "s", false); err == nil {
		t.Fatal("unrecognized host produced no error")
	}
}

func TestServiceDropsPossiblyCutLastLineWhenTruncated(t *testing.T) {
	out := "@@nexterm-ports ss\nLISTEN 0 128 0.0.0.0:22 0.0.0.0:*\nLISTEN 0 128 0.0.0.0:80 0.0.0.0:*\nLISTEN 0 128 0.0.0.0:8 0.0.0.0:*"
	for _, truncated := range []bool{false, true} {
		runner := &fakeRunner{reply: func(string) (base.ExecResult, error) {
			return base.ExecResult{Stdout: out, Truncated: truncated}, nil
		}}
		result, err := NewService(resolverFor(Target{Runner: runner, Shell: ShellPOSIX})).List(t.Context(), "s", false)
		if err != nil {
			t.Fatal(err)
		}
		ports := map[uint16]bool{}
		for _, l := range result.Listeners {
			ports[l.Port] = true
		}
		if truncated {
			if !result.Truncated || ports[8] || !ports[22] || !ports[80] {
				t.Fatalf("truncated: %+v", result)
			}
		} else if result.Truncated || !ports[8] {
			t.Fatalf("complete output must be kept whole: %+v", result)
		}
	}
	// Dropping must not remove the sentinel itself.
	runner := &fakeRunner{reply: func(string) (base.ExecResult, error) {
		return base.ExecResult{Stdout: "@@nexterm-ports ss\n", Truncated: true}, nil
	}}
	result, err := NewService(resolverFor(Target{Runner: runner, Shell: ShellPOSIX})).List(t.Context(), "s", false)
	if err != nil || !result.Truncated || result.Format != "ss" {
		t.Fatalf("sentinel-only output: %+v %v", result, err)
	}
}
