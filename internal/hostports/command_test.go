package hostports

import (
	"context"
	"errors"
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

func TestListCommandMentionsProcessFlagsOnlyWhenAsked(t *testing.T) {
	if !strings.Contains(ListCommand(true), "ss -ltnpH") || strings.Contains(ListCommand(false), "-ltnpH") {
		t.Fatal("process flag selection wrong")
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
