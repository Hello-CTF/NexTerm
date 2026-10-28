package cron

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func openFileCronStore(t *testing.T, path, install string) (*store.Store, *SQLStore) {
	t.Helper()
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := NewSQLStore(context.Background(), db.GORM(), WithInstall(install))
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return db, jobs
}

func copyDatabaseFile(t *testing.T, from, to string) {
	t.Helper()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		source, err := os.Open(from + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		target, err := os.Create(to + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(target, source); err != nil {
			t.Fatal(err)
		}
		_ = source.Close()
		if err := target.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// A database copied to another installation takes a new device ID, so its jobs stop running there until
// adopted; the original installation keeps its ID across restarts.
func TestCopiedDatabaseGetsANewDeviceID(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	original, copied := filepath.Join(dir, "a.db"), filepath.Join(dir, "b.db")
	now := time.Now().UTC()

	db, jobs := openFileCronStore(t, original, "install-a")
	deviceA := jobs.Device().ID
	if _, err := jobs.Create(ctx, Job{
		ID: "job-1", SessionID: "s", Prompt: "p", Schedule: "* * * * *", Timezone: "UTC", Enabled: true,
		Timeout: time.Minute, CreatedAt: now, UpdatedAt: now, ConfigUpdatedAt: now, NextRunAt: now.Add(-time.Second),
	}, 4); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	copyDatabaseFile(t, original, copied)

	db, jobs = openFileCronStore(t, copied, "install-b")
	if jobs.Device().ID == deviceA {
		t.Fatal("a copied database kept the original device ID")
	}
	listed, err := jobs.List(ctx)
	if err != nil || len(listed) != 1 || !listed[0].ForeignDevice {
		t.Fatalf("copied jobs=%+v err=%v, want the job foreign", listed, err)
	}
	due, err := jobs.ListDueIdle(WithInternalAccess(ctx), now.Add(time.Minute), 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("copied database would run %d jobs (err=%v)", len(due), err)
	}
	adopted, err := jobs.Adopt(ctx, "job-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.DeviceID != jobs.Device().ID || !adopted.ConfigUpdatedAt.After(listed[0].ConfigUpdatedAt) {
		t.Fatalf("adopted job=%+v", adopted)
	}
	due, err = jobs.ListDueIdle(WithInternalAccess(ctx), adopted.NextRunAt.Add(time.Second), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("adopted job due=%d err=%v, want it scheduled here", len(due), err)
	}
	copiedID := jobs.Device().ID
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, jobs = openFileCronStore(t, copied, "install-b")
	if jobs.Device().ID != copiedID {
		t.Fatal("the copy's device ID changed on reopen under the same installation")
	}
	_ = db.Close()
	db, jobs = openFileCronStore(t, original, "install-a")
	defer db.Close()
	if jobs.Device().ID != deviceA {
		t.Fatal("the original installation lost its device ID")
	}
}
