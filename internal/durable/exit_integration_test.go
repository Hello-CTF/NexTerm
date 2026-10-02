package durable

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

func TestRealTmuxProcessExit(t *testing.T) {
	if os.Getenv("NEXTERM_TMUX_INTEGRATION") != "1" {
		t.Skip("real tmux process-exit skipped: set NEXTERM_TMUX_INTEGRATION=1 (requires tmux on PATH)")
	}
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatalf("real tmux test opted in but tmux is unavailable: %v", err)
	}
	root := t.TempDir()
	runDir, err := os.MkdirTemp("/tmp", "nx-durable-exit-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	config := Config{
		Binary:         binary,
		SocketPath:     filepath.Join(runDir, "tmux.sock"),
		StateDir:       filepath.Join(root, "state"),
		Namespace:      "real-durable-exit-test",
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
	session, err := backend.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "printf 'before\\n'; sleep 0.05; printf 'final-without-newline'; exit 7"},
	})
	if err != nil {
		t.Fatal(err)
	}
	initial := session.Info()
	liveOutput := readAllBounded(t, ctx, session)
	want := []byte("before\r\nfinal-without-newline")
	if !bytes.Equal(liveOutput, want) {
		t.Fatalf("live exit output = %q, want %q", liveOutput, want)
	}
	infos, err := backend.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || !infos[0].Dead || infos[0].ExitCode == nil || *infos[0].ExitCode != 7 {
		t.Fatalf("exited session info = %+v", infos)
	}
	assertSameRealIdentity(t, initial, infos[0])
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
	deadInfo := session.Info()
	if !deadInfo.Dead || deadInfo.ExitCode == nil || *deadInfo.ExitCode != 7 {
		t.Fatalf("reattached dead info = %+v", deadInfo)
	}
	replay := readAllBounded(t, ctx, session)
	if !bytes.Equal(replay, want) {
		t.Fatalf("dead replay = %q, want exactly one complete copy of %q", replay, want)
	}
	if err := session.Detach(); err != nil {
		t.Fatal(err)
	}
	if err := backend.Kill(ctx, id); err != nil {
		t.Fatal(err)
	}
	t.Logf("dead pane preserved exit status 7 and complete final replay before bounded EOF")
}

type readAllResult struct {
	output []byte
	err    error
}

func readAllBounded(t *testing.T, ctx context.Context, session *Session) []byte {
	t.Helper()
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := make(chan readAllResult, 1)
	go func() {
		output, err := io.ReadAll(session)
		result <- readAllResult{output: output, err: err}
	}()
	select {
	case current := <-result:
		if current.err != nil {
			t.Fatalf("ReadAll error = %v", current.err)
		}
		return current.output
	case <-bounded.Done():
		_ = session.Detach()
		select {
		case <-result:
		case <-time.After(time.Second):
		}
		t.Fatalf("ReadAll did not reach EOF within five seconds: %v", bounded.Err())
		return nil
	}
}
