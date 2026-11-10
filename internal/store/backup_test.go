package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/db/sqlitedriver"
	"gorm.io/gorm"
)

func openSnapshot(t *testing.T, path string) *sql.DB {
	t.Helper()
	u := sqliteURL(path)
	u.RawQuery = "mode=ro"
	db, err := sql.Open(sqlitedriver.Name, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSnapshotSQLiteIsConsistentUnderConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "data.db")
	live, err := Open(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Close() }()
	if err := live.GORM().Exec("CREATE TABLE backup_probe (id INTEGER PRIMARY KEY, pair INTEGER NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}

	// Every transaction inserts two rows, so a consistent snapshot always
	// holds an even number of them.
	stop := make(chan struct{})
	writerErr := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for pair := 0; ; pair++ {
			select {
			case <-stop:
				writerErr <- nil
				return
			default:
			}
			err := live.GORM().Transaction(func(tx *gorm.DB) error {
				if err := tx.Exec("INSERT INTO backup_probe (pair) VALUES (?)", pair).Error; err != nil {
					return err
				}
				return tx.Exec("INSERT INTO backup_probe (pair) VALUES (?)", pair).Error
			})
			if err != nil {
				writerErr <- err
				return
			}
		}
	}()

	var last int64 = -1
	for attempt := 0; attempt < 5; attempt++ {
		dest := filepath.Join(dir, "snapshots", "copy-"+string(rune('a'+attempt))+".db")
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			t.Fatal(err)
		}
		snapshot, err := SnapshotSQLite(ctx, source, dest)
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("snapshot %d: %v", attempt, err)
		}
		if snapshot.SQLiteVersion == "" || len(snapshot.SchemaFingerprint) != 64 {
			t.Fatalf("snapshot metadata = %+v", snapshot)
		}
		for _, sidecar := range []string{dest + "-wal", dest + "-shm", dest + "-journal"} {
			if _, err := os.Stat(sidecar); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("snapshot left %s behind (err=%v)", sidecar, err)
			}
		}
		db := openSnapshot(t, dest)
		var check, mode string
		if err := db.QueryRow("PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
			t.Fatalf("integrity_check = %q, %v", check, err)
		}
		if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "delete" {
			t.Fatalf("journal_mode = %q, %v", mode, err)
		}
		var rows int64
		if err := db.QueryRow("SELECT COUNT(*) FROM backup_probe").Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows%2 != 0 {
			t.Fatalf("snapshot %d holds %d rows: a transaction was split", attempt, rows)
		}
		if rows < last {
			t.Fatalf("snapshot %d holds %d rows, fewer than the previous %d", attempt, rows, last)
		}
		last = rows
	}
	close(stop)
	wg.Wait()
	if err := <-writerErr; err != nil {
		t.Fatalf("writer: %v", err)
	}
	if err := live.GORM().Exec("INSERT INTO backup_probe (pair) VALUES (-1)").Error; err != nil {
		t.Fatalf("live store must stay writable after snapshots: %v", err)
	}
}

func TestSnapshotSQLiteRefusesMissingSourceAndExistingTarget(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.db")
	if _, err := SnapshotSQLite(ctx, missing, filepath.Join(dir, "out.db")); err == nil {
		t.Fatal("snapshot of a missing database must fail")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot must not create the missing source (err=%v)", err)
	}

	source := filepath.Join(dir, "data.db")
	live, err := Open(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(dir, "existing.db")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SnapshotSQLite(ctx, source, existing); err == nil {
		t.Fatal("snapshot over an existing file must fail")
	}
	if data, _ := os.ReadFile(existing); string(data) != "keep" {
		t.Fatalf("existing target changed: %q", data)
	}
}

func TestSnapshotSQLiteOfStoppedDatabaseLeavesItOpenable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "data.db")
	live, err := Open(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := live.GORM().Exec("CREATE TABLE backup_probe (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := SnapshotSQLite(ctx, source, filepath.Join(dir, "copy.db")); err != nil {
		t.Fatal(err)
	}
	for _, sidecar := range []string{source + "-wal", source + "-shm"} {
		if info, err := os.Stat(sidecar); err == nil && info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, want 0600", sidecar, info.Mode().Perm())
		}
	}
	reopened, err := Open(ctx, source)
	if err != nil {
		t.Fatalf("source must reopen after a snapshot: %v", err)
	}
	if err := reopened.GORM().Exec("INSERT INTO backup_probe DEFAULT VALUES").Error; err != nil {
		t.Fatal(err)
	}
	_ = reopened.Close()
}
