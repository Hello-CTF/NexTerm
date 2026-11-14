//go:build unix

package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

func TestDescribeHelperBuildReportsStaleHelperWithSessions(t *testing.T) {
	build := supervisor.HelperBuild{
		Helper: supervisor.RunningHelper{Protocol: supervisor.ProtocolVersion, PID: 4242, ActivityKnown: true,
			Activity: supervisor.ActivitySnapshot{PTY: 3},
			Runtime:  &supervisor.RuntimeMetadata{Version: "1.3.0", SHA256: strings.Repeat("b", 64)}},
		Local: supervisor.RuntimeMetadata{Version: "1.3.2", SHA256: strings.Repeat("a", 64)},
		Stale: true, Sessions: 3, SessionsKnown: true,
	}
	text := strings.Join(describeHelperBuildIn(build, "Mon Oct 26 09:00:00 2026", false, true), "\n")
	for _, want := range []string{
		"组件进程：4242，启动于 Mon Oct 26 09:00:00 2026", "版本 1.3.0", "运行中的持久终端 3 个",
		"主机端组件版本较旧（1.3.0，当前 1.3.2），仍有 3 个持久终端在运行；全部结束后将自动更新。",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("status lacks %q:\n%s", want, text)
		}
	}
}

func TestBridgePipeMakesTheHostAuthoritativeForTheShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix shell")
	}
	// The helper of this test process is the stale one: it was started with the installer's bash and a
	// minimal PATH.
	t.Setenv("SHELL", "/bin/bash")
	t.Setenv("PATH", "/usr/bin:/bin")
	shell := filepath.Join(t.TempDir(), "zsh")
	script := "#!/bin/sh\nprintf 'shell=%s path=%s args=%s\\n' \"$SHELL\" \"$PATH\" \"$*\"\n"
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := bridgeCreateDefaults
	bridgeCreateDefaults = func() supervisor.CreateDefaults {
		return supervisor.CreateDefaults{
			Shell: func(string) (string, bool) { return shell, true },
			Path:  func() string { return "/opt/homebrew/bin:/usr/bin:/bin" },
		}
	}
	t.Cleanup(func() { bridgeCreateDefaults = previous })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	harness := newBridgeHarness(t, ctx)
	// The request is what a desktop sends for a device terminal: no command, no SHELL, no PATH.
	created, err := harness.client().Create(ctx, supervisor.CreateOptions{Env: []string{"TERM=xterm-256color"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := harness.client().Attach(ctx, created.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	output := readUntilMarker(t, attachment, "args=-l")
	if !strings.Contains(output, "shell="+shell) || !strings.Contains(output, "path=/opt/homebrew/bin:/usr/bin:/bin") {
		t.Fatalf("session output = %q, want the host's shell and PATH instead of the helper's", output)
	}
	_ = attachment.Close()
}

func TestDescribeHelperBuildEnvStaleSaysNothing(t *testing.T) {
	build := supervisor.HelperBuild{
		Helper: supervisor.RunningHelper{Protocol: supervisor.ProtocolVersion, PID: 7, ActivityKnown: true,
			Activity: supervisor.ActivitySnapshot{PTY: 2}},
		Stale: true, StaleReason: "env", StartShell: "/bin/bash", ExpectedShell: "/bin/zsh", Sessions: 2, SessionsKnown: true,
	}
	if text := strings.Join(describeHelperBuild(build), "\n"); strings.Contains(text, "运行环境较早") || strings.Contains(text, "bash") {
		t.Fatalf("an environment-stale helper is replaced silently and prints nothing about shells:\n%s", text)
	}
}

func TestDescribeHelperBuildEnvStaleInSharedDirTellsTheUser(t *testing.T) {
	build := supervisor.HelperBuild{
		Helper: supervisor.RunningHelper{Protocol: supervisor.ProtocolVersion, PID: 7, SpawnedBy: supervisor.SpawnedByDesktop},
		Stale:  true, StaleReason: "env",
	}
	notice := supervisor.HelperBuild{EnvKept: true}.Notice()
	if text := strings.Join(describeHelperBuildIn(build, "", true, false), "\n"); !strings.Contains(text, "提示: "+notice) {
		t.Fatalf("shared dir status lacks the notice:\n%s", text)
	}
	if text := strings.Join(describeHelperBuildIn(build, "", false, false), "\n"); strings.Contains(text, "提示") {
		t.Fatalf("non-shared dir status must stay silent:\n%s", text)
	}
}

func TestDescribeHelperBuildKeepsEnvStaleNoticeOnlyForDesktopSpawnedHelpers(t *testing.T) {
	for _, spawnedBy := range []string{supervisor.SpawnedByAgent, ""} {
		build := supervisor.HelperBuild{
			Helper: supervisor.RunningHelper{Protocol: supervisor.ProtocolVersion, PID: 7, SpawnedBy: spawnedBy},
			Stale:  true, StaleReason: "env",
		}
		if text := strings.Join(describeHelperBuildIn(build, "", true, false), "\n"); strings.Contains(text, "提示") {
			t.Fatalf("helper spawned by %q is replaced, so no keep notice is due:\n%s", spawnedBy, text)
		}
	}
}

type countingHandler struct {
	mu      sync.Mutex
	records []string
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *countingHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record.Level.String()+" "+record.Message)
	return nil
}
func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(string) slog.Handler      { return h }
func (h *countingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.records)
}

func TestCheckHelperBuildLogsTheSameNoticeOncePerHelper(t *testing.T) {
	handler := &countingHandler{}
	agentRuntime := newStateRuntime(t, Options{DataDir: t.TempDir()})
	agentRuntime.logger = slog.New(handler)
	pid := 7
	agentRuntime.checkBuild = func(context.Context) (supervisor.HelperBuild, error) {
		return supervisor.HelperBuild{Helper: supervisor.RunningHelper{PID: pid}, EnvKept: true}, nil
	}
	agentRuntime.checkHelperBuild(context.Background())
	agentRuntime.checkHelperBuild(context.Background())
	if got := handler.count(); got != 1 {
		t.Fatalf("log records after two checks of one helper = %d, want 1", got)
	}
	pid = 8
	agentRuntime.checkHelperBuild(context.Background())
	agentRuntime.checkHelperBuild(context.Background())
	if got := handler.count(); got != 2 {
		t.Fatalf("log records after the helper changed = %d, want 2", got)
	}
}

func TestHelperBuildLoopRechecksSoonWhileAStaleHelperWaitsForItsTerminals(t *testing.T) {
	agentRuntime := newStateRuntime(t, Options{DataDir: t.TempDir()})
	agentRuntime.desired.TerminalEnabled = true
	agentRuntime.helperBuildEvery = time.Hour
	agentRuntime.helperBuildWaitingEvery = time.Millisecond
	checks := make(chan int, 8)
	count := 0
	agentRuntime.checkBuild = func(context.Context) (supervisor.HelperBuild, error) {
		count++
		checks <- count
		if count < 3 {
			// An older build that still runs one terminal.
			return supervisor.HelperBuild{Helper: supervisor.RunningHelper{PID: 9}, Stale: true, StaleReason: "build", Sessions: 1, SessionsKnown: true}, nil
		}
		return supervisor.HelperBuild{Stale: true, Replaced: true}, nil
	}
	// The startup check found the waiting helper.
	agentRuntime.checkHelperBuild(context.Background())
	<-checks
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		agentRuntime.helperBuildLoop(ctx)
	}()
	deadline := time.After(10 * time.Second)
	for want := 2; want <= 3; want++ {
		select {
		case got := <-checks:
			if got != want {
				t.Fatalf("check %d, want %d", got, want)
			}
		case <-deadline:
			t.Fatalf("check %d did not run while the stale helper waited", want)
		}
	}
	// Once replaced the loop is back on the long interval.
	select {
	case got := <-checks:
		t.Fatalf("check %d ran after the helper was replaced", got)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	<-done
}

func TestDescribeHelperBuildKeepsProcessDetailsForVerbose(t *testing.T) {
	build := supervisor.HelperBuild{
		Helper:        supervisor.RunningHelper{Protocol: supervisor.ProtocolVersion, PID: 31, Runtime: &supervisor.RuntimeMetadata{Version: "1.4.3"}},
		SessionsKnown: true,
	}
	plain := strings.Join(describeHelperBuild(build), "\n")
	if plain != "持久终端组件：正常，版本 1.4.3，运行中的持久终端 0 个" {
		t.Fatalf("default status = %q", plain)
	}
	if verbose := strings.Join(describeHelperBuildIn(build, "", false, true), "\n"); !strings.Contains(verbose, "组件进程：31") {
		t.Fatalf("verbose status lacks the process line:\n%s", verbose)
	}
}
