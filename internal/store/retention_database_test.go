package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestRetentionBatchSemanticsDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)
	const total = 2*128 + 7
	base := time.Now().Add(-time.Hour)
	audits := make([]store.AuditRow, 0, total)
	recordings := make([]store.RecordingRow, 0, total)
	commands := make([]store.CommandLog, 0, total)
	for i := 0; i < total; i++ {
		ts := base.Add(time.Duration(i) * time.Second).UnixMilli()
		endedAt := ts
		audits = append(audits, store.AuditRow{TS: ts, Source: "user", Kind: "retention-fixture", PayloadJSON: "{}"})
		recordings = append(recordings, store.RecordingRow{
			ID: fmt.Sprintf("recording-%03d", i), SessionID: "session", TabID: "tab", Path: "external.cast",
			Bytes: 1, StartedAt: ts, EndedAt: &endedAt,
		})
		commands = append(commands, store.CommandLog{
			SessionID: "session", TabID: "tab", AssetID: "asset", Command: "retention-fixture", Source: "test",
			StartedAt: ts, FinishedAt: ts,
		})
	}
	for name, rows := range map[string]any{"audit": audits, "recordings": recordings, "commands": commands} {
		if err := db.GORM().WithContext(ctx).CreateInBatches(rows, 64).Error; err != nil {
			t.Fatalf("insert %s fixture: %v", name, err)
		}
	}
	activeEnded := base.UnixMilli()
	if err := db.GORM().WithContext(ctx).Create(&store.RecordingRow{
		ID: "recording-active", SessionID: "session", TabID: "tab", Path: "active.cast", Bytes: 1, StartedAt: activeEnded,
	}).Error; err != nil {
		t.Fatal(err)
	}
	conversation, err := db.ConvCreate(ctx, "retention-contract", map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < total; i++ {
		finishedAt := base.Add(time.Duration(i) * time.Second).UnixMilli()
		if err := db.RunInsert(ctx, store.RunRow{
			ID: fmt.Sprintf("run-finished-%03d", i), ConversationID: conversation.ID, Status: store.RunStatusCompleted,
			CreatedAt: finishedAt, UpdatedAt: finishedAt, FinishedAt: &finishedAt,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []store.RunRow{
		{ID: "run-active", ConversationID: conversation.ID, Status: store.RunStatusRunning, CreatedAt: activeEnded, UpdatedAt: activeEnded},
		{ID: "run-pending", ConversationID: conversation.ID, Status: store.RunStatusInterrupted, CreatedAt: activeEnded, UpdatedAt: activeEnded},
	} {
		if err := db.RunInsert(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"run-pending", "run-finished-000", "run-orphan"} {
		if err := db.HitlRunSave(ctx, id, []byte("{}")); err != nil {
			t.Fatal(err)
		}
	}

	result, err := db.EnforceRetention(ctx, store.RetentionPolicy{
		AuditMaxCount: 4, RecordingMaxCount: 4, CommandMaxCount: 4, AIRunMaxCount: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantDeleted := int64(total - 4)
	if result.AuditDeleted != wantDeleted || result.RecordingsDeleted != wantDeleted ||
		result.CommandsDeleted != wantDeleted || result.AIRunsDeleted != wantDeleted {
		t.Fatalf("retention result = %+v, want %d deletions per table", result, wantDeleted)
	}
	for table, want := range map[string]int{
		"audit_log": 4, "terminal_recording": 5, "command_log": 4, "ai_run": 6, "ai_hitl_run": 1,
	} {
		var count int
		if err := db.DB().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s count = %d, want %d", table, count, want)
		}
	}
	for _, id := range []string{"run-active", "run-pending", "run-finished-259", "run-finished-260", "run-finished-261", "run-finished-262"} {
		if _, err := db.RunGet(ctx, id); err != nil {
			t.Fatalf("expected run %s to survive: %v", id, err)
		}
	}
	if _, err := db.RunGet(ctx, "run-finished-000"); err == nil {
		t.Fatal("oldest finished run survived count retention")
	}
	hitl, err := db.HitlRunList(ctx)
	if err != nil || len(hitl) != 1 || hitl[0].ID != "run-pending" {
		t.Fatalf("HITL rows after retention = %+v, %v", hitl, err)
	}
	repeated, err := db.EnforceRetention(ctx, store.RetentionPolicy{
		AuditMaxCount: 4, RecordingMaxCount: 4, CommandMaxCount: 4, AIRunMaxCount: 4,
	})
	if err != nil || repeated != (store.RetentionResult{}) {
		t.Fatalf("repeated retention result = %+v, %v", repeated, err)
	}
}
