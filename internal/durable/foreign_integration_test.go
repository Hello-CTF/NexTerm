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

func TestRealTmuxForeignIsolation(t *testing.T) {
	if os.Getenv("NEXTERM_TMUX_INTEGRATION") != "1" {
		t.Skip("real tmux foreign-isolation skipped: set NEXTERM_TMUX_INTEGRATION=1 (requires tmux on PATH)")
	}
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatalf("real tmux test opted in but tmux is unavailable: %v", err)
	}
	root := t.TempDir()
	runDir, err := os.MkdirTemp("/tmp", "nx-durable-owned-")
	if err != nil {
		t.Fatal(err)
	}
	foreignRunDir, err := os.MkdirTemp("/tmp", "nx-durable-foreign-")
	if err != nil {
		t.Fatal(err)
	}
	foreignSocket := filepath.Join(foreignRunDir, "tmux.sock")
	t.Cleanup(func() {
		_ = os.RemoveAll(runDir)
		_ = os.RemoveAll(foreignRunDir)
	})
	config := Config{
		Binary:         binary,
		SocketPath:     filepath.Join(runDir, "tmux.sock"),
		StateDir:       filepath.Join(root, "state"),
		Namespace:      "real-durable-test",
		CommandTimeout: 3 * time.Second,
	}
	backend, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ownedID := ids.New()
	foreignID := ids.New()
	foreignName := backend.sessionName(foreignID)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = backend.Kill(cleanupCtx, ownedID)
		_ = runRealTmux(cleanupCtx, binary, foreignSocket, "kill-session", "-t", "="+foreignName)
	})
	session, err := backend.Create(ctx, CreateOptions{ID: ownedID, Command: []string{"/bin/sleep", "30"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Detach(); err != nil {
		t.Fatal(err)
	}
	if err := runRealTmux(ctx, binary, foreignSocket, "new-session", "-d", "-s", foreignName, "/bin/sleep 30"); err != nil {
		t.Fatal(err)
	}
	infos, err := backend.List(ctx)
	if err != nil || len(infos) != 1 || infos[0].ID != ownedID || infos[0].Dead {
		t.Fatalf("foreign server changed owned discovery: infos=%+v err=%v", infos, err)
	}
	if err := backend.Kill(ctx, foreignID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign Kill error = %v, want ErrNotFound", err)
	}
	if err := runRealTmux(ctx, binary, foreignSocket, "has-session", "-t", "="+foreignName); err != nil {
		t.Fatalf("foreign session was killed: %v", err)
	}
	if err := backend.Kill(ctx, ownedID); err != nil {
		t.Fatal(err)
	}
	if err := runRealTmux(ctx, binary, foreignSocket, "has-session", "-t", "="+foreignName); err != nil {
		t.Fatalf("owned Kill affected foreign session: %v", err)
	}
}
