package store_test

import (
	"context"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// TestAgentOutcomeContractsDatabase exercises the outcome ledger CRUD,
// compare-and-swap completion, owner isolation, and concurrent completion
// against every real configured backend (PostgreSQL/MySQL/MariaDB via
// NEXTERM_TEST_DB_*), not just SQLite.
func TestAgentOutcomeContractsDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA := ipc.WithUserID(context.Background(), "outcome-owner-a")
	ctxB := ipc.WithUserID(context.Background(), "outcome-owner-b")

	// Unknown versus initiated versus final on a real backend.
	if _, err := db.AgentOutcomeGet(ctxA, "op-1"); err == nil {
		t.Fatal("unknown operation resolved")
	}
	row, created, err := db.AgentOutcomeBegin(ctxA, store.AgentOutcomeRow{OperationID: "op-1", Kind: "terminal_create", State: store.AgentOutcomeStateInitiated})
	if err != nil || !created {
		t.Fatalf("begin = %+v created=%t err=%v", row, created, err)
	}
	if _, created, err := db.AgentOutcomeBegin(ctxA, store.AgentOutcomeRow{OperationID: "op-1", Kind: "other", State: store.AgentOutcomeStateInitiated}); err != nil || created {
		t.Fatalf("second begin created=%t err=%v", created, err)
	}

	// Owner isolation on read and completion.
	if _, err := db.AgentOutcomeGet(ctxB, "op-1"); err == nil {
		t.Fatal("cross-owner get succeeded")
	}
	if _, err := db.AgentOutcomeComplete(ctxB, "op-1", map[string]any{"state": store.AgentOutcomeStateSucceeded, "finished_at": int64(1)}); err == nil {
		t.Fatal("cross-owner complete succeeded")
	}

	// Completion records the failure stage and residual resource.
	failed, err := db.AgentOutcomeComplete(ctxA, "op-1", map[string]any{
		"state": store.AgentOutcomeStateFailed, "failure_stage": "connect", "error": "dial failed",
		"residual_session_id": "session-1", "residual_resource": "tab-partial", "finished_at": int64(10),
	})
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != store.AgentOutcomeStateFailed || failed.FailureStage != "connect" ||
		failed.ResidualSessionID != "session-1" || failed.ResidualResource != "tab-partial" {
		t.Fatalf("failure record = %+v", failed)
	}

	// Monotonic completion: a later final write never regresses the outcome.
	again, err := db.AgentOutcomeComplete(ctxA, "op-1", map[string]any{
		"state": store.AgentOutcomeStateSucceeded, "result_json": `{"forged":true}`, "finished_at": int64(20),
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.State != store.AgentOutcomeStateFailed || again.FailureStage != "connect" {
		t.Fatalf("final outcome regressed: %+v", again)
	}

	// Concurrent completions on a real backend: exactly one winner, and every
	// caller observes that same winner.
	if _, _, err := db.AgentOutcomeBegin(ctxA, store.AgentOutcomeRow{OperationID: "op-2", Kind: "paste_submit", State: store.AgentOutcomeStateInitiated}); err != nil {
		t.Fatal(err)
	}
	const workers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	rows := make(chan store.AgentOutcomeRow, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			state := store.AgentOutcomeStateSucceeded
			if worker%2 == 1 {
				state = store.AgentOutcomeStateFailed
			}
			row, err := db.AgentOutcomeComplete(ctxA, "op-2", map[string]any{
				"state": state, "result_json": `{"worker":` + string(rune('0'+worker)) + `}`, "finished_at": int64(30),
			})
			if err != nil {
				errs <- err
				return
			}
			rows <- row
		}(i)
	}
	close(start)
	wg.Wait()
	close(rows)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var winner store.AgentOutcomeRow
	for row := range rows {
		if !store.AgentOutcomeFinal(row.State) || row.FinishedAt == nil {
			t.Fatalf("non-final completion: %+v", row)
		}
		if winner.OperationID == "" {
			winner = row
			continue
		}
		if row.State != winner.State || row.ResultJSON != winner.ResultJSON {
			t.Fatalf("callers observed different winners: %+v vs %+v", row, winner)
		}
	}
	stored, err := db.AgentOutcomeGet(ctxA, "op-2")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != winner.State || stored.ResultJSON != winner.ResultJSON {
		t.Fatalf("stored = %+v want winner %+v", stored, winner)
	}

	// Owner-scoped prefix lookup on a real backend: newest first, literal
	// pattern characters, and no cross-owner leakage.
	prefixRows, err := db.AgentOutcomeListByPrefix(ctxA, "op-", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixRows) != 2 || prefixRows[0].OperationID != "op-2" || prefixRows[1].OperationID != "op-1" {
		t.Fatalf("prefix rows = %+v", prefixRows)
	}
	prefixRows, err = db.AgentOutcomeListByPrefix(ctxA, "op-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixRows) != 1 || prefixRows[0].State != store.AgentOutcomeStateFailed {
		t.Fatalf("exact prefix rows = %+v", prefixRows)
	}
	prefixRows, err = db.AgentOutcomeListByPrefix(ctxA, "op-%", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixRows) != 0 {
		t.Fatalf("literal %% prefix matched %+v", prefixRows)
	}
	prefixRows, err = db.AgentOutcomeListByPrefix(ctxB, "op-", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixRows) != 0 {
		t.Fatalf("cross-owner prefix rows = %+v", prefixRows)
	}
	if _, err := db.AgentOutcomeListByPrefix(ctxA, "", 0); err == nil {
		t.Fatal("empty prefix accepted")
	}
}
