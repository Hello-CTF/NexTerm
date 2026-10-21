package store_test

import (
	"context"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// TestAgentOutcomeClaimAttemptDatabase exercises the family-level atomic
// claim, the progress residual write, and the transactional
// history-plus-release completion against every real configured backend
// (PostgreSQL/MySQL/MariaDB via NEXTERM_TEST_DB_*), not just SQLite.
func TestAgentOutcomeClaimAttemptDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA := ipc.WithUserID(context.Background(), "claim-owner-a")
	ctxB := ipc.WithUserID(context.Background(), "claim-owner-b")

	// Concurrent claims on one family: the (owner_id, operation_id) primary
	// key serializes them into exactly one winner on every backend.
	const workers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	claimed := make(chan bool, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			won, err := db.AgentOutcomeClaimAttempt(ctxA, "op-family-1", "terminal_create")
			claimed <- won
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(claimed)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	wins := 0
	for won := range claimed {
		if won {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("claim wins = %d, want 1", wins)
	}

	// The winner's row is the family's single pending attempt; a progress
	// write persists the residual immediately, and a second claim loses.
	if err := db.AgentOutcomeProgress(ctxA, "op-family-1", "session-1"); err != nil {
		t.Fatal(err)
	}
	if won, err := db.AgentOutcomeClaimAttempt(ctxA, "op-family-1", "terminal_create"); err != nil || won {
		t.Fatalf("second claim = %t err=%v", won, err)
	}
	// A different owner claims an independent family row (owner scoping).
	if won, err := db.AgentOutcomeClaimAttempt(ctxB, "op-family-1", "terminal_create"); err != nil || !won {
		t.Fatalf("cross-owner claim = %t err=%v", won, err)
	}
	if err := db.AgentOutcomeProgress(ctxA, "op-family-missing", "session-x"); err == nil {
		t.Fatal("progress on a missing claim succeeded")
	}

	// Completion writes the terminal history row and releases the claim in
	// one transaction; the family's next attempt can claim again.
	if err := db.AgentOutcomeCompleteAttempt(ctxA, "op-family-1", store.AgentOutcomeRow{
		OperationID: "op-family-1:attempt-1", Kind: "terminal_create", State: store.AgentOutcomeStateSucceeded,
		ResultJSON: `{"tab_id":"tab-new"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AgentOutcomeGet(ctxA, "op-family-1:claim"); err == nil {
		t.Fatal("claim row still present after completion")
	}
	if won, err := db.AgentOutcomeClaimAttempt(ctxA, "op-family-1", "terminal_create"); err != nil || !won {
		t.Fatalf("claim after completion = %t err=%v", won, err)
	}
	if err := db.AgentOutcomeCompleteAttempt(ctxA, "op-family-1", store.AgentOutcomeRow{
		OperationID: "op-family-1:attempt-2", Kind: "terminal_create", State: store.AgentOutcomeStateFailed,
		FailureStage: "connect", Error: "dial failed", ResidualSessionID: "session-2",
	}); err != nil {
		t.Fatal(err)
	}

	// The history rows survive with their payloads, newest first.
	rows, err := db.AgentOutcomeListByPrefix(ctxA, "op-family-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].OperationID != "op-family-1:attempt-2" || rows[1].OperationID != "op-family-1:attempt-1" {
		t.Fatalf("history rows = %+v", rows)
	}
	if rows[0].State != store.AgentOutcomeStateFailed || rows[0].ResidualSessionID != "session-2" || rows[0].FailureStage != "connect" {
		t.Fatalf("failed history row = %+v", rows[0])
	}
	// Owner isolation: the other owner's family rows are invisible.
	if rows, err := db.AgentOutcomeListByPrefix(ctxB, "op-family-1", 0); err != nil || len(rows) != 1 {
		t.Fatalf("cross-owner rows = %+v err=%v", rows, err)
	}

	// The abort release deletes the claim without a history row, so the
	// family's next attempt claims cleanly.
	if won, err := db.AgentOutcomeClaimAttempt(ctxA, "op-family-2", "terminal_create"); err != nil || !won {
		t.Fatalf("claim for release = %t err=%v", won, err)
	}
	if err := db.AgentOutcomeReleaseClaim(ctxA, "op-family-2"); err != nil {
		t.Fatal(err)
	}
	if won, err := db.AgentOutcomeClaimAttempt(ctxA, "op-family-2", "terminal_create"); err != nil || !won {
		t.Fatalf("claim after release = %t err=%v", won, err)
	}
	if err := db.AgentOutcomeReleaseClaim(ctxA, "op-family-missing"); err == nil {
		t.Fatal("release of a missing claim succeeded")
	}
}
