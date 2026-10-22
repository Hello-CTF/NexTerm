package cron

import (
	"context"
	"testing"
	"time"
)

func TestDatabaseDueScansSelectOnlyActionableCandidates(t *testing.T) {
	cronStore := openDatabaseStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	newJob := func(id string) Job {
		job := fullTestJob(id, "session", now)
		job.Run = RunState{}
		job.Lease = Lease{}
		job.RetryAt = time.Time{}
		job.CircuitOpenUntil = time.Time{}
		job.NextRunAt = now.Add(-time.Minute)
		return job
	}
	jobs := []Job{
		newJob("due"),
		newJob("future"),
		newJob("retry-waiting"),
		newJob("circuit-waiting"),
		newJob("disabled"),
		newJob("active"),
		newJob("abandoned"),
		newJob("due-earlier"),
		newJob("due-b"),
		newJob("abandoned-earlier"),
	}
	jobs[1].NextRunAt = now.Add(time.Minute)
	jobs[2].RetryAt = now.Add(time.Minute)
	jobs[3].CircuitOpenUntil = now.Add(time.Minute)
	jobs[4].Enabled = false
	jobs[5].Run = RunState{ID: "active-run", Deadline: now.Add(time.Minute)}
	jobs[5].Lease = Lease{Owner: "scheduler", ExpiresAt: now.Add(time.Minute)}
	jobs[6].Run = RunState{ID: "abandoned-run", Deadline: now.Add(-time.Minute)}
	jobs[6].Lease = Lease{Owner: "scheduler", ExpiresAt: now.Add(-time.Minute)}
	jobs[7].NextRunAt = now.Add(-2 * time.Minute)
	jobs[8].NextRunAt = now.Add(-time.Minute)
	jobs[9].Run = RunState{ID: "abandoned-earlier-run", Deadline: now.Add(-2 * time.Minute)}
	jobs[9].Lease = Lease{Owner: "scheduler", ExpiresAt: now.Add(-2 * time.Minute)}
	for _, job := range jobs {
		if _, err := cronStore.Create(ctx, job, 20); err != nil {
			t.Fatalf("create %s: %v", job.ID, err)
		}
	}

	idle, err := cronStore.ListDueIdle(ctx, now, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := jobIDs(idle); !equalJobIDs(got, []string{"due-earlier", "due", "due-b"}) {
		t.Fatalf("idle candidates = %v; want due-earlier, due, due-b in stable order", got)
	}
	recovery, err := cronStore.ListRecoveryCandidates(ctx, now, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := jobIDs(recovery); !equalJobIDs(got, []string{"abandoned-earlier", "abandoned"}) {
		t.Fatalf("recovery candidates = %v; want abandoned-earlier, abandoned in stable order", got)
	}

	limited, err := cronStore.ListDueIdle(ctx, now, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := jobIDs(limited); !equalJobIDs(got, []string{"due-earlier", "due"}) {
		t.Fatalf("limited idle batch = %v; want the first two in stable order", got)
	}
	limitedRecovery, err := cronStore.ListRecoveryCandidates(ctx, now, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := jobIDs(limitedRecovery); !equalJobIDs(got, []string{"abandoned-earlier"}) {
		t.Fatalf("limited recovery batch = %v; want the earliest deadline", got)
	}
	remaining, err := cronStore.ListRecoveryCandidates(ctx, now, 1, "abandoned-earlier")
	if err != nil || !equalJobIDs(jobIDs(remaining), []string{"abandoned"}) {
		t.Fatalf("recovery after excluding local cleanup = %v, %v", jobIDs(remaining), err)
	}
	if empty, err := cronStore.ListDueIdle(ctx, now, 0); err != nil || len(empty) != 0 {
		t.Fatalf("zero-limit idle batch = %v, %v; want none", jobIDs(empty), err)
	}
}

func equalJobIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
