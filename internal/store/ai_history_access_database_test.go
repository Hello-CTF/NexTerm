package store_test

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type ConversationRow = store.ConversationRow
type RunRow = store.RunRow
type MessageRow = store.MessageRow
type RunEventRow = store.RunEventRow
type HitlRunRow = store.HitlRunRow
type CheckpointRow = store.CheckpointRow

const RunStatusRunning = store.RunStatusRunning

var WithAIHistoryInternalAccess = store.WithAIHistoryInternalAccess

func TestAIHistoryQueryIsolationDatabase(t *testing.T) {
	ctx := context.Background()
	database := dbtest.NewFixture(t).OpenStore(t)
	gormDB := database.GORM().WithContext(ctx)
	for _, fixture := range []struct {
		id      string
		ownerID string
	}{
		{id: "a", ownerID: "owner-a"},
		{id: "b", ownerID: "owner-b"},
		{id: "orphan", ownerID: ""},
	} {
		conversation := ConversationRow{ID: "conv-" + fixture.id, ScopeJSON: "{}", CreatedAt: 1, UpdatedAt: 1, OwnerID: fixture.ownerID}
		if err := gormDB.Create(&conversation).Error; err != nil {
			t.Fatal(err)
		}
		run := RunRow{ID: "run-" + fixture.id, ConversationID: conversation.ID, Status: RunStatusRunning, Source: "chat", Seq: 1, CreatedAt: 2, UpdatedAt: 2, OwnerID: fixture.ownerID}
		if err := gormDB.Create(&run).Error; err != nil {
			t.Fatal(err)
		}
		for _, value := range []any{
			&MessageRow{ID: "message-" + fixture.id, ConversationID: conversation.ID, Role: "user", ContentJSON: "{}", CreatedAt: 3, Seq: 1, OwnerID: fixture.ownerID},
			&RunEventRow{RunID: run.ID, Seq: 1, Type: "delta", PayloadJSON: "{}", CreatedAt: 4, OwnerID: fixture.ownerID},
			&HitlRunRow{ID: run.ID, Data: []byte("{}"), UpdatedAt: 5, OwnerID: fixture.ownerID},
			&CheckpointRow{ID: run.ID, Data: []byte("{}"), UpdatedAt: 6, OwnerID: fixture.ownerID},
		} {
			if err := gormDB.Create(value).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	database.SetAIHistoryShared(false)
	regular := ipc.WithUserID(ctx, "owner-a")
	admin := ipc.WithRole(ipc.WithUserID(ctx, "admin"), "superadmin")

	conversations, err := database.ConvList(regular)
	if err != nil {
		t.Fatal(err)
	}
	requireAIHistoryIDs(t, conversationIDs(conversations), map[string]bool{"conv-a": true})
	runs, err := database.RunList(regular, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	requireAIHistoryIDs(t, runIDs(runs), map[string]bool{"run-a": true})
	active, err := database.RunsActive(regular)
	if err != nil {
		t.Fatal(err)
	}
	requireAIHistoryIDs(t, runIDs(active), map[string]bool{"run-a": true})
	messages, err := database.MsgList(regular, "conv-a")
	if err != nil || len(messages) != 1 || messages[0].ID != "message-a" {
		t.Fatalf("regular messages = %+v, %v", messages, err)
	}
	for _, conversationID := range []string{"conv-b", "conv-orphan"} {
		messages, err := database.MsgList(regular, conversationID)
		if err != nil || len(messages) != 0 {
			t.Fatalf("regular foreign messages for %s = %+v, %v", conversationID, messages, err)
		}
	}
	if _, err := database.ConvGet(regular, "conv-orphan"); err == nil {
		t.Fatal("regular owner read an ownerless conversation")
	}
	for _, runID := range []string{"run-b", "run-orphan"} {
		if _, err := database.RunGet(regular, runID); err == nil {
			t.Fatalf("regular owner read %s", runID)
		}
		if _, err := database.RunEventsAfter(regular, runID, 0); err == nil {
			t.Fatalf("regular owner read events for %s", runID)
		}
	}
	if _, found, err := database.CheckpointGet(regular, "run-orphan"); err != nil || found {
		t.Fatalf("regular ownerless checkpoint found=%v err=%v", found, err)
	}
	hitl, err := database.HitlRunList(regular)
	if err != nil {
		t.Fatal(err)
	}
	requireAIHistoryIDs(t, hitlIDs(hitl), map[string]bool{"run-a": true})

	conversations, err = database.ConvList(admin)
	if err != nil {
		t.Fatal(err)
	}
	requireAIHistoryIDs(t, conversationIDs(conversations), map[string]bool{"conv-orphan": true})
	runs, err = database.RunList(admin, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	requireAIHistoryIDs(t, runIDs(runs), map[string]bool{"run-orphan": true})
	active, err = database.RunsActive(admin)
	if err != nil {
		t.Fatal(err)
	}
	requireAIHistoryIDs(t, runIDs(active), map[string]bool{"run-orphan": true})
	messages, err = database.MsgList(admin, "conv-orphan")
	if err != nil || len(messages) != 1 || messages[0].ID != "message-orphan" {
		t.Fatalf("superadmin quarantine messages = %+v, %v", messages, err)
	}
	messages, err = database.MsgList(admin, "conv-a")
	if err != nil || len(messages) != 0 {
		t.Fatalf("superadmin read another owner's messages: %+v, %v", messages, err)
	}
	if _, err := database.RunGet(admin, "run-orphan"); err != nil {
		t.Fatalf("superadmin could not read quarantine run: %v", err)
	}
	if _, err := database.RunGet(admin, "run-a"); err == nil {
		t.Fatal("superadmin read another owner's run")
	}
	events, err := database.RunEventsAfter(admin, "run-orphan", 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("superadmin quarantine events = %+v, %v", events, err)
	}
	if _, found, err := database.CheckpointGet(admin, "run-orphan"); err != nil || !found {
		t.Fatalf("superadmin quarantine checkpoint found=%v err=%v", found, err)
	}
	hitl, err = database.HitlRunList(admin)
	if err != nil {
		t.Fatal(err)
	}
	requireAIHistoryIDs(t, hitlIDs(hitl), map[string]bool{"run-orphan": true})

	for name, read := range map[string]func() int{
		"conversations": func() int {
			rows, err := database.ConvList(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			return len(rows)
		},
		"runs": func() int {
			rows, err := database.RunList(context.Background(), "", 0)
			if err != nil {
				t.Fatal(err)
			}
			return len(rows)
		},
		"active runs": func() int {
			rows, err := database.RunsActive(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			return len(rows)
		},
		"messages": func() int {
			rows, err := database.MsgList(context.Background(), "conv-a")
			if err != nil {
				t.Fatal(err)
			}
			return len(rows)
		},
		"hitl": func() int {
			rows, err := database.HitlRunList(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			return len(rows)
		},
	} {
		if count := read(); count != 0 {
			t.Fatalf("anonymous account-mode %s returned %d rows", name, count)
		}
	}
	if _, err := database.ConvGet(context.Background(), "conv-a"); err == nil {
		t.Fatal("anonymous account-mode ConvGet succeeded")
	}
	if _, found, err := database.CheckpointGet(context.Background(), "run-a"); err != nil || found {
		t.Fatalf("anonymous account-mode checkpoint found=%v err=%v", found, err)
	}

	internal := WithAIHistoryInternalAccess(context.Background())
	runs, err = database.RunList(internal, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	requireAIHistoryIDs(t, runIDs(runs), map[string]bool{"run-a": true, "run-b": true})
	active, err = database.RunsActive(internal)
	if err != nil {
		t.Fatal(err)
	}
	requireAIHistoryIDs(t, runIDs(active), map[string]bool{"run-a": true, "run-b": true})
	seq, err := database.RunAppendEvent(internal, "run-a", "delta", func(uint64) ([]byte, error) { return []byte("{}"), nil })
	if err != nil || seq != 2 {
		t.Fatalf("internal recovery append seq=%d err=%v", seq, err)
	}
	var appended RunEventRow
	if err := gormDB.Where("run_id = ? AND seq = ?", "run-a", seq).First(&appended).Error; err != nil {
		t.Fatal(err)
	}
	if appended.OwnerID != "owner-a" {
		t.Fatalf("recovery event owner = %q, want parent run owner", appended.OwnerID)
	}
	events, err = database.RunEventsAfter(regular, "run-a", 1)
	if err != nil || len(events) != 1 || events[0].Seq != 2 {
		t.Fatalf("owner could not immediately read recovery event: %+v, %v", events, err)
	}
	if _, err := database.RunAppendEvent(internal, "run-orphan", "delta", func(uint64) ([]byte, error) { return []byte("{}"), nil }); err == nil {
		t.Fatal("internal recovery appended to an ownerless quarantine run")
	}

	database.SetAIHistoryShared(true)
	conversations, err = database.ConvList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	requireAIHistoryIDs(t, conversationIDs(conversations), map[string]bool{"conv-a": true, "conv-b": true, "conv-orphan": true})
	runs, err = database.RunList(internal, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	requireAIHistoryIDs(t, runIDs(runs), map[string]bool{"run-a": true, "run-b": true, "run-orphan": true})
}

func conversationIDs(rows []ConversationRow) map[string]bool {
	ids := make(map[string]bool, len(rows))
	for _, row := range rows {
		ids[row.ID] = true
	}
	return ids
}

func runIDs(rows []RunRow) map[string]bool {
	ids := make(map[string]bool, len(rows))
	for _, row := range rows {
		ids[row.ID] = true
	}
	return ids
}

func hitlIDs(rows []HitlRunRow) map[string]bool {
	ids := make(map[string]bool, len(rows))
	for _, row := range rows {
		ids[row.ID] = true
	}
	return ids
}

func requireAIHistoryIDs(t *testing.T, got, want map[string]bool) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("IDs = %v, want %v", got, want)
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("IDs = %v, want %v", got, want)
		}
	}
}
