package cron

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestSQLStoreScopesJobsToOwner(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctxA := ipc.WithUserID(context.Background(), "user-a")
	ctxB := ipc.WithUserID(context.Background(), "user-b")
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	jobA, err := store.Create(ctxA, fullTestJob("job-a", "session-a", now), 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctxB, fullTestJob("job-b", "session-b", now), 4); err != nil {
		t.Fatal(err)
	}

	jobsA, err := store.List(ctxA)
	if err != nil || len(jobsA) != 1 || jobsA[0].ID != jobA.ID {
		t.Fatalf("user A jobs = %+v, err=%v", jobsA, err)
	}
	jobsB, err := store.List(ctxB)
	if err != nil || len(jobsB) != 1 || jobsB[0].ID != "job-b" {
		t.Fatalf("user B jobs = %+v, err=%v", jobsB, err)
	}
	if _, err := store.CompareAndSwap(ctxB, jobA, jobA.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user B updated user A's job: %v", err)
	}
	if err := store.Delete(ctxB, jobA.SessionID, jobA.ID, jobA.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user B deleted user A's job: %v", err)
	}
	jobsA, err = store.List(ctxA)
	if err != nil || len(jobsA) != 1 || jobsA[0].ID != jobA.ID {
		t.Fatalf("user A job changed after cross-user operations = %+v, err=%v", jobsA, err)
	}
}
