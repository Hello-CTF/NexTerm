package dbtest

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

var requiredTables = []string{
	"asset_group", "asset", "credential", "setting", "audit_log",
	"ai_conversation", "ai_message", "ai_run", "ai_run_event", "ai_checkpoint", "ai_hitl_run",
	"snippet", "known_host", "terminal_recording", "outcome_record", "cron_job",
	"transcript", "transcript_chunk", "durable_transcript_offset", "credential_tombstone",
	"app_user", "user_dek", "user_session", "user_device", "sync_credential",
	"user_setting", "device_enroll_code", "user_sync_object", "user_sync_head",
	"device_agent", "device_metrics", "device_metrics_hourly",
	"sync_tombstone", "sync_state", "share_link", "host_share", "schema_migrations",
	"command_log", "device_share_link", "user_totp", "user_totp_recovery_code",
}

func TestPostgresFromScratchMigration(t *testing.T) {
	fixture := NewFixture(t)
	db := fixture.OpenStore(t)
	if db.Backend() != store.BackendPostgres {
		t.Fatalf("Backend() = %q", db.Backend())
	}
	raw := fixture.OpenRaw(t)
	var count int
	if err := raw.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != 22 {
		t.Fatalf("migration count=%d err=%v", count, err)
	}
	rows, err := raw.Query("SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema()")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	tables := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range requiredTables {
		if !tables[name] {
			t.Errorf("missing table %s", name)
		}
	}
	if tables["sync_tokens"] {
		t.Error("sync_tokens must be dropped by migration 0020")
	}
	var assetFKs int
	if err := raw.QueryRow(`SELECT count(*) FROM information_schema.table_constraints
WHERE table_schema = current_schema() AND table_name = 'asset' AND constraint_type = 'FOREIGN KEY'`).Scan(&assetFKs); err != nil {
		t.Fatal(err)
	}
	if assetFKs != 2 {
		t.Fatalf("asset foreign keys = %d; want 2 (group_id, cred_id)", assetFKs)
	}
}

func TestPostgresIdempotentReopen(t *testing.T) {
	fixture := NewFixture(t)
	first := fixture.OpenStore(t)
	if err := first.SettingSet(context.Background(), "reopen", "kept"); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := fixture.OpenStore(t)
	var count int
	if err := second.DB().QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != 22 {
		t.Fatalf("migration count after reopen=%d err=%v", count, err)
	}
	value, found, err := second.SettingGet(context.Background(), "reopen")
	if err != nil || !found || value != "kept" {
		t.Fatalf("SettingGet = %q, %v, %v", value, found, err)
	}
}

func TestPostgresRejectsChecksumDrift(t *testing.T) {
	fixture := NewFixture(t)
	db := fixture.OpenStore(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw := fixture.OpenRaw(t)
	if _, err := raw.Exec("UPDATE schema_migrations SET checksum = $1 WHERE version = 1", bytes.Repeat([]byte{1}, 32)); err != nil {
		t.Fatal(err)
	}
	_, err := store.OpenBackend(context.Background(), store.BackendConfig{Backend: store.BackendPostgres, DSN: fixture.DSN}, store.OpenOptions{})
	if err == nil {
		t.Fatal("reopen accepted a tampered checksum")
	}
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeDBMigrate {
		t.Fatalf("error = %v; want code %s", err, ipc.CodeDBMigrate)
	}
}

func TestPostgresCoreStoreBehavior(t *testing.T) {
	ctx := context.Background()
	db := NewFixture(t).OpenStore(t)

	if err := db.SettingSet(ctx, "pg.key", "value"); err != nil {
		t.Fatal(err)
	}
	if value, found, err := db.SettingGet(ctx, "pg.key"); err != nil || !found || value != "value" {
		t.Fatalf("SettingGet = %q, %v, %v", value, found, err)
	}

	saved, revision, err := db.LayoutSave(ctx, 0, `{"panes":1}`)
	if err != nil || !saved || revision != 1 {
		t.Fatalf("first layout save = %v, %d, %v", saved, revision, err)
	}
	saved, _, err = db.LayoutSave(ctx, 0, `{"panes":2}`)
	if err != nil || saved {
		t.Fatalf("stale layout save = %v, %v", saved, err)
	}

	group, err := db.GroupCreate(ctx, store.GroupInput{Name: "pg-group", Sort: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AssetCreate(ctx, store.AssetInput{GroupID: &group.ID, Kind: "ssh", Name: "pg-asset", OptionsJSON: "{}"}); err != nil {
		t.Fatal(err)
	}

	credentialID, err := db.CredentialPut(ctx, store.CredentialInput{Name: "pg-cred", Kind: "password", Nonce: []byte{1, 2, 3}, Blob: []byte{4, 5, 6}, KEKHint: "hint"})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := db.CredentialGetRow(ctx, credentialID)
	if err != nil || !bytes.Equal(credential.Nonce, []byte{1, 2, 3}) || !bytes.Equal(credential.Blob, []byte{4, 5, 6}) {
		t.Fatalf("credential roundtrip = %+v, %v", credential, err)
	}

	if _, err := db.AssetCreate(ctx, store.AssetInput{GroupID: &group.ID, Kind: "ssh", Name: "pg-asset-with-cred", CredID: &credentialID, OptionsJSON: "{}"}); err != nil {
		t.Fatalf("asset with existing cred_id: %v", err)
	}
	missingCred := "cred-missing"
	if _, err := db.AssetCreate(ctx, store.AssetInput{GroupID: &group.ID, Kind: "ssh", Name: "pg-asset-bad-cred", CredID: &missingCred, OptionsJSON: "{}"}); err == nil {
		t.Fatal("asset create with dangling cred_id was accepted (FK not enforced)")
	}

	if err := db.CredentialTombstonePut(ctx, "cred-1", 5); err != nil {
		t.Fatal(err)
	}
	if err := db.CredentialTombstonePut(ctx, "cred-1", 3); err != nil {
		t.Fatal(err)
	}
	tombstone, err := db.CredentialTombstoneGet(ctx, "cred-1")
	if err != nil || tombstone.DeletedAt != 5 {
		t.Fatalf("GREATEST regression: tombstone = %+v, %v", tombstone, err)
	}
	if err := db.CredentialTombstonePut(ctx, "cred-1", 7); err != nil {
		t.Fatal(err)
	}
	if tombstone, err = db.CredentialTombstoneGet(ctx, "cred-1"); err != nil || tombstone.DeletedAt != 7 {
		t.Fatalf("tombstone after raise = %+v, %v", tombstone, err)
	}

	if err := db.KnownHostAccept(ctx, "pg-host", 22, "ssh-ed25519", "fingerprint"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := db.KnownHostGet(ctx, "pg-host", 22, "ssh-ed25519"); err != nil || !found {
		t.Fatalf("KnownHostGet found=%v err=%v", found, err)
	}

	conversation, err := db.ConvCreate(ctx, "pg-conversation", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MsgInsert(ctx, conversation.ID, "user", map[string]any{"content": "hello"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.MsgInsert(ctx, conversation.ID, "assistant", map[string]any{"content": "world"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	messages, err := db.MsgList(ctx, conversation.ID)
	if err != nil || len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant" {
		t.Fatalf("messages = %+v, %v", messages, err)
	}

	transcript := store.TranscriptRow{ID: "pg-transcript", SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}
	if err := db.TranscriptStart(ctx, transcript); err != nil {
		t.Fatal(err)
	}
	chunks := []store.TranscriptChunkRow{{Seq: 1, TabID: "tab-1", TS: 1001, Data: []byte("data")}}
	if err := db.TranscriptAppendChunks(ctx, transcript.ID, chunks, map[string]int64{"durable-1": 4}); err != nil {
		t.Fatal(err)
	}
	if err := db.TranscriptEnd(ctx, transcript.ID, 2000, true); err != nil {
		t.Fatal(err)
	}
	ended, err := db.TranscriptGet(ctx, transcript.ID)
	if err != nil || !ended.Truncated || ended.Bytes != 4 || ended.Chunks != 1 {
		t.Fatalf("transcript = %+v, %v", ended, err)
	}
	if offset, err := db.DurableTranscriptOffsetGet(ctx, "durable-1"); err != nil || offset != 4 {
		t.Fatalf("durable offset = %d, %v", offset, err)
	}
	if err := db.DurableTranscriptOffsetSet(ctx, "durable-1", 9); err != nil {
		t.Fatal(err)
	}
	if offset, err := db.DurableTranscriptOffsetGet(ctx, "durable-1"); err != nil || offset != 9 {
		t.Fatalf("durable offset after set = %d, %v", offset, err)
	}

	for i := range 3 {
		if err := db.AuditInsert(ctx, store.AuditInput{Source: "pg", Kind: "audit", Payload: map[string]any{"i": i}}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := db.EnforceRetention(ctx, store.RetentionPolicy{AuditMaxCount: 2})
	if err != nil || result.AuditDeleted != 1 {
		t.Fatalf("retention = %+v, %v", result, err)
	}
	if count, err := db.AuditCount(ctx, store.AuditQuery{}); err != nil || count != 2 {
		t.Fatalf("audit count after retention = %d, %v", count, err)
	}

	for i, endedAt := range []int64{3000, 4000} {
		row := store.TranscriptRow{ID: "pg-transcript-" + string(rune('a'+i)), SessionID: "sess-1", AssetID: "asset-1", StartedAt: endedAt - 1000}
		if err := db.TranscriptStart(ctx, row); err != nil {
			t.Fatal(err)
		}
		if err := db.TranscriptEnd(ctx, row.ID, endedAt, false); err != nil {
			t.Fatal(err)
		}
	}
	transcriptRetention, err := db.EnforceTranscriptRetention(ctx, store.TranscriptRetentionPolicy{MaxCount: 2})
	if err != nil || transcriptRetention.Deleted != 1 {
		t.Fatalf("transcript max-count retention = %+v, %v", transcriptRetention, err)
	}
	remaining, err := db.TranscriptListByAsset(ctx, "asset-1", 10)
	if err != nil || len(remaining) != 2 {
		t.Fatalf("transcripts after retention = %d, %v", len(remaining), err)
	}
}
