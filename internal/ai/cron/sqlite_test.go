package cron

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/migrations"
)

func sqliteTestDSN(path string) string {
	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	u.RawQuery = "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	return u.String()
}

func openSQLiteStore(t *testing.T, path string) *SQLiteStore {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteTestDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	contents, err := migrations.Files.ReadFile("0005_cron.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), string(contents)); err != nil {
		_ = db.Close()
		t.Fatalf("apply 0005_cron.sql: %v", err)
	}
	contents, err = migrations.Files.ReadFile("0008_cron_model_profile.sql")
	if err != nil {
		t.Fatal(err)
	}
	var column string
	if err := db.QueryRowContext(context.Background(), `SELECT name FROM pragma_table_info('cron_job') WHERE name = 'model_profile_id'`).Scan(&column); errors.Is(err, sql.ErrNoRows) {
		if _, err := db.ExecContext(context.Background(), string(contents)); err != nil {
			_ = db.Close()
			t.Fatalf("apply 0008_cron_model_profile.sql: %v", err)
		}
	} else if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	store, err := NewSQLiteStore(context.Background(), db)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.db.Close() })
	return store
}

func newTestSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()
	return openSQLiteStore(t, filepath.Join(t.TempDir(), "cron.db"))
}

func fullTestJob(id, sessionID string, now time.Time) Job {
	return Job{
		ID: id, SessionID: sessionID, Name: "backup", Prompt: "run the backup and report",
		Schedule: "*/5 * * * *", Timezone: "UTC", Enabled: true, Timeout: 90 * time.Second,
		CreatedAt: now, UpdatedAt: now, Revision: 1, NextRunAt: now.Add(5 * time.Minute),
		RetryAt: now.Add(time.Minute), CircuitOpenUntil: now.Add(2 * time.Minute),
		ConsecutiveFailures: 3, LastRunAt: now.Add(-time.Hour), LastScheduledFor: now.Add(-time.Hour),
		LastCoalesced: true, LastError: "previous failure", ModelProfileID: "profile-1",
		Lease: Lease{Owner: "owner-1", ExpiresAt: now.Add(20 * time.Second)},
		Run:   RunState{ID: "run-1", ScheduledFor: now.Add(-time.Minute), StartedAt: now, Deadline: now.Add(time.Minute), Coalesced: true},
	}
}

func TestSQLiteStoreRoundTripPreservesEveryField(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	job := fullTestJob("job-1", "session-a", now)
	created, err := store.Create(ctx, job, 4)
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 {
		t.Fatalf("created revision = %d; want 1", created.Revision)
	}
	jobs, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("listed %d jobs; want 1", len(jobs))
	}
	got := jobs[0]
	if !jobsEqual(got, job) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, job)
	}

	stored, err := store.CompareAndSwap(ctx, got, got.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Revision != 2 {
		t.Fatalf("cas revision = %d; want 2", stored.Revision)
	}
	reloaded, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded) != 1 || reloaded[0].Revision != 2 || !jobsEqual(reloaded[0], stored) {
		t.Fatalf("cas not durable: %+v", reloaded)
	}
}

func jobsEqual(a, b Job) bool {
	return a.ID == b.ID && a.SessionID == b.SessionID && a.Name == b.Name && a.Prompt == b.Prompt &&
		a.Schedule == b.Schedule && a.Timezone == b.Timezone && a.Enabled == b.Enabled && a.Timeout == b.Timeout &&
		a.CreatedAt.Equal(b.CreatedAt) && a.UpdatedAt.Equal(b.UpdatedAt) && a.Revision == b.Revision &&
		a.NextRunAt.Equal(b.NextRunAt) && a.RetryAt.Equal(b.RetryAt) && a.CircuitOpenUntil.Equal(b.CircuitOpenUntil) &&
		a.ConsecutiveFailures == b.ConsecutiveFailures && a.LastRunAt.Equal(b.LastRunAt) &&
		a.LastScheduledFor.Equal(b.LastScheduledFor) && a.LastCoalesced == b.LastCoalesced && a.LastError == b.LastError &&
		a.ModelProfileID == b.ModelProfileID &&
		a.Lease == b.Lease && a.Run == b.Run
}

func TestSQLiteStoreCreateEnforcesBoundAndUniqueness(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"a-1", "a-2"} {
		if _, err := store.Create(ctx, fullTestJob(id, "session-a", now), 2); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Create(ctx, fullTestJob("a-3", "session-a", now), 2); !errors.Is(err, ErrJobLimit) {
		t.Fatalf("bound error = %v; want ErrJobLimit", err)
	}
	if _, err := store.Create(ctx, fullTestJob("a-1", "session-b", now), 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate error = %v; want ErrConflict", err)
	}
	if _, err := store.Create(ctx, fullTestJob("b-1", "session-b", now), 2); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, fullTestJob("b-2", "session-b", now), 1); !errors.Is(err, ErrJobLimit) {
		t.Fatalf("per-store bound error = %v; want ErrJobLimit", err)
	}
}

func TestSQLiteStoreCompareAndSwapGuards(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	created, err := store.Create(ctx, fullTestJob("job", "session-a", now), 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompareAndSwap(ctx, created, 99); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision error = %v; want ErrConflict", err)
	}
	foreign := created
	foreign.SessionID = "session-b"
	if _, err := store.CompareAndSwap(ctx, foreign, created.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign session error = %v; want ErrConflict", err)
	}
	missing := created
	missing.ID = "missing"
	if _, err := store.CompareAndSwap(ctx, missing, created.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing error = %v; want ErrNotFound", err)
	}
	updated := created
	updated.LastError = "boom"
	stored, err := store.CompareAndSwap(ctx, updated, created.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Revision != created.Revision+1 || stored.LastError != "boom" {
		t.Fatalf("stored = %+v", stored)
	}
	if _, err := store.CompareAndSwap(ctx, updated, created.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("replayed revision error = %v; want ErrConflict", err)
	}
}

func TestSQLiteStoreDeleteGuardsOtherSessions(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	a, err := store.Create(ctx, fullTestJob("a", "session-a", now), 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, fullTestJob("b", "session-b", now), 4); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "session-b", "a", a.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-session delete error = %v; want ErrNotFound", err)
	}
	if err := store.Delete(ctx, "session-a", "a", 99); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale delete error = %v; want ErrConflict", err)
	}
	if err := store.Delete(ctx, "session-a", "a", a.Revision); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "session-a", "a", a.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repeat delete error = %v; want ErrNotFound", err)
	}
	jobs, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != "b" {
		t.Fatalf("delete touched other sessions: %+v", jobs)
	}
}

func TestSQLiteStoreRestartKeepsJobsWithoutDuplicates(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cron.db")
	store := openSQLiteStore(t, path)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	running := fullTestJob("running", "session-a", now)
	if _, err := store.Create(ctx, running, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, fullTestJob("idle", "session-b", now), 4); err != nil {
		t.Fatal(err)
	}
	_ = store.db.Close()

	reopened := openSQLiteStore(t, path)
	jobs, err := reopened.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("reopened jobs = %d; want 2", len(jobs))
	}
	byID := map[string]Job{}
	for _, job := range jobs {
		byID[job.ID] = job
	}
	if got := byID["running"]; got.Revision != 1 || !got.Running() || got.Lease.Owner != "owner-1" || !got.Run.Deadline.Equal(now.Add(time.Minute)) {
		t.Fatalf("running job lost state across restart: %+v", got)
	}
	if got := byID["idle"]; got.Revision != 1 || got.LastError != "previous failure" || !got.RetryAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("idle job lost state across restart: %+v", got)
	}
}

func TestSQLiteStoreRestartReconcilesWithoutReexecuting(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cron.db")
	store := openSQLiteStore(t, path)
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	old := testScheduler(t, store, ExecutorFunc(func(context.Context, Trigger) error { return nil }), clock, func(options *Options) { options.OwnerID = "old-owner" })
	registerTestJob(t, old, "job", "session")

	claimedAt := clock.Add(time.Minute)
	job := loadSQLiteJob(t, store, "job")
	job.Lease = Lease{Owner: "old-owner", ExpiresAt: claimedAt.Add(5 * time.Second)}
	job.Run = RunState{ID: "abandoned-run", ScheduledFor: claimedAt, StartedAt: claimedAt, Deadline: claimedAt.Add(time.Minute)}
	job.NextRunAt = claimedAt.Add(time.Minute)
	if _, err := store.CompareAndSwap(ctx, job, job.Revision); err != nil {
		t.Fatal(err)
	}
	_ = store.db.Close()

	reopened := openSQLiteStore(t, path)
	var calls atomic.Int32
	restarted := testScheduler(t, reopened, ExecutorFunc(func(context.Context, Trigger) error {
		calls.Add(1)
		return nil
	}), clock, func(options *Options) { options.OwnerID = "new-owner" })
	if claimed, err := restarted.runOnce(ctx, claimedAt.Add(time.Minute)); err != nil || claimed != 0 {
		t.Fatalf("recovery runOnce = %d, %v", claimed, err)
	}
	recovered := loadSQLiteJob(t, reopened, "job")
	if recovered.Running() || recovered.ConsecutiveFailures != 1 || !strings.Contains(recovered.LastError, "interrupted") {
		t.Fatalf("abandoned run not reconciled after real restart: %+v", recovered)
	}
	if calls.Load() != 0 {
		t.Fatal("abandoned occurrence was reexecuted after real restart")
	}
	next := claimedAt.Add(2 * time.Minute)
	clock.Set(next)
	if claimed, err := restarted.runOnce(ctx, next); err != nil || claimed != 1 {
		t.Fatalf("subsequent occurrence = %d, %v", claimed, err)
	}
	restarted.wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("subsequent executions = %d; want 1", calls.Load())
	}
	final := loadSQLiteJob(t, reopened, "job")
	if final.ConsecutiveFailures != 0 || final.LastError != "" || final.LastRunAt.IsZero() {
		t.Fatalf("successful run not checkpointed durably: %+v", final)
	}
}

func loadSQLiteJob(t *testing.T, store *SQLiteStore, id string) Job {
	t.Helper()
	jobs, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.ID == id {
			return job
		}
	}
	t.Fatalf("job %s not found", id)
	return Job{}
}

func TestSQLiteStoreConcurrentCreateStaysBounded(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cron.db")
	store := openSQLiteStore(t, path)
	const max = 4
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	var succeeded atomic.Int32
	var limited atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.Create(ctx, fullTestJob(fmt.Sprintf("job-%d", i), "session-a", now), max)
			switch {
			case err == nil:
				succeeded.Add(1)
			case errors.Is(err, ErrJobLimit):
				limited.Add(1)
			default:
				t.Errorf("create %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if succeeded.Load() != max || limited.Load() != 12-max {
		t.Fatalf("succeeded=%d limited=%d; want %d/%d", succeeded.Load(), limited.Load(), max, 12-max)
	}
	jobs, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != max {
		t.Fatalf("stored jobs = %d; want %d", len(jobs), max)
	}
}

func TestSQLiteStoreConcurrentCompareAndSwapHasSingleWinner(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	created, err := store.Create(ctx, fullTestJob("job", "session-a", now), 4)
	if err != nil {
		t.Fatal(err)
	}
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			candidate := created
			candidate.LastError = fmt.Sprintf("writer-%d", i)
			if _, err := store.CompareAndSwap(ctx, candidate, created.Revision); err == nil {
				won.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("cas %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("winners = %d; want 1", won.Load())
	}
}

func TestSQLiteStoreSurfacesExplicitStorageErrors(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	_ = store.db.Close()
	if _, err := store.List(ctx); err == nil {
		t.Fatal("list on a closed database reported success")
	}
	scheduler, err := NewScheduler(store, ExecutorFunc(func(context.Context, Trigger) error { return nil }), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Register(ctx, testRegistration("session")); !errors.Is(err, ErrStorage) {
		t.Fatalf("register on a failed store = %v; want explicit ErrStorage", err)
	}
}
