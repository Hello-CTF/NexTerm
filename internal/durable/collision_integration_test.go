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

func TestRealTmuxLiteralCompletionCollision(t *testing.T) {
	if os.Getenv("NEXTERM_TMUX_INTEGRATION") != "1" {
		t.Skip("real tmux literal-completion collision skipped: set NEXTERM_TMUX_INTEGRATION=1 (requires tmux on PATH)")
	}
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatalf("real tmux test opted in but tmux is unavailable: %v", err)
	}
	root := t.TempDir()
	runDir, err := os.MkdirTemp("/tmp", "nx-durable-collision-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	config := Config{
		Binary:         binary,
		SocketPath:     filepath.Join(runDir, "tmux.sock"),
		StateDir:       filepath.Join(root, "state"),
		Namespace:      "real-durable-collision-test",
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
	marker := legacyCompletionMarker(id)
	payload := append([]byte("before-"), marker...)
	payload = append(payload, []byte("-middle-")...)
	payload = append(payload, marker...)
	payload = append(payload, []byte("-after")...)
	payloadPath := filepath.Join(root, "payload")
	if err := os.WriteFile(payloadPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	finish := filepath.Join(root, "finish")
	quotedPayload := shellQuote(payloadPath)
	script := "/bin/dd if=" + quotedPayload + " bs=1 count=5 2>/dev/null; sleep 0.05; " +
		"/bin/dd if=" + quotedPayload + " bs=1 skip=5 count=17 2>/dev/null; sleep 0.05; " +
		"/bin/dd if=" + quotedPayload + " bs=1 skip=22 2>/dev/null; " +
		"while [ ! -f " + shellQuote(finish) + " ]; do sleep 0.05; done; exit 0"
	session, err := backend.Create(ctx, CreateOptions{ID: id, Command: []string{"/bin/sh", "-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	live := readFullBounded(t, ctx, session, len(payload))
	if !bytes.Equal(live, payload) {
		t.Fatalf("live literal collision output = %q, want %q", live, payload)
	}
	infos, err := backend.List(ctx)
	if err != nil || len(infos) != 1 || infos[0].Dead {
		t.Fatalf("literal collision changed live process state: infos=%+v err=%v", infos, err)
	}
	if err := os.WriteFile(finish, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if remaining := readAllBounded(t, ctx, session); len(remaining) != 0 {
		t.Fatalf("unexpected output after literal payload: %q", remaining)
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
	if !bytes.Equal(replay, payload) {
		t.Fatalf("fresh literal collision replay = %q, want exactly one complete copy of %q", replay, payload)
	}
	if err := session.Detach(); err != nil {
		t.Fatal(err)
	}
	if err := backend.Kill(ctx, id); err != nil {
		t.Fatal(err)
	}
	t.Logf("two full legacy markers and fragmented surrounding bytes round-tripped live and after fresh dead replay")
}

type readFullResult struct {
	output []byte
	err    error
}

func readFullBounded(t *testing.T, ctx context.Context, session *Session, size int) []byte {
	t.Helper()
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := make(chan readFullResult, 1)
	go func() {
		output := make([]byte, size)
		_, err := io.ReadFull(session, output)
		result <- readFullResult{output: output, err: err}
	}()
	select {
	case current := <-result:
		if current.err != nil {
			t.Fatalf("ReadFull error = %v", current.err)
		}
		return current.output
	case <-bounded.Done():
		_ = session.Detach()
		select {
		case <-result:
		case <-time.After(time.Second):
		}
		t.Fatalf("ReadFull did not complete within five seconds: %v", bounded.Err())
		return nil
	}
}
