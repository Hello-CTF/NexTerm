package tasks

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"
)

// TestCloseDuringStartupFailure forces Close to start waiting on a task whose
// Start then fails. The startup-failure path must complete the task exactly
// once so Close returns instead of waiting forever.
func TestCloseDuringStartupFailure(t *testing.T) {
	requireShell(t)
	errStart := errors.New("injected start failure")
	starting := make(chan struct{})
	release := make(chan struct{})
	m := testManager(t, func(o *Options) {
		o.Starter = StarterFunc(func(ctx context.Context, cmd Command, output io.Writer) (Process, error) {
			close(starting)
			<-release
			return nil, errStart
		})
	})
	ctx := context.Background()
	gate := filepath.Join(t.TempDir(), "gate")
	runDone := make(chan error, 1)
	go func() {
		_, err := m.Run(ctx, ownerA, gatedCommand(gate, "x", "y"), 0)
		runDone <- err
	}()
	<-starting
	infos, err := m.List(ctx, ownerA)
	if err != nil || len(infos) != 1 {
		t.Fatalf("startup task not registered: %+v err=%v", infos, err)
	}
	id := infos[0].ID
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- m.Close(context.Background())
	}()
	// Wait until Close has transitioned the task, which means it is about
	// to wait on done; only then let Start fail.
	deadline := time.Now().Add(10 * time.Second)
	for {
		m.mu.RLock()
		registered := m.tasks[id]
		m.mu.RUnlock()
		if registered == nil {
			t.Fatal("task vanished before Close transitioned it")
		}
		registered.mu.Lock()
		finished := registered.finished
		registered.mu.Unlock()
		if finished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Close never transitioned the startup task")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close returned an error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close stranded by startup failure")
	}
	select {
	case err := <-runDone:
		if !errors.Is(err, errStart) {
			t.Fatalf("Run should surface the start failure, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after startup failure")
	}
	if names := dirEntries(t, m.dir); len(names) != 0 {
		t.Fatalf("startup failure left garbage: %v", names)
	}
}
