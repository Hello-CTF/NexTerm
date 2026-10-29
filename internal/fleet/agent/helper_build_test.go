//go:build unix

package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	text := strings.Join(describeHelperBuild(build, "Mon Oct 26 09:00:00 2026"), "\n")
	for _, want := range []string{
		"进程=4242", "启动时间=Mon Oct 26 09:00:00 2026", "版本=1.3.0", "持久终端=3",
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
	if text := strings.Join(describeHelperBuild(build, ""), "\n"); strings.Contains(text, "运行环境较早") || strings.Contains(text, "bash") {
		t.Fatalf("an environment-stale helper is replaced silently and prints nothing about shells:\n%s", text)
	}
}
