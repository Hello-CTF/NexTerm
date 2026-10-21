package production

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

// gatedAppendStore blocks one transcript append at the store boundary once
// armed and records the real results of the append and the session end, so a
// test can prove a flush is in flight before it contends with Close or
// Shutdown and assert which driver outcome it actually met — a busy error
// from the contended driver wait, a pool-close error from a flush that
// started after Close, or success. Everything else delegates unchanged.
type gatedAppendStore struct {
	*store.Store
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}

	mu         sync.Mutex
	appendErr  error
	appendDone bool
	endErr     error
	endDone    bool
}

func (s *gatedAppendStore) TranscriptAppendChunks(ctx context.Context, transcriptID string, chunks []store.TranscriptChunkRow, durableBytes ...map[string]int64) error {
	gated := s.armed.CompareAndSwap(true, false)
	if gated {
		close(s.entered)
		<-s.release
	}
	err := s.Store.TranscriptAppendChunks(ctx, transcriptID, chunks, durableBytes...)
	if gated {
		// Only the gated call's outcome is recorded: earlier flushes (the
		// prefix) must not latch the result the test asserts on.
		s.mu.Lock()
		s.appendErr, s.appendDone = err, true
		s.mu.Unlock()
	}
	return err
}

func (s *gatedAppendStore) TranscriptEnd(ctx context.Context, transcriptID string, endedAt int64, truncated bool) error {
	err := s.Store.TranscriptEnd(ctx, transcriptID, endedAt, truncated)
	s.mu.Lock()
	s.endErr, s.endDone = err, true
	s.mu.Unlock()
	return err
}

func (s *gatedAppendStore) results() (appendErr error, appendDone bool, endErr error, endDone bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendErr, s.appendDone, s.endErr, s.endDone
}

// rootError unwraps an error chain to its leaf so tests can compare error
// classes by identity: the closed-pool leaf is database/sql's unexported
// "database is closed" sentinel, the busy leaf a *sqlite.Error.
func rootError(err error) error {
	for err != nil {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
	return err
}

// closedPoolRoot returns the leaf error this store produces for a write on
// the closed pool, giving tests a reference for the closed-pool failure
// class without matching the unexported sentinel's message text.
func closedPoolRoot(t *testing.T, database *store.Store) error {
	t.Helper()
	err := database.TranscriptStart(context.Background(), store.TranscriptRow{SessionID: "closed-pool-ref", StartedAt: 1})
	if err == nil {
		t.Fatal("a write on the closed pool must fail")
	}
	return rootError(err)
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

// flushPrefixAndLock flushes the prefix chunk, takes the exclusive lock, and
// returns the locker pieces.
func flushPrefixAndLock(t *testing.T, writer *transcriptWriter, database *store.Store, path string) (*store.Store, *sql.Conn) {
	t.Helper()
	ctx := context.Background()
	writer.SessionStarted(ctx, transcriptIdentity("session-inflight"))
	writer.SessionOutput(ctx, "tab-1", "session-inflight", "tab-1", []byte("flushed-prefix\r\n"))
	waitForTranscriptChunks(t, database, mustTranscriptID(t, database), 1)
	return lockStoreExclusively(t, path)
}

// startGatedTailFlush arms the gate and enqueues the tail; it returns once
// the flush is blocked at the store boundary, before any driver call.
func startGatedTailFlush(t *testing.T, writer *transcriptWriter, gated *gatedAppendStore) {
	t.Helper()
	ctx := context.Background()
	gated.armed.Store(true)
	writer.SessionOutput(ctx, "tab-1", "session-inflight", "tab-1", []byte("unflushed-tail\r\n"))
	select {
	case <-gated.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the tail flush never reached the store")
	}
}

// waitForInflightDriverWait blocks until the pool shows a connection checked
// out continuously for the hold window. The append's pre-flush read returns
// its connection within milliseconds, so only a statement stuck in the driver
// busy wait against the locker's exclusive lock can hold a conn this long.
// A condition wait on observable pool state — never a fixed sleep guessing
// driver progress.
func waitForInflightDriverWait(t *testing.T, db *sql.DB) {
	t.Helper()
	const sample = 5 * time.Millisecond
	const holdWindow = 50 * time.Millisecond
	held := time.Duration(0)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if db.Stats().InUse > 0 {
			held += sample
			if held >= holdWindow {
				return
			}
		} else {
			held = 0
		}
		time.Sleep(sample)
	}
	t.Fatal("the tail flush never entered the driver busy wait")
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
// provably inside the driver busy wait (the pool holds its connection) when
// Close and Shutdown run — the captured append result must be the driver's
// busy error, never a pool-close error, and the post-Close drain must fail
// fast on the closed pool instead of busy-waiting again. The busy-timeout
// fixture bounds the uninterruptible wait at 2s, so the 30s shutdown join
// returns nil — with the 30s production busy_timeout this same interleaving
// deterministically returned context.DeadlineExceeded. The data outcome is
// unchanged: the unflushed tail stays suppressed and the durable offset does
// not advance.
func TestTranscriptWriterShutdownJoinsInflightFlushPastClose(t *testing.T) {
	ctx := context.Background()
	writer, database, gated, path := newInflightFlushFixture(t, 2*time.Second)
	locker, lockConn := flushPrefixAndLock(t, writer, database, path)
	startGatedTailFlush(t, writer, gated)
	close(gated.release)
	waitForInflightDriverWait(t, database.DB())

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if _, appendDone, _, _ := gated.results(); appendDone {
		t.Fatal("the tail flush must still be in flight when Close returns")
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown must join the run loop once the bounded busy wait expires: %v", err)
	}
	appendErr, appendDone, endErr, endDone := gated.results()
	if !appendDone || !store.IsBusy(appendErr) {
		t.Fatalf("the in-flight flush must fail with the driver busy error, got done=%v err=%v", appendDone, appendErr)
	}
	if closed := closedPoolRoot(t, database); !endDone || rootError(endErr) != closed {
		t.Fatalf("the post-Close drain must fail fast on the closed pool, got done=%v err=%v", endDone, endErr)
	}
	if stats := writer.statsSnapshot(); stats.flushErrors == 0 {
		t.Fatal("the contended in-flight flush must surface as a flush error")
	}
	rollbackLocker(t, locker, lockConn)
	assertOnlyPrefixPersisted(t, path)
}

// The same in-flight interleaving without closing the store first: Shutdown
// joins after the bounded busy wait, the tail stays suppressed, and the
// drain's TranscriptEnd busy-waits again and fails busy while the lock is
// held — the wait bound is per write, not one busy_timeout for the drain.
func TestTranscriptWriterShutdownJoinsInflightFlushUnderLock(t *testing.T) {
	ctx := context.Background()
	writer, database, gated, path := newInflightFlushFixture(t, 2*time.Second)
	locker, lockConn := flushPrefixAndLock(t, writer, database, path)
	t.Cleanup(func() { _ = database.Close() })
	startGatedTailFlush(t, writer, gated)
	close(gated.release)
	waitForInflightDriverWait(t, database.DB())

	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown must join the run loop once the bounded busy wait expires: %v", err)
	}
	appendErr, appendDone, endErr, endDone := gated.results()
	if !appendDone || !store.IsBusy(appendErr) {
		t.Fatalf("the in-flight flush must fail with the driver busy error, got done=%v err=%v", appendDone, appendErr)
	}
	if !endDone || !store.IsBusy(endErr) {
		t.Fatalf("the drain's TranscriptEnd must busy-wait and fail busy under the held lock, got done=%v err=%v", endDone, endErr)
	}
	if stats := writer.statsSnapshot(); stats.flushErrors == 0 {
		t.Fatal("the contended in-flight flush must surface as a flush error")
	}
	rollbackLocker(t, locker, lockConn)
	assertOnlyPrefixPersisted(t, path)
}

// Positive control for the fixture: the tail flush is provably inside the
// controlled busy wait (the pool holds its connection) before the lock
// clears, so when the lock clears before the busy timeout the in-flight
// flush commits and Shutdown drains and ends the transcript — the bounded
// wait did not weaken the durable flush.
func TestTranscriptWriterInflightFlushPersistsOnceContentionClears(t *testing.T) {
	ctx := context.Background()
	writer, database, gated, path := newInflightFlushFixture(t, 2*time.Second)
	locker, lockConn := flushPrefixAndLock(t, writer, database, path)
	t.Cleanup(func() { _ = database.Close() })
	startGatedTailFlush(t, writer, gated)
	close(gated.release)
	waitForInflightDriverWait(t, database.DB())
	rollbackLocker(t, locker, lockConn)

	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	appendErr, appendDone, endErr, endDone := gated.results()
	if !appendDone || appendErr != nil {
		t.Fatalf("the in-flight flush must commit once the lock clears, got done=%v err=%v", appendDone, appendErr)
	}
	if !endDone || endErr != nil {
		t.Fatalf("the transcript must be ended after the recovered flush, got done=%v err=%v", endDone, endErr)
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

// The other race branch, made deterministic and kept as the counterexample
// guard: the gated tail flush starts only after Close, so it fails at pool
// checkout with the closed-pool error — never the driver busy error the
// in-flight variants assert. If those variants' in-flight synchronization
// were vacuous, their flush would land here and their busy assertion would
// fail the same way. Shutdown still joins and the data outcome is identical:
// the tail stays suppressed and the offset does not advance.
func TestTranscriptWriterShutdownJoinsFlushStartedAfterClose(t *testing.T) {
	ctx := context.Background()
	writer, database, gated, path := newInflightFlushFixture(t, 2*time.Second)
	locker, lockConn := flushPrefixAndLock(t, writer, database, path)
	startGatedTailFlush(t, writer, gated)

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	close(gated.release)
	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown must join the run loop on the closed-pool branch: %v", err)
	}
	appendErr, appendDone, endErr, endDone := gated.results()
	closed := closedPoolRoot(t, database)
	if !appendDone || store.IsBusy(appendErr) || rootError(appendErr) != closed {
		t.Fatalf("a flush starting after Close must fail on the closed pool, got done=%v err=%v", appendDone, appendErr)
	}
	if !endDone || rootError(endErr) != closed {
		t.Fatalf("the closed-pool drain must fail fast, got done=%v err=%v", endDone, endErr)
	}
	rollbackLocker(t, locker, lockConn)
	assertOnlyPrefixPersisted(t, path)
}
