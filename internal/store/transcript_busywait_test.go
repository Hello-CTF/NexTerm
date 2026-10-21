package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// The busy-timeout fixture must reach every pooled connection: the default
// stays at the 30s production value, a custom duration is applied verbatim on
// file and in-memory DSNs, and a negative value is rejected.
func TestStoreBusyTimeoutOptionConfiguresPragma(t *testing.T) {
	ctx := context.Background()
	busyTimeout := func(t *testing.T, s *Store) int64 {
		t.Helper()
		var ms int64
		if err := s.DB().QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&ms); err != nil {
			t.Fatal(err)
		}
		return ms
	}

	standard, err := Open(ctx, filepath.Join(t.TempDir(), "default.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = standard.Close() })
	if got := busyTimeout(t, standard); got != defaultBusyTimeoutMS {
		t.Fatalf("default busy_timeout = %d, want %d", got, defaultBusyTimeoutMS)
	}

	configured, err := OpenWithOptions(ctx, filepath.Join(t.TempDir(), "configured.db"), OpenOptions{BusyTimeout: 1250 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configured.Close() })
	if got := busyTimeout(t, configured); got != 1250 {
		t.Fatalf("configured busy_timeout = %d, want 1250", got)
	}

	memory, err := OpenInMemoryWithOptions(ctx, OpenOptions{BusyTimeout: 750 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memory.Close() })
	if got := busyTimeout(t, memory); got != 750 {
		t.Fatalf("in-memory busy_timeout = %d, want 750", got)
	}

	if _, err := OpenWithOptions(ctx, filepath.Join(t.TempDir(), "negative.db"), OpenOptions{BusyTimeout: -time.Second}); err == nil {
		t.Fatal("a negative busy timeout must be rejected")
	}
	if _, err := OpenInMemoryWithOptions(ctx, OpenOptions{BusyTimeout: -time.Second}); err == nil {
		t.Fatal("a negative in-memory busy timeout must be rejected")
	}
}

// Documents the modernc busy-handler limitation M91 proved and this change
// designs around rather than fixes: once a statement enters the busy wait,
// cancelling its context does not interrupt it, and closing the pool neither
// interrupts it nor joins it — the wait ends only when busy_timeout expires.
// The fixture keeps that boundary at 2s so the assertions stay fast while the
// production default remains 30s.
func TestStoreSQLiteBusyWaitOutlivesCancelAndClose(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	database, err := OpenWithOptions(ctx, path, OpenOptions{BusyTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	locker, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locker.Close() })
	lockConn, err := locker.DB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockConn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	unlock := func() {
		if _, err := lockConn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
			t.Error(err)
		}
		if err := lockConn.Close(); err != nil {
			t.Error(err)
		}
	}

	// A contended write keeps waiting past its context's cancellation and
	// fails only when the 2s busy timeout expires.
	cancelCtx, cancel := context.WithCancel(ctx)
	cancelled := make(chan error, 1)
	go func() {
		cancelled <- database.TranscriptStart(cancelCtx, TranscriptRow{SessionID: "busy-cancel", StartedAt: 1})
	}()
	time.Sleep(300 * time.Millisecond) // the write is inside the busy wait
	cancel()
	select {
	case err := <-cancelled:
		t.Fatalf("ctx cancellation interrupted the busy wait after 300ms: %v", err)
	case <-time.After(400 * time.Millisecond):
	}
	select {
	case err := <-cancelled:
		if !IsBusy(err) {
			t.Fatalf("busy wait must expire with a busy error, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the busy wait never honoured busy_timeout")
	}

	// Closing the pool returns without joining the in-flight busy wait, and
	// the wait still runs to busy_timeout instead of being interrupted.
	inflight := make(chan error, 1)
	go func() {
		inflight <- database.TranscriptStart(ctx, TranscriptRow{SessionID: "busy-close", StartedAt: 2})
	}()
	time.Sleep(300 * time.Millisecond) // the write is inside the busy wait
	closed := make(chan error, 1)
	go func() { closed <- database.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close during the busy wait: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close joined the in-flight busy wait instead of returning")
	}
	select {
	case err := <-inflight:
		if !IsBusy(err) {
			t.Fatalf("Close must not interrupt the busy wait; it must expire busy, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the in-flight busy wait never honoured busy_timeout after Close")
	}
	unlock()
}
