package inventory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWatchStartCancelsInitialSync(t *testing.T) {
	storage := newFakeWatchStore()
	entered := make(chan struct{})
	release := make(chan struct{})
	manager, err := NewWatchManager(storage, func(ctx context.Context, watch SSHWatch) (SSHWatch, error) {
		close(entered)
		select {
		case <-ctx.Done():
			return watch, ctx.Err()
		case <-release:
			return watch, errors.New("released test sync")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(release); _ = manager.Shutdown(context.Background()) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := manager.Start(ctx, "owner", "/config", ""); done <- err }()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("start error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("initial SSH synchronization ignored request cancellation")
	}
	if _, exists := manager.Status("owner"); exists {
		t.Fatal("canceled watch remains active")
	}
	if owners := storage.persistedOwners(); len(owners) != 0 {
		t.Fatalf("canceled watch remains persisted: %v", owners)
	}
}
