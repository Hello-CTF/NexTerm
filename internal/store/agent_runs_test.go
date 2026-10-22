package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func runPageFixture(t *testing.T, db *Store, ctx context.Context, conversationID string, count int) string {
	t.Helper()
	conversation, err := db.ConvCreate(ctx, conversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		// Stagger timestamps and share some, so the id tie-break is exercised.
		run := RunRow{
			ID: fmt.Sprintf("run-%03d", i), ConversationID: conversation.ID,
			Status: RunStatusCompleted, CreatedAt: int64(1000 + i/2), UpdatedAt: int64(1000 + i/2),
		}
		if i%3 == 0 {
			run.Status = RunStatusFailed
		}
		if err := db.RunInsert(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	return conversation.ID
}

func TestRunPageStablePagination(t *testing.T) {
	db := testStore(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	conversationID := runPageFixture(t, db, ctx, "conv-runs", 120)

	var collected []RunRow
	var cursor *RunCursor
	pages := 0
	for {
		page, err := db.RunPage(ctx, RunPageFilter{ConversationID: conversationID, Limit: 50, After: cursor})
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
	if pages != 3 || len(collected) != 120 {
		t.Fatalf("pages=%d rows=%d, want 3/120", pages, len(collected))
	}
	// Strictly descending (created_at, id) order with no duplicates or gaps.
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
	// The page cursor is exactly the last row of its page.
	first, err := db.RunPage(ctx, RunPageFilter{ConversationID: conversationID, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	last := first.Runs[len(first.Runs)-1]
	if first.NextCursor == nil || first.NextCursor.CreatedAt != last.CreatedAt || first.NextCursor.ID != last.ID {
		t.Fatalf("next cursor = %+v, want %+v", first.NextCursor, last)
	}
	second, err := db.RunPage(ctx, RunPageFilter{ConversationID: conversationID, Limit: 50, After: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if second.Runs[0].ID >= last.ID || second.Runs[0].CreatedAt > last.CreatedAt {
		t.Fatalf("second page starts inside the first: %+v", second.Runs[0])
	}
}

func TestRunPageStatusAndConversationFilters(t *testing.T) {
	db := testStore(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	conversationID := runPageFixture(t, db, ctx, "conv-runs", 12)
	otherConversation, err := db.ConvCreate(ctx, "conv-other", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RunInsert(ctx, RunRow{ID: "run-other", ConversationID: otherConversation.ID, Status: RunStatusCompleted, CreatedAt: 9999, UpdatedAt: 9999}); err != nil {
		t.Fatal(err)
	}

	failed, err := db.RunPage(ctx, RunPageFilter{ConversationID: conversationID, Statuses: []string{RunStatusFailed}, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(failed.Runs) != 4 {
		t.Fatalf("failed rows = %d, want 4", len(failed.Runs))
	}
	for _, row := range failed.Runs {
		if row.Status != RunStatusFailed {
			t.Fatalf("status filter leaked %s", row.Status)
		}
	}
	other, err := db.RunPage(ctx, RunPageFilter{ConversationID: otherConversation.ID, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Runs) != 1 || other.Runs[0].ID != "run-other" {
		t.Fatalf("conversation filter = %+v", other.Runs)
	}
	// Every status keeps all rows.
	all, err := db.RunPage(ctx, RunPageFilter{ConversationID: conversationID, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Runs) != 12 {
		t.Fatalf("unfiltered rows = %d, want 12", len(all.Runs))
	}
}

func TestRunPageOwnerIsolationAndTitleExclusion(t *testing.T) {
	db := testStore(t)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	conversationID := runPageFixture(t, db, ctxA, "conv-runs", 3)
	if err := db.RunInsert(ctxA, RunRow{ID: "run-title", ConversationID: conversationID, Status: RunStatusCompleted, Source: RunSourceTitle, CreatedAt: 5000, UpdatedAt: 5000}); err != nil {
		t.Fatal(err)
	}
	pageA, err := db.RunPage(ctxA, RunPageFilter{ConversationID: conversationID, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(pageA.Runs) != 3 {
		t.Fatalf("owner A rows = %d, want 3 (title excluded)", len(pageA.Runs))
	}
	pageB, err := db.RunPage(ctxB, RunPageFilter{ConversationID: conversationID, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(pageB.Runs) != 0 {
		t.Fatalf("cross-owner rows = %d", len(pageB.Runs))
	}
}

func TestRunPageDefaultAndCappedLimit(t *testing.T) {
	db := testStore(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	conversationID := runPageFixture(t, db, ctx, "conv-runs", 60)
	page, err := db.RunPage(ctx, RunPageFilter{ConversationID: conversationID})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Runs) != runPageDefaultLimit || page.NextCursor == nil {
		t.Fatalf("default limit page = %d rows cursor=%v", len(page.Runs), page.NextCursor)
	}
	capped, err := db.RunPage(ctx, RunPageFilter{ConversationID: conversationID, Limit: 9999})
	if err != nil {
		t.Fatal(err)
	}
	if len(capped.Runs) != 60 {
		t.Fatalf("capped page = %d rows, want all 60", len(capped.Runs))
	}
}
