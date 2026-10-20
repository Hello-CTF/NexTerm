package cron

import (
	"context"
	"testing"
	"time"
)

func TestDatabaseListDueSelectsOnlyActionableCandidates(t *testing.T) {
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
	}
	jobs[1].NextRunAt = now.Add(time.Minute)
	jobs[2].RetryAt = now.Add(time.Minute)
	jobs[3].CircuitOpenUntil = now.Add(time.Minute)
	jobs[4].Enabled = false
	jobs[5].Run = RunState{ID: "active-run", Deadline: now.Add(time.Minute)}
	jobs[5].Lease = Lease{Owner: "scheduler", ExpiresAt: now.Add(time.Minute)}
	jobs[6].Run = RunState{ID: "abandoned-run", Deadline: now.Add(-time.Minute)}
	jobs[6].Lease = Lease{Owner: "scheduler", ExpiresAt: now.Add(-time.Minute)}
	for _, job := range jobs {
		if _, err := cronStore.Create(ctx, job, 20); err != nil {
			t.Fatalf("create %s: %v", job.ID, err)
		}
	}

	candidates, err := cronStore.ListDue(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool, len(candidates))
	for _, job := range candidates {
		got[job.ID] = true
	}
	if len(got) != 2 || !got["due"] || !got["abandoned"] {
		t.Fatalf("due candidates = %+v, want due and abandoned only", got)
	}
}
