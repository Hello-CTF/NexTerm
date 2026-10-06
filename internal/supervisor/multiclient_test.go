//go:build unix

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

// TestRemoteHelperTwoClientsShareSession proves the multi-client contract on a
// real helper process: two independent remote clients (separately dialed
// connections, as two machines bridging over their own SSH exec channels)
// attach to the same session concurrently, interleave detach and reattach, and
// observe one shared session identity with replay, fan-out, resize and kill
// intact.
func TestRemoteHelperTwoClientsShareSession(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	_, socketPath := startHelperSupervisor(t, dir)
	digest, err := stateDigestFor(dir)
	if err != nil {
		t.Fatal(err)
	}
	dialA := func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}
	dialB := func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}
	clientA := NewRemoteClient(dialA, digest)
	clientB := NewRemoteClient(dialB, digest)
	ctx := context.Background()

	id := ids.New()
	infoA, err := clientA.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		Cols:    80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	streamA, err := clientA.Attach(ctx, id, &Identity{CreatedAt: infoA.CreatedAt, Incarnation: infoA.Incarnation})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := streamA.Write([]byte("echo mc-boot-''x\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, streamA, "mc-boot-x")

	if _, err := clientB.Create(ctx, CreateOptions{ID: id, Cols: 80, Rows: 24}); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("second client create = %v; want ErrAlreadyExists without a duplicate session", err)
	}
	infos, err := clientB.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != id {
		t.Fatalf("second client listed %+v; want exactly the shared session", infos)
	}
	if !infos[0].CreatedAt.Equal(infoA.CreatedAt) || infos[0].Incarnation != infoA.Incarnation {
		t.Fatalf("second client sees identity %+v; want %+v", infos[0], infoA)
	}

	streamB, err := clientB.Attach(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	replay := readUntil(t, streamB, "mc-boot-x")
	if count := countOccurrences(string(replay), "mc-boot-x"); count != 1 {
		t.Fatalf("late attach replayed the boot marker %d times: %q", count, replay)
	}

	if _, err := streamA.Write([]byte("echo mc-a1-''x\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, streamA, "mc-a1-x")
	readUntil(t, streamB, "mc-a1-x")
	if _, err := streamB.Write([]byte("echo mc-b1-''x\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, streamB, "mc-b1-x")
	readUntil(t, streamA, "mc-b1-x")

	if err := streamA.Close(); err != nil {
		t.Fatal(err)
	}
	infos, err = clientB.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != id || infos[0].Dead {
		t.Fatalf("detach killed the session: %+v", infos)
	}
	if _, err := streamB.Write([]byte("echo mc-detach-''x\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, streamB, "mc-detach-x")

	streamA2, err := clientA.Attach(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	replay = readUntil(t, streamA2, "mc-detach-x")
	assertOrderedOnce(t, string(replay), "mc-boot-x", "mc-a1-x", "mc-b1-x", "mc-detach-x")

	if err := streamB.Resize(ctx, 100, 30); err != nil {
		t.Fatal(err)
	}
	infos, err = clientA.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Cols != 100 || infos[0].Rows != 30 {
		t.Fatalf("grid after the peer resize = %+v; want 100x30", infos)
	}

	const burst = 20
	var writers sync.WaitGroup
	writers.Add(1)
	go func() {
		defer writers.Done()
		for index := 0; index < burst; index++ {
			command := fmt.Sprintf("echo mc-burst-%d-''x\n", index)
			if _, err := streamB.Write([]byte(command)); err != nil {
				t.Errorf("burst write %d: %v", index, err)
				return
			}
		}
	}()
	readers := make([]*Stream, 2)
	readers[0], readers[1] = streamA2, streamB
	seen := make([][]byte, 2)
	var readerWG sync.WaitGroup
	for index, stream := range readers {
		readerWG.Add(1)
		go func(index int, stream *Stream) {
			defer readerWG.Done()
			seen[index] = readUntil(t, stream, "mc-burst-19-x")
		}(index, stream)
	}
	writers.Wait()
	readerWG.Wait()
	for index, data := range seen {
		for marker := 0; marker < burst; marker++ {
			if count := countOccurrences(string(data), fmt.Sprintf("mc-burst-%d-x", marker)); count != 1 {
				t.Fatalf("stream %d saw burst marker %d %d times: %q", index, marker, count, data)
			}
		}
	}

	if err := streamA2.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	infos, err = clientB.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("session survived a kill: %+v", infos)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var exitErr *base.ExitError
	if err := streamB.Wait(waitCtx); !errors.As(err, &exitErr) || exitErr.Signal != "SIGKILL" {
		t.Fatalf("peer Wait after kill = %v; want a SIGKILL exit error", err)
	}
	if err := streamB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := streamA2.Close(); err != nil {
		t.Fatal(err)
	}
}
