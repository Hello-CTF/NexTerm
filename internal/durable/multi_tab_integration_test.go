package durable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

func TestRealTmuxMultiTabSessions(t *testing.T) {
	if os.Getenv("NEXTERM_TMUX_INTEGRATION") != "1" {
		t.Skip("real tmux multi-tab skipped: set NEXTERM_TMUX_INTEGRATION=1 (requires tmux on PATH)")
	}
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatalf("real tmux test opted in but tmux is unavailable: %v", err)
	}
	root := t.TempDir()
	runDir, err := os.MkdirTemp("/tmp", "nx-durable-multitab-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	config := Config{
		Binary:         binary,
		SocketPath:     filepath.Join(runDir, "tmux.sock"),
		StateDir:       filepath.Join(root, "state"),
		Namespace:      "real-durable-multitab-test",
		CommandTimeout: 3 * time.Second,
	}
	backend, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	firstID := ids.New()
	secondID := ids.New()
	foreignID := ids.New()
	foreignName := backend.sessionName(foreignID)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = backend.Kill(cleanupCtx, firstID)
		_ = backend.Kill(cleanupCtx, secondID)
		_ = runRealTmux(cleanupCtx, binary, config.SocketPath, "kill-session", "-t", "="+foreignName)
	})
	taggedScript := func(tag string) []string {
		return []string{"/bin/sh", "-c", fmt.Sprintf(`printf 'boot %s\n'
( i=0; while [ "$i" -lt 40 ]; do printf 'tick-%s\n'; i=$((i+1)); sleep 0.05; done ) &
while IFS= read -r line; do
  printf 'in-%s:%%s\n' "$line"
done`, tag, tag, tag)}
	}
	first, err := backend.Create(ctx, CreateOptions{ID: firstID, Command: taggedScript("alpha"), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	second, err := backend.Create(ctx, CreateOptions{ID: secondID, Command: taggedScript("beta"), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("second Create while first session exists: %v", err)
	}
	firstInitial := first.Info()
	secondInitial := second.Info()
	if firstInitial.SessionID == secondInitial.SessionID || firstInitial.PaneID == secondInitial.PaneID || firstInitial.PID == secondInitial.PID {
		t.Fatalf("durable identities crossed: %+v vs %+v", firstInitial, secondInitial)
	}
	var firstLive, secondLive bytes.Buffer
	if err := readUntil(ctx, first, &firstLive, "boot alpha\r\n"); err != nil {
		t.Fatal(err)
	}
	if err := readUntil(ctx, second, &secondLive, "boot beta\r\n"); err != nil {
		t.Fatal(err)
	}

	tickErrors := make(chan error, 2)
	var readers sync.WaitGroup
	readTicks := func(tag string, session *Session, output *bytes.Buffer, want int) {
		defer readers.Done()
		for strings.Count(output.String(), "tick-"+tag) < want {
			result := make(chan readResult, 1)
			go func() {
				buffer := make([]byte, 4096)
				count, err := session.Read(buffer)
				result <- readResult{data: append([]byte(nil), buffer[:count]...), err: err}
			}()
			select {
			case <-ctx.Done():
				tickErrors <- fmt.Errorf("%s: %w", tag, ctx.Err())
				return
			case current := <-result:
				output.Write(current.data)
				if current.err != nil {
					tickErrors <- fmt.Errorf("%s ended after %d ticks: %w", tag, strings.Count(output.String(), "tick-"+tag), current.err)
					return
				}
			}
		}
	}
	readers.Add(2)
	go readTicks("alpha", first, &firstLive, 20)
	go readTicks("beta", second, &secondLive, 20)
	readers.Wait()
	close(tickErrors)
	for tickErr := range tickErrors {
		t.Error(tickErr)
	}
	if t.Failed() {
		t.Fatal("a durable session was marked exited while both tmux sessions exist")
	}

	if _, err := first.Write([]byte("hello-alpha\r")); err != nil {
		t.Fatal(err)
	}
	if err := readUntil(ctx, first, &firstLive, "in-alpha:hello-alpha\r\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Write([]byte("hello-beta\r")); err != nil {
		t.Fatal(err)
	}
	if err := readUntil(ctx, second, &secondLive, "in-beta:hello-beta\r\n"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(secondLive.String(), "alpha") || strings.Contains(firstLive.String(), "beta") {
		t.Fatalf("identity crossed: first=%q second=%q", firstLive.String(), secondLive.String())
	}

	if err := runRealTmux(ctx, binary, config.SocketPath, "new-session", "-d", "-s", foreignName, "/bin/sleep", "30"); err != nil {
		t.Fatal(err)
	}
	infos, err := backend.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 || infos[0].Dead || infos[1].Dead {
		t.Fatalf("foreign session changed owned discovery: %+v", infos)
	}
	if err := backend.Kill(ctx, foreignID); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("foreign Kill error = %v, want ErrNotOwned", err)
	}
	if err := runRealTmux(ctx, binary, config.SocketPath, "has-session", "-t", "="+foreignName); err != nil {
		t.Fatalf("foreign session was killed: %v", err)
	}

	if err := first.Detach(); err != nil {
		t.Fatal(err)
	}
	if err := second.Detach(); err != nil {
		t.Fatal(err)
	}
	backend, err = New(config)
	if err != nil {
		t.Fatal(err)
	}
	infos, err = backend.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("List after restart = %+v", infos)
	}
	byID := make(map[string]Info, len(infos))
	for _, info := range infos {
		byID[info.ID] = info
	}
	assertSameRealIdentity(t, firstInitial, byID[firstID])
	assertSameRealIdentity(t, secondInitial, byID[secondID])

	reFirst, err := backend.Attach(ctx, firstID)
	if err != nil {
		t.Fatal(err)
	}
	defer reFirst.Detach()
	var firstReplay bytes.Buffer
	if err := readUntil(ctx, reFirst, &firstReplay, "in-alpha:hello-alpha\r\n"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(firstReplay.String(), "beta") {
		t.Fatalf("first replay contains second session output: %q", firstReplay.String())
	}
	reSecond, err := backend.Attach(ctx, secondID)
	if err != nil {
		t.Fatal(err)
	}
	defer reSecond.Detach()
	var secondReplay bytes.Buffer
	if err := readUntil(ctx, reSecond, &secondReplay, "in-beta:hello-beta\r\n"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(secondReplay.String(), "alpha") {
		t.Fatalf("second replay contains first session output: %q", secondReplay.String())
	}
	if _, err := reFirst.Write([]byte("post-restart\r")); err != nil {
		t.Fatal(err)
	}
	if err := readUntil(ctx, reFirst, &firstReplay, "in-alpha:post-restart\r\n"); err != nil {
		t.Fatal(err)
	}

	if err := runRealTmux(ctx, binary, config.SocketPath, "kill-session", "-t", "="+foreignName); err != nil {
		t.Fatal(err)
	}
	if err := backend.Kill(ctx, firstID); err != nil {
		t.Fatal(err)
	}
	if err := backend.Kill(ctx, secondID); err != nil {
		t.Fatal(err)
	}
	infos, err = backend.List(ctx)
	if err != nil || len(infos) != 0 {
		t.Fatalf("Kill left durable sessions: infos=%+v err=%v", infos, err)
	}
	for _, id := range []string{firstID, secondID} {
		if output, err := queryRealTmux(ctx, binary, config.SocketPath, "has-session", "-t", "="+backend.sessionName(id)); err == nil {
			t.Fatalf("tmux session for %s leaked: %s", id, output)
		}
	}
	if output, err := queryRealTmux(ctx, binary, config.SocketPath, "list-sessions"); err == nil ||
		(!strings.Contains(err.Error(), "no server running") && !strings.Contains(err.Error(), "no sessions")) {
		t.Fatalf("tmux sessions remain after cleanup: %q err=%v", output, err)
	}
	entries, err := os.ReadDir(config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("durable artifacts remain after cleanup: %+v", entries)
	}
	t.Logf("two durable sessions coexisted on one socket with stable identities, clean reattach and leak-free cleanup")
}
