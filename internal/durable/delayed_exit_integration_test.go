package durable

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

func TestRealTmuxProcessExitDelayedRecorder(t *testing.T) {
	if os.Getenv("NEXTERM_TMUX_INTEGRATION") != "1" {
		t.Skip("real tmux delayed-recorder exit skipped: set NEXTERM_TMUX_INTEGRATION=1 (requires tmux on PATH and /bin/kill)")
	}
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatalf("real tmux test opted in but tmux is unavailable: %v", err)
	}
	root := t.TempDir()
	runDir, err := os.MkdirTemp("/tmp", "nx-durable-delay-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	config := Config{
		Binary:         binary,
		SocketPath:     filepath.Join(runDir, "tmux.sock"),
		StateDir:       filepath.Join(root, "state"),
		Namespace:      "real-durable-delay-test",
		CommandTimeout: 3 * time.Second,
	}
	backend, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id := ids.New()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = backend.Kill(cleanupCtx, id)
	})
	fire := filepath.Join(root, "fire")
	script := "while [ ! -f " + shellQuote(fire) + " ]; do sleep 0.05; done; printf 'delayed-final'; exit 7"
	session, err := backend.Create(ctx, CreateOptions{ID: id, Command: []string{"/bin/sh", "-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	pipeText, err := queryRealTmux(ctx, binary, config.SocketPath, "display-message", "-p", "-t", session.Info().PaneID, "#{pane_pipe_pid}")
	if err != nil {
		t.Fatal(err)
	}
	pipePID, err := strconv.Atoi(strings.TrimSpace(pipeText))
	if err != nil || pipePID <= 0 {
		t.Fatalf("invalid recorder PID %q: %v", pipeText, err)
	}
	if err := signalRecorder(ctx, pipePID, "-STOP"); err != nil {
		t.Fatal(err)
	}
	resumed := false
	t.Cleanup(func() {
		if !resumed {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			_ = signalRecorder(cleanupCtx, pipePID, "-CONT")
		}
	})
	if err := os.WriteFile(fire, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info := waitForDeadExit(t, ctx, backend, id)
	if info.ExitCode == nil || *info.ExitCode != 7 {
		t.Fatalf("delayed exit info = %+v", info)
	}
	state, err := queryRealTmux(ctx, binary, config.SocketPath, "display-message", "-p", "-t", info.PaneID, "#{pane_dead} #{pane_pipe}")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(state) != "1 1" {
		t.Fatalf("delayed fixture requires a retained dead active pipe, got %q", state)
	}
	if size := recordingSize(t, backend, id); size != 0 {
		t.Fatalf("stopped recorder wrote %d bytes before release", size)
	}

	result := make(chan readAllResult, 1)
	go func() {
		output, err := io.ReadAll(session)
		result <- readAllResult{output: output, err: err}
	}()
	time.Sleep(700 * time.Millisecond)
	select {
	case current := <-result:
		t.Fatalf("Read completed during the stopped-recorder quiet window: output=%q err=%v", current.output, current.err)
	default:
	}
	if size := recordingSize(t, backend, id); size != 0 {
		t.Fatalf("stopped recorder wrote %d bytes during the quiet window", size)
	}
	if err := signalRecorder(ctx, pipePID, "-CONT"); err != nil {
		t.Fatal(err)
	}
	resumed = true
	bounded, boundedCancel := context.WithTimeout(ctx, 5*time.Second)
	defer boundedCancel()
	select {
	case current := <-result:
		if current.err != nil {
			t.Fatalf("delayed ReadAll error = %v", current.err)
		}
		if string(current.output) != "delayed-final" {
			t.Fatalf("delayed output = %q, want %q", current.output, "delayed-final")
		}
	case <-bounded.Done():
		_ = session.Detach()
		t.Fatalf("delayed completion did not reach EOF after recorder release: %v", bounded.Err())
	}
	if err := session.Detach(); err != nil {
		t.Fatal(err)
	}

	backend, err = New(config)
	if err != nil {
		t.Fatal(err)
	}
	session, err = backend.Attach(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	replay := readAllBounded(t, ctx, session)
	if string(replay) != "delayed-final" {
		t.Fatalf("delayed dead replay = %q, want %q", replay, "delayed-final")
	}
	if err := session.Detach(); err != nil {
		t.Fatal(err)
	}
	if err := backend.Kill(ctx, id); err != nil {
		t.Fatal(err)
	}
	t.Logf("recorder released after a dead 700ms empty window; ordered completion preserved all 13 final bytes before EOF and replay")
}

func waitForDeadExit(t *testing.T, ctx context.Context, backend *Backend, id string) Info {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		infos, err := backend.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(infos) == 1 && infos[0].Dead {
			return infos[0]
		}
		select {
		case <-ctx.Done():
			t.Fatalf("session %s did not exit: %v", id, ctx.Err())
			return Info{}
		case <-ticker.C:
		}
	}
}

func recordingSize(t *testing.T, backend *Backend, id string) int64 {
	t.Helper()
	info, err := os.Stat(backend.recordingPath(id))
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func signalRecorder(ctx context.Context, pid int, signal string) error {
	cmd := exec.CommandContext(ctx, "/bin/kill", signal, strconv.Itoa(pid))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("kill %s %d: %w: %s", signal, pid, err, output)
	}
	return nil
}
