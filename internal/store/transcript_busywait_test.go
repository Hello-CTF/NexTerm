package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	sqlite "github.com/glebarez/go-sqlite"
	sqlite3 "modernc.org/sqlite/lib"
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

// isInterruptErr reports whether the driver aborted the statement with
// SQLITE_INTERRUPT — the outcome when sqlite3_interrupt lands before the
// busy handler engages.
func isInterruptErr(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	return sqliteErr.Code()&0xff == sqlite3.SQLITE_INTERRUPT
}

// Documents the modernc busy-handler contract M91 proved and this change
// designs around rather than fixes. Every claim is anchored on the driver's
// own terminal error, which the SQLITE_BUSY handler returns only after
// busy_timeout expires — no pool metrics, no sleeps as synchronization:
//
//   - A contended statement runs the full busy_timeout even when its ctx is
//     cancelled before the call: the tx begin executes on context.Background
//     inside glebarez's newTx, and the plain statement path arms the
//     driver's interruptOnDone watcher, yet the SQLITE_BUSY result comes
//     back only at expiry in both cases. (An interrupt delivered in the
//     narrow setup window before the busy handler engages can abort the
//     statement with SQLITE_INTERRUPT instead — but the production flush
//     path never delivers one: the durable write ctx is never cancelled.)
//   - database/sql Close does not prevent a contended wait started after
//     it: Go closes only free pool connections, a connection pre-acquired
//     from the pool keeps working across Close, and the contended statement
//     on it still ends with the busy handler's terminal error. Only this
//     deterministic close-first ordering is asserted. The complementary
//     interleaving — Close landing while such a wait is already in flight —
//     is not covered by any test here: no Go-level seam proves the wait
//     engaged before Close without driver internals, and the terminal busy
//     verdict, the Close return timing, and an in-flight check cannot
//     distinguish the two orderings. M91's probes showed Close neither
//     interrupts nor joins that wait; the shutdown design relies on that
//     probe result rather than on a regression test.
//
// The fixture keeps the boundary at 2s so the assertions stay fast while the
// production default remains 30s.
func TestStoreSQLiteBusyWaitOutlivesCancelAndClose(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	// Both stores migrate before the exclusive lock is taken: opening one
	// afterwards would busy-wait on its own migration DDL.
	database, err := OpenWithOptions(ctx, path, OpenOptions{BusyTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	second, err := OpenWithOptions(ctx, path, OpenOptions{BusyTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
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
	mustBusy := func(err error, what string) {
		t.Helper()
		if !IsBusy(err) {
			t.Fatalf("%s must end with the driver busy error at busy_timeout, got %v", what, err)
		}
	}

	// The production flush path begins its transaction this way: glebarez's
	// newTx executes BEGIN IMMEDIATE with context.Background. Even a ctx
	// cancelled before the call therefore cannot reach the wait, and the
	// begin runs to busy_timeout.
	t.Run("pre-cancelled ctx does not interrupt the transaction begin busy wait", func(t *testing.T) {
		conn, err := database.DB().Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_, err = conn.BeginTx(cancelled, nil)
		mustBusy(err, "the contended begin")
	})

	// The plain statement path arms the driver's ctx watcher
	// (interruptOnDone), so a cancelled ctx can abort the statement — but
	// only when the interrupt lands before the busy handler engages; once
	// the handler is running, the statement fails at busy_timeout instead
	// (M91's probes). Which of the two outcomes a single run takes is
	// decided by interrupt timing inside the driver, so the deterministic
	// contract pinned here is the union: the statement never commits and it
	// always ends with one of the two driver error classes.
	t.Run("cancelled statement either interrupts before engagement or expires busy, never commits", func(t *testing.T) {
		conn, err := database.DB().Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_, err = conn.ExecContext(cancelled, "INSERT INTO transcript (id, session_id, started_at, owner_id) VALUES ('probe-cancel', 'session-probe', 1, '')")
		if err == nil {
			t.Fatal("the cancelled contended statement must not commit")
		}
		if !IsBusy(err) && !isInterruptErr(err) {
			t.Fatalf("the cancelled statement must end interrupt-class or busy-class, got %v", err)
		}
		t.Logf("cancelled statement ended with %v (boundary outcome is interrupt-timing dependent)", err)
	})

	// A connection checked out before Close keeps working after it: the
	// contended statement still runs to busy_timeout — Close neither
	// prevents nor interrupts the wait.
	t.Run("close before the wait neither prevents nor interrupts it", func(t *testing.T) {
		conn, err := second.DB().Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if err := second.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = conn.BeginTx(ctx, nil)
		mustBusy(err, "the busy wait starting after Close")
	})

	unlock()

	// Whichever boundary outcome the cancelled statement took, it never
	// committed: the probe row must not exist once the lock is gone.
	var rows int64
	if err := locker.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM transcript WHERE id = 'probe-cancel'").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("the cancelled statement must not commit, found %d rows", rows)
	}
}
