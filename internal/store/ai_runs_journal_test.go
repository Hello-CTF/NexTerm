package store

import (
	"context"
	"fmt"
	"testing"
)

func appendRunEvent(t *testing.T, db *Store, runID, eventType, payload string) uint64 {
	t.Helper()
	seq, err := db.RunAppendEvent(context.Background(), runID, eventType, func(uint64) ([]byte, error) {
		return []byte(payload), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func TestRunEventsAfterLimitPaginates(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	conversation, err := db.ConvCreate(ctx, "page", map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RunInsert(ctx, RunRow{ID: "run-page", ConversationID: conversation.ID, Status: RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 5; index++ {
		appendRunEvent(t, db, "run-page", "delta", fmt.Sprintf(`{"seq":%d,"text":"%d","type":"delta"}`, index, index))
	}
	first, err := db.RunEventsAfterLimit(ctx, "run-page", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].Seq != 1 || first[1].Seq != 2 {
		t.Fatalf("first page = %+v", first)
	}
	second, err := db.RunEventsAfterLimit(ctx, "run-page", first[1].Seq, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 2 || second[0].Seq != 3 || second[1].Seq != 4 {
		t.Fatalf("second page = %+v", second)
	}
	rest, err := db.RunEventsAfterLimit(ctx, "run-page", second[1].Seq, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || rest[0].Seq != 5 {
		t.Fatalf("rest page = %+v", rest)
	}
	full, err := db.RunEventsAfter(ctx, "run-page", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 5 {
		t.Fatalf("unlimited page = %+v", full)
	}
}

func TestRunErrorCountsClassifiesRetryable(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	conversation, err := db.ConvCreate(ctx, "counts", map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"run-a", "run-b"} {
		if err := db.RunInsert(ctx, RunRow{ID: id, ConversationID: conversation.ID, Status: RunStatusRunning}); err != nil {
			t.Fatal(err)
		}
	}
	appendRunEvent(t, db, "run-a", "error", `{"message":"boom","retryable":true,"seq":1,"type":"error"}`)
	appendRunEvent(t, db, "run-a", "error", `{"message":"boom","retryable":true,"seq":2,"type":"error"}`)
	appendRunEvent(t, db, "run-a", "error", `{"message":"fatal","retryable":false,"seq":3,"type":"error"}`)
	appendRunEvent(t, db, "run-a", "delta", `{"seq":4,"text":"x","type":"delta"}`)
	appendRunEvent(t, db, "run-b", "error", `{"message":"fatal","retryable":false,"seq":1,"type":"error"}`)
	appendRunEvent(t, db, "run-b", "canceled", `{"message":"已停止本轮","seq":2,"type":"canceled"}`)
	counts, err := db.RunErrorCounts(ctx, []string{"run-a", "run-b", "run-missing"})
	if err != nil {
		t.Fatal(err)
	}
	if counts["run-a"].Retries != 2 || counts["run-a"].Failures != 1 {
		t.Fatalf("run-a counts = %+v", counts["run-a"])
	}
	if counts["run-b"].Retries != 0 || counts["run-b"].Failures != 1 {
		t.Fatalf("run-b counts = %+v", counts["run-b"])
	}
	if counts["run-missing"].Retries != 0 || counts["run-missing"].Failures != 0 {
		t.Fatalf("missing run counts = %+v", counts["run-missing"])
	}
	empty, err := db.RunErrorCounts(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty ids = %+v err=%v", empty, err)
	}
}

func TestRunListExcludesTitleSource(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	conversation, err := db.ConvCreate(ctx, "titles", map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	finished := int64(1700000000000)
	for _, row := range []RunRow{
		{ID: "run-chat", ConversationID: conversation.ID, Status: RunStatusCompleted, Source: "chat", FinishedAt: &finished},
		{ID: "run-title", ConversationID: conversation.ID, Status: RunStatusCompleted, Source: RunSourceTitle, FinishedAt: &finished},
		{ID: "run-other", ConversationID: conversation.ID, Status: RunStatusCompleted, Source: "cron", FinishedAt: &finished},
	} {
		if err := db.RunInsert(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.RunList(ctx, conversation.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("conversation runs = %+v", rows)
	}
	for _, row := range rows {
		if row.Source == RunSourceTitle {
			t.Fatalf("title row leaked into run list: %+v", row)
		}
	}
	all, err := db.RunList(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("all runs = %+v", all)
	}
	summary, err := db.RunUsageSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dimensions := map[string]bool{}
	for _, row := range summary {
		dimensions[row.Source] = true
	}
	if !dimensions[RunSourceTitle] || !dimensions["chat"] || !dimensions["cron"] {
		t.Fatalf("summary dimensions = %+v", summary)
	}
}
