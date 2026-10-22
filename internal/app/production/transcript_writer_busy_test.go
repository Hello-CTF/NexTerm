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
// test can assert which driver outcome the flush actually met. The busy
// handler's terminal SQLITE_BUSY is the only engagement evidence the
// glebarez/modernc stack exposes to Go (there is no busy-handler callback
// seam without unsafe or new dependencies), so every contention claim below
// is anchored on that recorded driver result — never on pool metrics or
// sleeps. Everything else delegates unchanged.
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

func (s *gatedAppendStore) waitForAppendResult(t *testing.T) error {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if appendErr, appendDone, _, _ := s.results(); appendDone {
			return appendErr
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the gated append never returned")
	return nil
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

// The flush is provably contended while Shutdown runs: the pool stays open,
// so the gated append has no outcome other than meeting the held lock, and
// its recorded busy error — the busy handler's terminal event — is only
// produced after the wait actually engaged. The drain's TranscriptEnd
// contends and fails busy again while the lock is held, so the wait bound
// is per write, not one busy_timeout for the drain. Shutdown joins both
// failures (their results are recorded before the join returns) well inside
// the 30s budget — with the 30s production busy_timeout this interleaving
// deterministically returned context.DeadlineExceeded in M91's forced-race
// diagnostic. Closing the store afterwards, in the production order, is a
// no-op for the already-finished flush. The data outcome is unchanged: the
// unflushed tail stays suppressed and the durable offset does not advance.
func TestTranscriptWriterShutdownJoinsInflightFlushUnderLock(t *testing.T) {
	ctx := context.Background()
	writer, database, gated, path := newInflightFlushFixture(t, 600*time.Millisecond)
	locker, lockConn := flushPrefixAndLock(t, writer, database, path)
	t.Cleanup(func() { _ = database.Close() })
	startGatedTailFlush(t, writer, gated)
	close(gated.release)

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
	if err := database.Close(); err != nil {
		t.Fatalf("closing the store after the joined shutdown: %v", err)
	}
	rollbackLocker(t, locker, lockConn)
	assertOnlyPrefixPersisted(t, path)
}

// Contention and recovery, proven on driver verdicts alone: the gated tail
// flush meets the held lock and fails with the busy handler's terminal
// error — the only engagement evidence this driver stack exposes — which
// also freezes its durable advance. Once the lock clears, the writer
// persists the next output, ends the transcript, and the durable offset
// still covers only flushed bytes. Whether a flush already waiting when the
// lock clears goes on to commit cannot be asserted from Go on this stack:
// the busy handler's engagement is not observable before its terminal
// result without a driver-internal seam that glebarez/modernc do not
// provide without unsafe or new dependencies (see TestStoreSQLiteBusyWaitOutlivesCancelAndClose).
func TestTranscriptWriterRecoversAfterContentionClears(t *testing.T) {
	ctx := context.Background()
	writer, database, gated, path := newInflightFlushFixture(t, 600*time.Millisecond)
	locker, lockConn := flushPrefixAndLock(t, writer, database, path)
	t.Cleanup(func() { _ = database.Close() })
	startGatedTailFlush(t, writer, gated)
	close(gated.release)

	if appendErr := gated.waitForAppendResult(t); !store.IsBusy(appendErr) {
		t.Fatalf("the contended tail flush must fail with the driver busy error, got %v", appendErr)
	}
	rollbackLocker(t, locker, lockConn)
	writer.SessionOutput(ctx, "tab-1", "session-inflight", "tab-1", []byte("post-clear-tail\r\n"))
	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	if _, _, endErr, endDone := gated.results(); !endDone || endErr != nil {
		t.Fatalf("the transcript must be ended after the recovery, got done=%v err=%v", endDone, endErr)
	}

	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	rows, err := reopened.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].EndedAt == nil || !rows[0].Truncated {
		t.Fatalf("the recovery must end the transcript and keep the contention failure marked truncated: %+v", rows)
	}
	chunks := readTranscriptAll(t, reopened, rows[0].ID)
	if len(chunks) != 2 || !bytes.Contains(chunks[0].Data, []byte("flushed-prefix")) || !bytes.Contains(chunks[1].Data, []byte("post-clear-tail")) {
		t.Fatalf("the recovery must persist the prefix and the post-clear output, got %+v", chunks)
	}
	offset, err := reopened.DurableTranscriptOffsetGet(ctx, "tab-1")
	if err != nil {
		t.Fatal(err)
	}
	if offset != int64(len("flushed-prefix\r\n")) {
		t.Fatalf("the durable offset must stay frozen at the flushed prefix after the failed flush, got %d", offset)
	}
}

// The closed-pool race branch, made deterministic and kept as the
// counterexample guard: the gated tail flush starts only after Close, so it
// fails at pool checkout with the closed-pool error — never the driver busy
// error the held-lock variant asserts. The remaining interleaving — the
// production flush already inside the driver's busy wait at the exact
// Close instant — is not deterministically covered from Go on this stack:
// the pool checkout that must precede Close is not observable without
// driver internals, and the terminal busy verdict cannot distinguish that
// ordering from close-first, so no test here or in the store package
// asserts it. TestStoreSQLiteBusyWaitOutlivesCancelAndClose covers only the
// deterministic close-first ordering — Close does not prevent a contended
// wait started afterwards on a pre-acquired connection — and M91's probes
// supply the behavioral contract for the uncovered half. Shutdown still
// joins here and the data outcome is identical: the tail stays suppressed
// and the offset does not advance.
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
