package production

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

// gatedAppendStore blocks one transcript append at the store boundary once
// armed, so a test can prove a flush is in flight before it contends with
// Close or Shutdown. Everything else delegates unchanged.
type gatedAppendStore struct {
	*store.Store
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (s *gatedAppendStore) TranscriptAppendChunks(ctx context.Context, transcriptID string, chunks []store.TranscriptChunkRow, durableBytes ...map[string]int64) error {
	if s.armed.CompareAndSwap(true, false) {
		close(s.entered)
		<-s.release
	}
	return s.Store.TranscriptAppendChunks(ctx, transcriptID, chunks, durableBytes...)
}

// newInflightFlushFixture returns a started writer whose store uses the
// OpenOptions.BusyTimeout fixture: the modernc busy handler cannot be
// interrupted (see TestStoreSQLiteBusyWaitOutlivesCancelAndClose), so the
// fixture — not cancellation or pool close — bounds the contended flush and
// keeps the shutdown join deterministic.
func newInflightFlushFixture(t *testing.T, busyTimeout time.Duration) (*transcriptWriter, *store.Store, *gatedAppendStore, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	database, err := store.OpenWithOptions(ctx, path, store.OpenOptions{BusyTimeout: busyTimeout})
	if err != nil {
		t.Fatal(err)
	}
	gated := &gatedAppendStore{Store: database, entered: make(chan struct{}), release: make(chan struct{})}
	writer := newTranscriptWriter(transcriptWriterConfig{Database: gated, Logger: discardTranscriptLogger()})
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return writer, database, gated, path
}

func lockStoreExclusively(t *testing.T, path string) (*store.Store, *sql.Conn) {
	t.Helper()
	locker, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := locker.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	return locker, conn
}

// enqueueTailWithInflightFlush flushes the prefix, takes the exclusive lock,
// and returns with the tail flush provably inside the store while the lock is
// held: the driver busy wait is unavoidable from there.
func enqueueTailWithInflightFlush(t *testing.T, writer *transcriptWriter, database *store.Store, gated *gatedAppendStore, path string) (*store.Store, *sql.Conn) {
	t.Helper()
	ctx := context.Background()
	writer.SessionStarted(ctx, transcriptIdentity("session-inflight"))
	writer.SessionOutput(ctx, "tab-1", "session-inflight", "tab-1", []byte("flushed-prefix\r\n"))
	waitForTranscriptChunks(t, database, mustTranscriptID(t, database), 1)
	locker, lockConn := lockStoreExclusively(t, path)
	gated.armed.Store(true)
	writer.SessionOutput(ctx, "tab-1", "session-inflight", "tab-1", []byte("unflushed-tail\r\n"))
	select {
	case <-gated.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the tail flush never reached the store")
	}
	close(gated.release)
	time.Sleep(300 * time.Millisecond) // the flush is inside the driver busy wait
	return locker, lockConn
}

func rollbackLocker(t *testing.T, locker *store.Store, lockConn *sql.Conn) {
	t.Helper()
	if _, err := lockConn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Error(err)
	}
	if err := lockConn.Close(); err != nil {
		t.Error(err)
	}
	if err := locker.Close(); err != nil {
		t.Error(err)
	}
}

func assertOnlyPrefixPersisted(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	offset, err := reopened.DurableTranscriptOffsetGet(ctx, "tab-1")
	if err != nil {
		t.Fatal(err)
	}
	if offset != int64(len("flushed-prefix\r\n")) {
		t.Fatalf("the failed in-flight flush must not advance the offset, got %d", offset)
	}
	chunks := readTranscriptAll(t, reopened, mustTranscriptID(t, reopened))
	if len(chunks) != 1 || !bytes.Contains(chunks[0].Data, []byte("flushed-prefix")) {
		t.Fatalf("only the flushed prefix may be persisted: %+v", chunks)
	}
}

// M91's forced-race diagnostic, made deterministic: the tail flush is
// provably in flight inside the driver busy wait when Close and Shutdown run.
// The busy-timeout fixture bounds the uninterruptible wait at 1.5s, so the
// 30s shutdown join returns nil — with the 30s production busy_timeout this
// same interleaving deterministically returned context.DeadlineExceeded. The
// data outcome is unchanged: the unflushed tail stays suppressed and the
// durable offset does not advance.
func TestTranscriptWriterShutdownJoinsInflightFlushPastClose(t *testing.T) {
	ctx := context.Background()
	writer, database, gated, path := newInflightFlushFixture(t, 1500*time.Millisecond)
	locker, lockConn := enqueueTailWithInflightFlush(t, writer, database, gated, path)

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown must join the run loop once the bounded busy wait expires: %v", err)
	}
	if stats := writer.statsSnapshot(); stats.flushErrors == 0 {
		t.Fatal("the contended in-flight flush must surface as a flush error")
	}
	rollbackLocker(t, locker, lockConn)
	assertOnlyPrefixPersisted(t, path)
}

// The same in-flight interleaving without closing the store first: Shutdown
// joins after the bounded busy wait, the tail stays suppressed, and the
// transcript keeps crash semantics (the contended End also fails busy).
func TestTranscriptWriterShutdownJoinsInflightFlushUnderLock(t *testing.T) {
	ctx := context.Background()
	writer, database, gated, path := newInflightFlushFixture(t, 1500*time.Millisecond)
	locker, lockConn := enqueueTailWithInflightFlush(t, writer, database, gated, path)
	t.Cleanup(func() { _ = database.Close() })

	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown must join the run loop once the bounded busy wait expires: %v", err)
	}
	if stats := writer.statsSnapshot(); stats.flushErrors == 0 {
		t.Fatal("the contended in-flight flush must surface as a flush error")
	}
	rollbackLocker(t, locker, lockConn)
	assertOnlyPrefixPersisted(t, path)
}

// Positive control for the fixture: when the external lock clears before the
// busy timeout expires, the in-flight flush commits and Shutdown drains and
// ends the transcript — the bounded wait did not weaken the durable flush.
func TestTranscriptWriterInflightFlushPersistsOnceContentionClears(t *testing.T) {
	ctx := context.Background()
	writer, database, gated, path := newInflightFlushFixture(t, 2*time.Second)
	locker, lockConn := enqueueTailWithInflightFlush(t, writer, database, gated, path)
	t.Cleanup(func() { _ = database.Close() })
	rollbackLocker(t, locker, lockConn)

	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	offset, err := reopened.DurableTranscriptOffsetGet(ctx, "tab-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(len("flushed-prefix\r\n") + len("unflushed-tail\r\n")); offset != want {
		t.Fatalf("offset = %d, want both flushes persisted: %d", offset, want)
	}
	rows, err := reopened.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].EndedAt == nil {
		t.Fatalf("the recovered flush must drain and end the transcript: %+v", rows)
	}
	chunks := readTranscriptAll(t, reopened, rows[0].ID)
	if len(chunks) != 2 || !bytes.Contains(chunks[1].Data, []byte("unflushed-tail")) {
		t.Fatalf("the in-flight flush must persist once contention clears: %+v", chunks)
	}
}
