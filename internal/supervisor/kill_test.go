//go:build unix

package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/pty"
)

func TestKillCleanupFailureRetry(t *testing.T) {
	supervisor := testSupervisor(t)
	id := ids.New()
	testScriptSessionID(t, supervisor, id, `sleep 30`)
	ctx := context.Background()

	root := sessionsRoot(supervisor.StateDir())
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })

	err := supervisor.Kill(ctx, id)
	if err == nil {
		t.Fatal("kill must fail while artifacts cannot be removed")
	}
	if _, statErr := os.Lstat(sessionDir(supervisor.StateDir(), id)); statErr != nil {
		t.Fatalf("session artifacts must remain after failed cleanup: %v", statErr)
	}
	infos, listErr := supervisor.List(ctx)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(infos) != 1 {
		t.Fatalf("session must stay registered until cleanup succeeds: %+v", infos)
	}

	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Kill(ctx, id); err != nil {
		t.Fatalf("retry after restoring permissions = %v, want successful cleanup", err)
	}
	if _, statErr := os.Lstat(sessionDir(supervisor.StateDir(), id)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("artifacts must be removed after retry: %v", statErr)
	}
	if err := supervisor.Kill(ctx, id); err != nil {
		t.Fatalf("kill after successful cleanup = %v, want nil", err)
	}
}

func TestKillMissingCleansLeftoverArtifacts(t *testing.T) {
	supervisor := testSupervisor(t)
	ctx := context.Background()

	id := ids.New()
	dir := sessionDir(supervisor.StateDir(), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, entryName), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, recordingName), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}

	tombstoneID := ids.New()
	tombstone := tombstonePath(supervisor.StateDir(), tombstoneID)
	if err := os.MkdirAll(tombstone, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tombstone, "junk"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := supervisor.Kill(ctx, id); err != nil {
		t.Fatalf("kill with leftover original artifacts = %v, want cleanup", err)
	}
	if err := supervisor.Kill(ctx, tombstoneID); err != nil {
		t.Fatalf("kill with leftover tombstone = %v, want cleanup", err)
	}
	for _, path := range []string{dir, tombstone} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("leftover artifacts remain at %s: %v", path, err)
		}
	}
	if err := supervisor.Kill(ctx, id); err != nil {
		t.Fatalf("repeated kill after cleanup = %v, want nil", err)
	}
}

func TestKillConcurrentSameID(t *testing.T) {
	supervisor := testSupervisor(t)
	id := ids.New()
	testScriptSessionID(t, supervisor, id, `sleep 30`)
	ctx := context.Background()

	const killers = 8
	var wg sync.WaitGroup
	errs := make([]error, killers)
	for index := 0; index < killers; index++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			errs[slot] = supervisor.Kill(ctx, id)
		}(index)
	}
	wg.Wait()
	for slot, err := range errs {
		if err != nil {
			t.Fatalf("concurrent kill %d = %v", slot, err)
		}
	}
	if _, err := os.Lstat(sessionDir(supervisor.StateDir(), id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("artifacts remain after concurrent kills: %v", err)
	}
}

func TestCreateCloseRaceLeavesNoOrphanRegistry(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	supervisor, err := New(Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	realStart := supervisor.ptyStart
	release := make(chan struct{})
	started := make(chan struct{})
	supervisor.ptyStart = func(ctx context.Context, config pty.Config) (*pty.Session, error) {
		close(started)
		<-release
		return realStart(context.Background(), config)
	}
	t.Cleanup(func() { supervisor.ptyStart = realStart })

	createDone := make(chan error, 1)
	go func() {
		_, err := supervisor.Create(context.Background(), CreateOptions{
			Command: []string{"/bin/sh", "-c", "sleep 30"},
			Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		})
		createDone <- err
	}()
	<-started

	closeDone := make(chan error, 1)
	go func() { closeDone <- supervisor.Close() }()
	time.Sleep(150 * time.Millisecond)
	close(release)

	if err := <-createDone; !errors.Is(err, ErrClosed) {
		t.Fatalf("Create during Close = %v, want ErrClosed", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close = %v", err)
	}

	dirents, err := os.ReadDir(sessionsRoot(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, dirent := range dirents {
		data, readErr := os.ReadFile(filepath.Join(sessionsRoot(stateDir), dirent.Name(), entryName))
		if readErr == nil && strings.Contains(string(data), `"running":true`) {
			t.Fatalf("orphan running registry entry after Create/Close race: %s", data)
		}
	}
	if len(dirents) != 0 {
		t.Fatalf("Create abort must remove artifacts, found %d leftover entries", len(dirents))
	}
}

func TestBlockedWriterUnblockedByKill(t *testing.T) {
	supervisor := testSupervisor(t)
	session := testScriptSession(t, supervisor, `stty raw -echo; printf 'ready\n'; exec sleep 60`)
	attachment, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, attachment, "ready")

	writerDone := make(chan error, 1)
	go func() {
		_, err := attachment.Write(make([]byte, 8*1024*1024))
		writerDone <- err
	}()
	time.Sleep(300 * time.Millisecond)

	killDone := make(chan error, 1)
	go func() { killDone <- supervisor.Kill(context.Background(), session.ID()) }()
	select {
	case err := <-killDone:
		if err != nil {
			t.Fatalf("Kill during blocked writer = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Kill did not complete while a writer was blocked")
	}
	select {
	case err := <-writerDone:
		if err == nil {
			t.Fatal("blocked writer must be woken with an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked writer was not woken by Kill")
	}
}

func TestCloseUnblocksInputPumpWithSlaveHeldOpen(t *testing.T) {
	supervisor, err := New(Config{StateDir: filepath.Join(t.TempDir(), "state"), CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close() })
	session := testScriptSession(t, supervisor, `stty raw -echo; tty; exec sleep 60`)
	attachment, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Close() }()
	slavePath := strings.TrimSpace(string(readUntil(t, attachment, "\n")))
	slave, err := os.OpenFile(slavePath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open slave %q: %v", slavePath, err)
	}
	defer func() { _ = slave.Close() }()

	payload := make([]byte, maxInputChunkLen)
	saturated := false
	for attempt := 0; attempt < inputQueueSlots+2; attempt++ {
		if _, err := session.tryWrite(payload); err != nil {
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("fill input queue: %v", err)
			}
			select {
			case <-session.inputDone:
				t.Fatalf("input closed while filling the queue: %v", err)
			default:
				saturated = true
			}
			break
		}
	}
	if !saturated || len(session.inputQueue) != cap(session.inputQueue) {
		t.Fatalf("input queue did not saturate: len=%d cap=%d", len(session.inputQueue), cap(session.inputQueue))
	}

	if err := supervisor.Close(); err != nil {
		_ = slave.Close()
		pumpsDone := make(chan struct{})
		go func() {
			supervisor.pumps.Wait()
			close(pumpsDone)
		}()
		select {
		case <-pumpsDone:
		case <-time.After(5 * time.Second):
			t.Error("supervisor pumps remained blocked after releasing the slave")
		}
		t.Fatalf("Close with a blocked input pump = %v", err)
	}
	if _, err := session.Write([]byte("after close")); !errors.Is(err, ErrExited) {
		t.Fatalf("Write after Close = %v, want ErrExited", err)
	}
}
