package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// TestRunPageContractsDatabase exercises the keyset pagination on every
// real configured backend (PostgreSQL/MySQL/MariaDB via NEXTERM_TEST_DB_*),
// not just SQLite.
func TestRunPageContractsDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := ipc.WithUserID(context.Background(), "run-page-owner")
	conversation, err := db.ConvCreate(ctx, "run-page", nil)
	if err != nil {
		t.Fatal(err)
	}
	const total = 120
	for i := 0; i < total; i++ {
		status := store.RunStatusCompleted
		if i%3 == 0 {
			status = store.RunStatusFailed
		}
		run := store.RunRow{
			ID: fmt.Sprintf("run-%03d", i), ConversationID: conversation.ID,
			Status: status, CreatedAt: int64(1000 + i/2), UpdatedAt: int64(1000 + i/2),
		}
		if err := db.RunInsert(ctx, run); err != nil {
			t.Fatal(err)
		}
	}

	var collected []store.RunRow
	var cursor *store.RunCursor
	pages := 0
	for {
		page, err := db.RunPage(ctx, store.RunPageFilter{ConversationID: conversation.ID, Limit: 50, After: cursor})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		collected = append(collected, page.Runs...)
		if page.NextCursor == nil {
			break
		}
		cursor = page.NextCursor
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if pages != 3 || len(collected) != total {
		t.Fatalf("pages=%d rows=%d, want 3/%d", pages, len(collected), total)
	}
	seen := map[string]bool{}
	for i, row := range collected {
		if seen[row.ID] {
			t.Fatalf("row %s returned twice", row.ID)
		}
		seen[row.ID] = true
		if i > 0 {
			previous := collected[i-1]
			if row.CreatedAt > previous.CreatedAt || (row.CreatedAt == previous.CreatedAt && row.ID >= previous.ID) {
				t.Fatalf("order broken at %d: %+v after %+v", i, row, previous)
			}
		}
	}
	// The status filter composes with the cursor on a real backend.
	failed, err := db.RunPage(ctx, store.RunPageFilter{ConversationID: conversation.ID, Statuses: []string{store.RunStatusFailed}, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(failed.Runs) != 40 {
		t.Fatalf("failed rows = %d, want 40", len(failed.Runs))
	}
}

// TestRunFinishWithEventIfStatusContractsDatabase exercises the atomic
// event+finish contract on every real configured backend.
func TestRunFinishWithEventIfStatusContractsDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := ipc.WithUserID(context.Background(), "finish-owner")
	conversation, err := db.ConvCreate(ctx, "finish", nil)
	if err != nil {
		t.Fatal(err)
	}
	insert := func(id, status string) {
		t.Helper()
		if err := db.RunInsert(ctx, store.RunRow{ID: id, ConversationID: conversation.ID, Status: status}); err != nil {
			t.Fatal(err)
		}
	}

	insert("run-ok", store.RunStatusRunning)
	// Success: row, event, and seq land atomically, exactly once.
	applied, err := db.RunFinishWithEventIfStatus(ctx, "run-ok", store.RunStatusRunning, store.RunStatusFailed, "", "gone", 1, 2, 3, 4, 5, "error", func(seq uint64) ([]byte, error) {
		return []byte(`{"message":"gone"}`), nil
	})
	if err != nil || !applied {
		t.Fatalf("applied=%t err=%v", applied, err)
	}
	row, _ := db.RunGet(ctx, "run-ok")
	if row.Status != store.RunStatusFailed || row.FinishedAt == nil || row.Seq != 1 {
		t.Fatalf("row = %+v", row)
	}
	events, _ := db.RunEventsAfter(ctx, "run-ok", 0)
	if len(events) != 1 || events[0].Seq != 1 || events[0].Type != "error" {
		t.Fatalf("events = %+v", events)
	}

	// Wrong expected status: a no-op that never calls the payload and
	// never appends an event.
	payloadCalled := false
	applied, err = db.RunFinishWithEventIfStatus(ctx, "run-ok", store.RunStatusRunning, store.RunStatusInterrupted, "", "stale", 0, 0, 0, 0, 0, "error", func(seq uint64) ([]byte, error) {
		payloadCalled = true
		return []byte(`{"message":"stale"}`), nil
	})
	if err != nil || applied {
		t.Fatalf("wrong status applied=%t err=%v", applied, err)
	}
	if payloadCalled {
		t.Fatal("wrong status called the payload")
	}
	events, _ = db.RunEventsAfter(ctx, "run-ok", 0)
	if len(events) != 1 {
		t.Fatalf("wrong status appended an event: %+v", events)
	}

	// Payload error: the row, seq, and event all roll back, and the retry
	// produces no seq gap.
	insert("run-rollback", store.RunStatusRunning)
	applied, err = db.RunFinishWithEventIfStatus(ctx, "run-rollback", store.RunStatusRunning, store.RunStatusFailed, "", "gone", 0, 0, 0, 0, 0, "error", func(seq uint64) ([]byte, error) {
		return nil, errors.New("payload failed")
	})
	if err == nil || applied {
		t.Fatalf("payload error applied=%t err=%v", applied, err)
	}
	row, _ = db.RunGet(ctx, "run-rollback")
	if row.Status != store.RunStatusRunning || row.FinishedAt != nil || row.Seq != 0 {
		t.Fatalf("payload error did not roll back: %+v", row)
	}
	events, _ = db.RunEventsAfter(ctx, "run-rollback", 0)
	if len(events) != 0 {
		t.Fatalf("payload error appended an event: %+v", events)
	}
	applied, err = db.RunFinishWithEventIfStatus(ctx, "run-rollback", store.RunStatusRunning, store.RunStatusFailed, "", "gone", 0, 0, 0, 0, 0, "error", func(seq uint64) ([]byte, error) {
		return []byte(`{"message":"gone"}`), nil
	})
	if err != nil || !applied {
		t.Fatalf("retry applied=%t err=%v", applied, err)
	}
	events, _ = db.RunEventsAfter(ctx, "run-rollback", 0)
	if len(events) != 1 || events[0].Seq != 1 {
		t.Fatalf("retry produced a seq gap: %+v", events)
	}

	// Concurrent writers with the same expected status: exactly one
	// applies, exactly one event lands.
	insert("run-race", store.RunStatusRunning)
	const writers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan bool, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			applied, err := db.RunFinishWithEventIfStatus(ctx, "run-race", store.RunStatusRunning, store.RunStatusFailed, "", "gone", 0, 0, 0, 0, 0, "error", func(seq uint64) ([]byte, error) {
				return []byte(`{"message":"gone"}`), nil
			})
			if err != nil {
				t.Error(err)
				return
			}
			results <- applied
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	appliedCount := 0
	for applied := range results {
		if applied {
			appliedCount++
		}
	}
	if appliedCount != 1 {
		t.Fatalf("applied writers = %d, want exactly 1", appliedCount)
	}
	events, _ = db.RunEventsAfter(ctx, "run-race", 0)
	if len(events) != 1 {
		t.Fatalf("race produced %d events, want exactly 1", len(events))
	}

	// Cross-owner rejection.
	other := ipc.WithUserID(context.Background(), "other-owner")
	if _, err := db.RunFinishWithEventIfStatus(other, "run-race", store.RunStatusRunning, store.RunStatusFailed, "", "gone", 0, 0, 0, 0, 0, "error", func(seq uint64) ([]byte, error) {
		return []byte(`{"message":"gone"}`), nil
	}); err == nil {
		t.Fatal("cross-owner finish succeeded")
	}

	// The older conditional finish is unchanged.
	insert("run-legacy", store.RunStatusRunning)
	applied, err = db.RunFinishUsageIfStatus(ctx, "run-legacy", store.RunStatusRunning, store.RunStatusFailed, "", "gone", 0, 0, 0, 0, 0)
	if err != nil || !applied {
		t.Fatalf("legacy conditional finish applied=%t err=%v", applied, err)
	}
}
