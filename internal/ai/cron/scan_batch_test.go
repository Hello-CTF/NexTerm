package cron

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type scanCall struct {
	limit int
	rows  int
}

type scanRecorder struct {
	mu       sync.Mutex
	idle     []scanCall
	recovery []scanCall
}

func (r *scanRecorder) recordIdle(limit, rows int) {
	r.mu.Lock()
	r.idle = append(r.idle, scanCall{limit: limit, rows: rows})
	r.mu.Unlock()
}

func (r *scanRecorder) recordRecovery(limit, rows int) {
	r.mu.Lock()
	r.recovery = append(r.recovery, scanCall{limit: limit, rows: rows})
	r.mu.Unlock()
}

func (r *scanRecorder) idleSnapshot() []scanCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]scanCall(nil), r.idle...)
}

func (r *scanRecorder) recoverySnapshot() []scanCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]scanCall(nil), r.recovery...)
}

type recordingStore struct {
	Store
	recorder *scanRecorder
}

func (s *recordingStore) ListDueIdle(ctx context.Context, now time.Time, limit int) ([]Job, error) {
	jobs, err := s.Store.ListDueIdle(ctx, now, limit)
	s.recorder.recordIdle(limit, len(jobs))
	return jobs, err
}

func (s *recordingStore) ListRecoveryCandidates(ctx context.Context, now time.Time, limit int, excludedIDs ...string) ([]Job, error) {
	jobs, err := s.Store.ListRecoveryCandidates(ctx, now, limit, excludedIDs...)
	s.recorder.recordRecovery(limit, len(jobs))
	return jobs, err
}

func plantRunState(t *testing.T, store *memoryStore, id string, run RunState, lease Lease) {
	t.Helper()
	job := store.job(t, id)
	job.Run = run
	job.Lease = lease
	if _, err := store.CompareAndSwap(context.Background(), job, job.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestRunOnceDoesNotPrefetchIdleRowsWhenSlotsAreFull(t *testing.T) {
	ctx := context.Background()
	recorder := &scanRecorder{}
	store := &recordingStore{Store: newMemoryStore(), recorder: recorder}
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var calls atomic.Int32
	executor := executorFunc(func(_ context.Context, _ Trigger) error {
		calls.Add(1)
		<-release
		return nil
	})
	scheduler := testScheduler(t, store, executor, clock, func(options *Options) {
		options.MaxConcurrentExecutions = 2
	})
	for index := 0; index < 6; index++ {
		registerTestJob(t, scheduler, fmt.Sprintf("job-%d", index), "session")
	}
	now := clock.Add(time.Minute)

	claimed, err := scheduler.runOnce(ctx, now)
	if err != nil || claimed != 2 {
		t.Fatalf("first runOnce = %d, %v; want 2", claimed, err)
	}
	idle := recorder.idleSnapshot()
	if len(idle) != 1 || idle[0].limit > 2 || idle[0].rows > 2 {
		t.Fatalf("first tick idle scans = %+v; want one free-slot batch", idle)
	}

	claimed, err = scheduler.runOnce(ctx, now)
	if err != nil || claimed != 0 {
		t.Fatalf("full-slot runOnce = %d, %v; want 0", claimed, err)
	}
	if got := len(recorder.idleSnapshot()); got != 1 {
		t.Fatalf("idle scans after full-slot tick = %d; want still 1 (no prefetch)", got)
	}

	releaseOnce.Do(func() { close(release) })
	waitSchedulerWithTimeout(t, scheduler)
	for attempts := 0; calls.Load() < 6 && attempts < 4; attempts++ {
		if _, err := scheduler.runOnce(ctx, now); err != nil {
			t.Fatal(err)
		}
		waitSchedulerWithTimeout(t, scheduler)
	}
	if calls.Load() != 6 {
		t.Fatalf("executions = %d; want 6 (no loss across batches)", calls.Load())
	}
	for _, scan := range recorder.idleSnapshot() {
		if scan.limit > 2 || scan.rows > 2 {
			t.Fatalf("idle scan exceeded free slots: %+v", scan)
		}
	}
}

func TestRunOnceDrainsDueJobsAcrossBatchesWithoutLossOrDuplicates(t *testing.T) {
	ctx := context.Background()
	recorder := &scanRecorder{}
	store := &recordingStore{Store: newMemoryStore(), recorder: recorder}
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	const total = 10
	executed := make(chan string, total)
	scheduler := testScheduler(t, store, executorFunc(func(_ context.Context, trigger Trigger) error {
		executed <- trigger.JobID
		return nil
	}), clock, func(options *Options) {
		options.MaxConcurrentExecutions = 3
	})
	for index := 0; index < total; index++ {
		registerTestJob(t, scheduler, fmt.Sprintf("job-%02d", index), "session")
	}
	now := clock.Add(time.Minute)

	unique := make(map[string]bool, total)
	started := 0
	for attempts := 0; started < total && attempts < total; attempts++ {
		claimed, err := scheduler.runOnce(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		started += claimed
		waitSchedulerWithTimeout(t, scheduler)
	}
	if started != total {
		t.Fatalf("claimed = %d; want %d (no loss)", started, total)
	}
	for len(executed) > 0 {
		unique[<-executed] = true
	}
	if len(unique) != total {
		t.Fatalf("unique executions = %d; want %d (no duplicates)", len(unique), total)
	}
	scans := recorder.idleSnapshot()
	if len(scans) < 2 {
		t.Fatalf("idle scans = %+v; want batched draining", scans)
	}
	for _, scan := range scans {
		if scan.limit > 3 || scan.rows > 3 {
			t.Fatalf("idle scan exceeded free slots: %+v", scan)
		}
	}
}

func TestRunOnceDrainsRecoveryCandidatesWithoutReexecuting(t *testing.T) {
	ctx := context.Background()
	recorder := &scanRecorder{}
	memory := newMemoryStore()
	store := &recordingStore{Store: memory, recorder: recorder}
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	var calls atomic.Int32
	scheduler := testScheduler(t, store, executorFunc(func(context.Context, Trigger) error {
		calls.Add(1)
		return nil
	}), clock, func(options *Options) {
		options.RecoveryBatchSize = 2
	})
	now := clock.Now()
	for index := 0; index < 5; index++ {
		id := fmt.Sprintf("abandoned-%d", index)
		registerTestJob(t, scheduler, id, "session")
		plantRunState(t, memory, id,
			RunState{ID: "run-" + id, ScheduledFor: now.Add(-2 * time.Minute), StartedAt: now.Add(-2 * time.Minute), Deadline: now.Add(-time.Minute)},
			Lease{Owner: "dead-owner", ExpiresAt: now.Add(-time.Minute)})
	}
	for _, id := range []string{"active-a", "active-b"} {
		registerTestJob(t, scheduler, id, "session")
		plantRunState(t, memory, id,
			RunState{ID: "run-" + id, ScheduledFor: now, StartedAt: now, Deadline: now.Add(time.Hour)},
			Lease{Owner: "live-owner", ExpiresAt: now.Add(time.Hour)})
	}

	claimed, err := scheduler.runOnce(ctx, now)
	if err != nil || claimed != 0 {
		t.Fatalf("recovery runOnce = %d, %v; want 0 claims", claimed, err)
	}
	for index := 0; index < 5; index++ {
		id := fmt.Sprintf("abandoned-%d", index)
		job := memory.job(t, id)
		if job.Running() || job.ConsecutiveFailures != 1 || !strings.Contains(job.LastError, "interrupted") {
			t.Fatalf("abandoned run %s not reconciled: %+v", id, job)
		}
	}
	for _, id := range []string{"active-a", "active-b"} {
		job := memory.job(t, id)
		if !job.Running() || job.Run.ID != "run-"+id || job.Lease.Owner != "live-owner" {
			t.Fatalf("active run %s was recovered: %+v", id, job)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("abandoned occurrences were reexecuted")
	}
	scans := recorder.recoverySnapshot()
	if len(scans) < 3 {
		t.Fatalf("recovery scans = %+v; want batched draining", scans)
	}
	for _, scan := range scans {
		if scan.limit > 2 || scan.rows > 2 {
			t.Fatalf("recovery scan exceeded batch size: %+v", scan)
		}
	}
}

func TestRunOnceLeavesLocallyRunningRecoveryCandidateAlone(t *testing.T) {
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	var calls atomic.Int32
	scheduler := testScheduler(t, store, executorFunc(func(context.Context, Trigger) error {
		calls.Add(1)
		return nil
	}), clock, func(options *Options) {
		options.RecoveryBatchSize = 2
	})
	now := clock.Now()
	registerTestJob(t, scheduler, "stuck", "session")
	plantRunState(t, store, "stuck",
		RunState{ID: "stuck-run", ScheduledFor: now.Add(-2 * time.Minute), StartedAt: now.Add(-2 * time.Minute), Deadline: now.Add(-time.Minute)},
		Lease{Owner: "dead-owner", ExpiresAt: now.Add(-time.Minute)})
	scheduler.mu.Lock()
	scheduler.local["stuck"] = "stuck-run"
	scheduler.mu.Unlock()

	claimed, err := runOnceWithTimeout(t, scheduler, now)
	if err != nil || claimed != 0 {
		t.Fatalf("runOnce = %d, %v; want the local cleanup left alone", claimed, err)
	}
	job := store.job(t, "stuck")
	if !job.Running() || job.Run.ID != "stuck-run" {
		t.Fatalf("locally tracked run was reconciled: %+v", job)
	}
	if calls.Load() != 0 {
		t.Fatal("locally tracked run was reexecuted")
	}
}

func TestDueScanStoresBoundBatchesAndOrderStably(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	for name, build := range map[string]func(*testing.T) Store{
		"memory": func(*testing.T) Store { return newMemoryStore() },
		"sqlite": func(t *testing.T) Store { return newTestSQLiteStore(t) },
	} {
		t.Run(name, func(t *testing.T) {
			store := build(t)
			idle := func(id string, next time.Time) Job {
				return Job{
					ID: id, SessionID: "session", Prompt: "run", Schedule: "* * * * *", Timezone: "UTC",
					Enabled: true, Timeout: time.Minute, NextRunAt: next,
				}
			}
			for _, job := range []Job{
				idle("due-b", now.Add(-time.Minute)),
				idle("due-a", now.Add(-time.Minute)),
				idle("due-c", now.Add(-2*time.Minute)),
			} {
				if _, err := store.Create(ctx, job, 10); err != nil {
					t.Fatal(err)
				}
			}
			batch, err := store.ListDueIdle(ctx, now, 2)
			if err != nil {
				t.Fatal(err)
			}
			if len(batch) != 2 || batch[0].ID != "due-c" || batch[1].ID != "due-a" {
				t.Fatalf("limited idle batch = %+v; want stable (next_run_at, id) order", jobIDs(batch))
			}
			full, err := store.ListDueIdle(ctx, now, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(full) != 3 || full[0].ID != "due-c" || full[1].ID != "due-a" || full[2].ID != "due-b" {
				t.Fatalf("idle order = %+v; want stable (next_run_at, id) order", jobIDs(full))
			}
			if empty, err := store.ListDueIdle(ctx, now, 0); err != nil || len(empty) != 0 {
				t.Fatalf("zero-limit idle batch = %+v, %v; want none", jobIDs(empty), err)
			}
			if empty, err := store.ListDueIdle(ctx, now, -1); err != nil || len(empty) != 0 {
				t.Fatalf("negative-limit idle batch = %+v, %v; want none", jobIDs(empty), err)
			}

			abandoned := func(id string, deadline time.Time) Job {
				job := idle(id, now.Add(time.Hour))
				job.Run = RunState{ID: "run-" + id, ScheduledFor: deadline.Add(-time.Minute), StartedAt: deadline.Add(-time.Minute), Deadline: deadline}
				job.Lease = Lease{Owner: "dead-owner", ExpiresAt: deadline.Add(-time.Minute)}
				return job
			}
			for _, job := range []Job{
				abandoned("recovery-b", now.Add(-time.Minute)),
				abandoned("recovery-a", now.Add(-2*time.Minute)),
			} {
				if _, err := store.Create(ctx, job, 10); err != nil {
					t.Fatal(err)
				}
			}
			recovery, err := store.ListRecoveryCandidates(ctx, now, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(recovery) != 1 || recovery[0].ID != "recovery-a" {
				t.Fatalf("limited recovery batch = %+v; want the earliest deadline", jobIDs(recovery))
			}
			fullRecovery, err := store.ListRecoveryCandidates(ctx, now, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(fullRecovery) != 2 || fullRecovery[0].ID != "recovery-a" || fullRecovery[1].ID != "recovery-b" {
				t.Fatalf("recovery order = %+v; want stable (run_deadline, id) order", jobIDs(fullRecovery))
			}
			if empty, err := store.ListRecoveryCandidates(ctx, now, 0); err != nil || len(empty) != 0 {
				t.Fatalf("zero-limit recovery batch = %+v, %v; want none", jobIDs(empty), err)
			}
		})
	}
}

func jobIDs(jobs []Job) []string {
	ids := make([]string, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, job.ID)
	}
	return ids
}

func TestSchedulerHardStopWaitsForCleanupAndDoesNotRerun(t *testing.T) {
	memory := newMemoryStore()
	started := make(chan struct{})
	canceling := make(chan struct{})
	cleanup := make(chan struct{})
	var calls atomic.Int32
	executor := executorFunc(func(ctx context.Context, _ Trigger) error {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		close(canceling)
		<-cleanup
		return ctx.Err()
	})
	scheduler, err := NewScheduler(memory, executor, Options{
		Now: time.Now, OwnerID: "first", LeaseDuration: time.Hour,
		ExecutionTimeout: time.Hour, PollInterval: time.Hour, FailureBackoff: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	registration := testRegistration("session")
	registration.ID = "hard-stop-job"
	job, err := scheduler.Register(context.Background(), registration)
	if err != nil {
		t.Fatal(err)
	}
	job.NextRunAt = time.Now().Add(-time.Second)
	if _, err := memory.CompareAndSwap(context.Background(), job, job.Revision); err != nil {
		t.Fatal(err)
	}

	runContext, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- scheduler.Run(runContext) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("execution did not start")
	}
	cancel()
	select {
	case <-canceling:
	case <-time.After(2 * time.Second):
		t.Fatal("executor did not observe cancellation")
	}
	select {
	case err := <-runDone:
		t.Fatalf("Run abandoned the executor wait: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(cleanup)
	select {
	case err := <-runDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v; want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not finish after executor cleanup")
	}
	finished := memory.job(t, job.ID)
	if finished.Running() || !strings.Contains(finished.LastError, context.Canceled.Error()) {
		t.Fatalf("hard-stopped run was not reconciled: %+v", finished)
	}

	second := testScheduler(t, memory, executorFunc(func(context.Context, Trigger) error {
		calls.Add(1)
		return nil
	}), newFakeClock(time.Now()), func(options *Options) { options.OwnerID = "second" })
	if claimed, err := second.runOnce(context.Background(), time.Now()); err != nil || claimed != 0 {
		t.Fatalf("post-shutdown rerun = %d, %v; want no reentry", claimed, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("occurrence executed %d times; want 1", calls.Load())
	}
}
