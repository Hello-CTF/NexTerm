package durable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

func TestRealTmuxRestartReattach(t *testing.T) {
	if os.Getenv("NEXTERM_TMUX_INTEGRATION") != "1" {
		t.Skip("real tmux restart/reattach skipped: set NEXTERM_TMUX_INTEGRATION=1 (requires tmux on PATH)")
	}
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatalf("real tmux test opted in but tmux is unavailable: %v", err)
	}
	root := t.TempDir()
	runDir, err := os.MkdirTemp("/tmp", "nx-durable-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
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
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	id := ids.New()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = backend.Kill(cleanupCtx, id)
	})
	script := `printf 'boot\n'
while IFS= read -r line; do
  case "$line" in
    later) sleep 0.2; printf 'detached\n' ;;
    burst) i=0; while [ "$i" -lt 20 ]; do printf 'tick:%02d\n' "$i"; i=$((i+1)); sleep 0.02; done ;;
    size) stty size ;;
    *) printf 'in:%s\n' "$line" ;;
  esac
done
`
	session, err := backend.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", script},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatal(err)
	}
	initial := session.Info()
	var initialOutput bytes.Buffer
	if err := readUntil(ctx, session, &initialOutput, "boot\r\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Write([]byte("later\r")); err != nil {
		t.Fatal(err)
	}
	if err := readUntil(ctx, session, &initialOutput, "later\r\n"); err != nil {
		t.Fatal(err)
	}
	if err := session.Detach(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)

	backend, err = New(config)
	if err != nil {
		t.Fatal(err)
	}
	infos, err := backend.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("List after restart = %+v", infos)
	}
	assertSameRealIdentity(t, initial, infos[0])
	session, err = backend.Attach(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var replay bytes.Buffer
	if err := readUntil(ctx, session, &replay, "detached\r\n"); err != nil {
		t.Fatal(err)
	}
	if strings.Count(replay.String(), "boot\r\n") != 1 || strings.Count(replay.String(), "detached\r\n") != 1 {
		t.Fatalf("replay is not continuous and unique:\n%q", replay.String())
	}
	if _, err := session.Write([]byte("fresh 世界\r")); err != nil {
		t.Fatal(err)
	}
	if err := readUntil(ctx, session, &replay, "in:fresh 世界\r\n"); err != nil {
		t.Fatal(err)
	}

	if _, err := session.Write([]byte("burst\r")); err != nil {
		t.Fatal(err)
	}
	if err := readUntil(ctx, session, &replay, "burst\r\n"); err != nil {
		t.Fatal(err)
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
	var racing bytes.Buffer
	if err := readUntil(ctx, session, &racing, "tick:19\r\n"); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 20; index++ {
		marker := fmt.Sprintf("tick:%02d\r\n", index)
		if strings.Count(racing.String(), marker) != 1 {
			t.Fatalf("%s count is not one in continuous output:\n%q", marker, racing.String())
		}
		if index > 0 {
			previous := fmt.Sprintf("tick:%02d\r\n", index-1)
			if strings.Index(racing.String(), previous) > strings.Index(racing.String(), marker) {
				t.Fatalf("ticks out of order: %s before %s", marker, previous)
			}
		}
	}

	if err := session.Resize(ctx, 132, 43); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Write([]byte("size\r")); err != nil {
		t.Fatal(err)
	}
	if err := readUntil(ctx, session, &racing, "43 132\r\n"); err != nil {
		t.Fatal(err)
	}
	if err := session.Detach(); err != nil {
		t.Fatal(err)
	}
	infos, err = backend.List(ctx)
	if err != nil || len(infos) != 1 {
		t.Fatalf("Detach affected durable session: infos=%+v err=%v", infos, err)
	}
	assertSameRealIdentity(t, initial, infos[0])
	if infos[0].Dead {
		t.Fatalf("durable process exited after Detach: %+v", infos[0])
	}
	t.Logf("reattached %s across backend restarts with unchanged pane PID %d and continuous old/new output", id, initial.PID)
	if err := backend.Kill(ctx, id); err != nil {
		t.Fatal(err)
	}
	infos, err = backend.List(ctx)
	if err != nil || len(infos) != 0 {
		t.Fatalf("owned Kill left sessions: infos=%+v err=%v", infos, err)
	}
}

func assertSameRealIdentity(t *testing.T, expected, actual Info) {
	t.Helper()
	if !sameIdentity(expected, actual) {
		t.Fatalf("identity changed:\nfirst  %+v\nsecond %+v", expected, actual)
	}
}

type readResult struct {
	data []byte
	err  error
}

func readUntil(ctx context.Context, session *Session, output *bytes.Buffer, marker string) error {
	for !strings.Contains(output.String(), marker) {
		result := make(chan readResult, 1)
		go func() {
			buffer := make([]byte, 4096)
			count, err := session.Read(buffer)
			result <- readResult{data: append([]byte(nil), buffer[:count]...), err: err}
		}()
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %q in %q: %w", marker, output.String(), ctx.Err())
		case current := <-result:
			output.Write(current.data)
			if current.err != nil && !errors.Is(current.err, io.EOF) {
				return current.err
			}
			if len(current.data) == 0 && errors.Is(current.err, io.EOF) {
				return io.EOF
			}
		}
	}
	return nil
}

func runRealTmux(ctx context.Context, binary, socket string, args ...string) error {
	_, err := queryRealTmux(ctx, binary, socket, args...)
	return err
}

func queryRealTmux(ctx context.Context, binary, socket string, args ...string) (string, error) {
	commandArgs := []string{"-f", os.DevNull, "-S", socket}
	commandArgs = append(commandArgs, args...)
	cmd := exec.CommandContext(ctx, binary, commandArgs...)
	cmd.Env = cleanEnvironment(os.Environ())
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("tmux %s: %w: %s", args[0], err, output)
	}
	return string(output), nil
}
