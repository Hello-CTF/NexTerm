package durable

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

func TestRealTmuxKillIdempotence(t *testing.T) {
	if os.Getenv("NEXTERM_TMUX_INTEGRATION") != "1" {
		t.Skip("real tmux kill idempotence skipped: set NEXTERM_TMUX_INTEGRATION=1 (requires tmux on PATH)")
	}
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatalf("real tmux test opted in but tmux is unavailable: %v", err)
	}
	root := t.TempDir()
	runDir, err := os.MkdirTemp("/tmp", "nx-durable-kill-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	config := Config{
		Binary:         binary,
		SocketPath:     filepath.Join(runDir, "tmux.sock"),
		StateDir:       filepath.Join(root, "state"),
		Namespace:      "real-durable-kill-test",
		CommandTimeout: 3 * time.Second,
	}
	backend, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for iteration := 0; iteration < 25; iteration++ {
		id := ids.New()
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			_ = backend.Kill(cleanupCtx, id)
		})
		command := []string{"/bin/sleep", "30"}
		if iteration%2 == 0 {
			command = []string{"/bin/sh", "-c", "printf 'done\\n'; exit 0"}
		}
		session, err := backend.Create(ctx, CreateOptions{ID: id, Command: command})
		if err != nil {
			t.Fatalf("iteration %d Create: %v", iteration, err)
		}
		if err := session.Detach(); err != nil {
			t.Fatal(err)
		}
		if err := backend.Kill(ctx, id); err != nil {
			t.Fatalf("iteration %d first Kill: %v", iteration, err)
		}
		if err := backend.Kill(ctx, id); err != nil {
			t.Fatalf("iteration %d retry Kill: %v", iteration, err)
		}
		for _, path := range []string{
			backend.sessionDir(id),
			backend.tombstonePath(id),
			backend.recorderDonePath(id),
			backend.recorderDoneTempPath(id),
		} {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("iteration %d artifact remains at %s: %v", iteration, path, err)
			}
		}
		infos, err := backend.List(ctx)
		if err != nil || len(infos) != 0 {
			t.Fatalf("iteration %d live sessions after Kill: infos=%+v err=%v", iteration, infos, err)
		}
	}
	t.Logf("25 immediate Kill/retry iterations preserved retry-safe cleanup postconditions")
}
