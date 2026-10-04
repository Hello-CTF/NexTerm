package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestRunUsageColumnsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	conversation, err := db.ConvCreate(ctx, "usage", map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RunInsert(ctx, RunRow{ID: "run-usage", ConversationID: conversation.ID, Status: RunStatusRunning, Source: "cron", ProfileID: "profile-1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.RunFinishUsage(ctx, "run-usage", RunStatusCompleted, "done", "", 3, 100, 40, 25, 1250); err != nil {
		t.Fatal(err)
	}
	row, err := db.RunGet(ctx, "run-usage")
	if err != nil {
		t.Fatal(err)
	}
	if row.ProfileID != "profile-1" || row.TokensIn != 100 || row.TokensOut != 40 || row.CacheCreationTokens != 25 || row.LatencyMS != 1250 {
		t.Fatalf("row = %+v", row)
	}
}

func TestRunUsageSummaryGroupsBySceneAndProfile(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	conversation, err := db.ConvCreate(ctx, "summary", map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	insert := func(id, source, profile string, tokensIn, cache, latency int64) {
		t.Helper()
		if err := db.RunInsert(ctx, RunRow{ID: id, ConversationID: conversation.ID, Status: RunStatusRunning, Source: source, ProfileID: profile}); err != nil {
			t.Fatal(err)
		}
		if err := db.RunFinishUsage(ctx, id, RunStatusCompleted, "", "", 1, tokensIn, 10, cache, latency); err != nil {
			t.Fatal(err)
		}
	}
	insert("run-chat", "chat", "profile-1", 100, 5, 1000)
	insert("run-cron", "cron", "profile-2", 200, 15, 3000)
	insert("run-chat-2", "chat", "profile-1", 50, 0, 500)
	if err := db.RunInsert(ctx, RunRow{ID: "run-active", ConversationID: conversation.ID, Status: RunStatusRunning, Source: "chat", ProfileID: "profile-1"}); err != nil {
		t.Fatal(err)
	}
	rows, err := db.RunUsageSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("summary rows = %+v", rows)
	}
	byKey := map[string]RunUsageSummaryRow{}
	for _, row := range rows {
		byKey[row.Source+"/"+row.ProfileID] = row
	}
	chat := byKey["chat/profile-1"]
	if chat.Runs != 2 || chat.TokensIn != 150 || chat.CacheCreationTokens != 5 || chat.AverageLatencyMS != 750 {
		t.Fatalf("chat summary = %+v", chat)
	}
	cron := byKey["cron/profile-2"]
	if cron.Runs != 1 || cron.TokensIn != 200 || cron.CacheCreationTokens != 15 || cron.AverageLatencyMS != 3000 {
		t.Fatalf("cron summary = %+v", cron)
	}
}

func TestRunUsageSummaryRoundsFractionalAverageLatency(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	conversation, err := db.ConvCreate(ctx, "fraction", map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	for i, latency := range []int64{1, 2} {
		id := fmt.Sprintf("run-frac-%d", i)
		if err := db.RunInsert(ctx, RunRow{ID: id, ConversationID: conversation.ID, Status: RunStatusRunning, Source: "chat"}); err != nil {
			t.Fatal(err)
		}
		if err := db.RunFinishUsage(ctx, id, RunStatusCompleted, "", "", 1, 1, 1, 0, latency); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.RunUsageSummary(ctx)
	if err != nil {
		t.Fatalf("fractional average must not fail the summary: %v", err)
	}
	if len(rows) != 1 || rows[0].AverageLatencyMS != 2 {
		t.Fatalf("rows = %+v, want rounded average 2", rows)
	}
}

func TestAIRetentionSkipsActiveAndPendingHitlRuns(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	conversation, err := db.ConvCreate(ctx, "retention", map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour).UnixMilli()
	insert := func(id, status string, finished *int64) {
		t.Helper()
		if err := db.RunInsert(ctx, RunRow{ID: id, ConversationID: conversation.ID, Status: status, CreatedAt: old, UpdatedAt: old, FinishedAt: finished}); err != nil {
			t.Fatal(err)
		}
	}
	insert("run-old-done", RunStatusCompleted, &old)
	insert("run-old-failed", RunStatusFailed, &old)
	insert("run-active", RunStatusRunning, nil)
	insert("run-interrupted", RunStatusInterrupted, nil)
	insert("run-hitl-finished", RunStatusCompleted, &old)
	if err := db.HitlRunSave(ctx, "run-hitl-pending", []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := db.HitlRunSave(ctx, "run-hitl-finished", []byte{2}); err != nil {
		t.Fatal(err)
	}
	result, err := db.EnforceRetention(ctx, RetentionPolicy{AIRunMaxAge: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if result.AIRunsDeleted != 2 {
		t.Fatalf("deleted = %d, want 2: %+v", result.AIRunsDeleted, result)
	}
	if _, err := db.RunGet(ctx, "run-old-done"); err == nil {
		t.Fatal("old finished run survived age retention")
	}
	if _, err := db.RunGet(ctx, "run-active"); err != nil {
		t.Fatal("active run was deleted")
	}
	if _, err := db.RunGet(ctx, "run-interrupted"); err != nil {
		t.Fatal("interrupted run was deleted")
	}
	if _, err := db.RunGet(ctx, "run-hitl-finished"); err != nil {
		t.Fatal("run with pending HITL state was deleted")
	}
	if _, err := db.HitlRunList(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestAIRetentionCountKeepsNewest(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	conversation, err := db.ConvCreate(ctx, "retention-count", map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		finished := base.Add(time.Duration(i) * time.Minute).UnixMilli()
		id := "run-count-" + string(rune('a'+i))
		if err := db.RunInsert(ctx, RunRow{ID: id, ConversationID: conversation.ID, Status: RunStatusCompleted, CreatedAt: finished, UpdatedAt: finished, FinishedAt: &finished}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := db.EnforceRetention(ctx, RetentionPolicy{AIRunMaxCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.AIRunsDeleted != 3 {
		t.Fatalf("deleted = %d, want 3", result.AIRunsDeleted)
	}
	rows, err := db.RunList(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "run-count-e" || rows[1].ID != "run-count-d" {
		t.Fatalf("survivors = %+v", rows)
	}
}
