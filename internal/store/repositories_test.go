package store

import (
	"context"
	"testing"
)

func TestSettingsAuditAndConversations(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	if _, found, err := db.SettingGet(ctx, "missing"); err != nil || found {
		t.Fatalf("missing setting found=%v err=%v", found, err)
	}
	if err := db.SettingSet(ctx, "key", "one"); err != nil {
		t.Fatal(err)
	}
	if err := db.SettingSet(ctx, "key", "two"); err != nil {
		t.Fatal(err)
	}
	if value, found, _ := db.SettingGet(ctx, "key"); !found || value != "two" {
		t.Fatalf("setting=%q found=%v", value, found)
	}
	if err := db.SettingDelete(ctx, "key"); err != nil {
		t.Fatal(err)
	}

	if err := db.AuditInsert(ctx, AuditInput{Source: "user", Kind: "connect", AssetID: ptr("asset-1"), Payload: map[string]any{"ok": true}}); err != nil {
		t.Fatal(err)
	}
	if err := db.AuditInsert(ctx, AuditInput{Source: "ai", Kind: "exec", Payload: []string{"ls"}}); err != nil {
		t.Fatal(err)
	}
	audits, err := db.AuditQuery(ctx, AuditQuery{Source: ptr("user")})
	if err != nil || len(audits) != 1 || audits[0].AssetID == nil || *audits[0].AssetID != "asset-1" {
		t.Fatalf("audits=%+v err=%v", audits, err)
	}

	conversation, err := db.ConvCreate(ctx, "chat", map[string]any{"assetId": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MsgInsert(ctx, conversation.ID, "user", map[string]any{"content": "hello"}, ptr(int64(3)), nil); err != nil {
		t.Fatal(err)
	}
	messages, err := db.MsgList(ctx, conversation.ID)
	if err != nil || len(messages) != 1 || messages[0].TokensIn == nil || *messages[0].TokensIn != 3 {
		t.Fatalf("messages=%+v err=%v", messages, err)
	}
	if err := db.ConvRename(ctx, conversation.ID, "renamed"); err != nil {
		t.Fatal(err)
	}
	conversations, err := db.ConvList(ctx)
	if err != nil || len(conversations) != 1 || conversations[0].Title != "renamed" {
		t.Fatalf("conversations=%+v err=%v", conversations, err)
	}
	if err := db.ConvDelete(ctx, conversation.ID); err != nil {
		t.Fatal(err)
	}
	messages, err = db.MsgList(ctx, conversation.ID)
	if err != nil || len(messages) != 0 {
		t.Fatalf("orphan messages=%+v err=%v", messages, err)
	}
}

func TestConvDeleteRemovesCronJobs(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	conversation, err := db.ConvCreate(ctx, "chat", nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.ConvCreate(ctx, "other", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, sessionID := range []string{conversation.ID, other.ID} {
		if _, err := db.DB().ExecContext(ctx, `INSERT INTO cron_job (id, session_id, prompt, schedule, timezone, enabled, timeout_ms, created_at, updated_at, revision, next_run_at)
VALUES (?, ?, 'prompt', '0 2 * * *', 'UTC', 1, 60000, 1, 1, 1, 1)`, "job-"+sessionID, sessionID); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.ConvDelete(ctx, conversation.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := db.DB().QueryContext(ctx, "SELECT session_id FROM cron_job")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	remaining := []string{}
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			t.Fatal(err)
		}
		remaining = append(remaining, sessionID)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0] != other.ID {
		t.Fatalf("remaining cron jobs=%v", remaining)
	}
}

func TestSnippetsKnownHostsAndRecordings(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	snippet, err := db.SnippetCreate(ctx, "update", "uname -a", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SnippetUpdate(ctx, snippet.ID, "kernel", "uname -r"); err != nil {
		t.Fatal(err)
	}
	snippets, err := db.SnippetList(ctx)
	if err != nil || len(snippets) != 1 || snippets[0].Body != "uname -r" {
		t.Fatalf("snippets=%+v err=%v", snippets, err)
	}
	if err := db.SnippetDelete(ctx, snippet.ID); err != nil {
		t.Fatal(err)
	}

	if err := db.KnownHostAccept(ctx, "host", 22, "ssh-ed25519", "SHA256:first"); err != nil {
		t.Fatal(err)
	}
	first, found, err := db.KnownHostGet(ctx, "host", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("known host=%+v found=%v err=%v", first, found, err)
	}
	if err := db.KnownHostAccept(ctx, "host", 22, "ssh-ed25519", "SHA256:second"); err != nil {
		t.Fatal(err)
	}
	second, _, _ := db.KnownHostGet(ctx, "host", 22, "ssh-ed25519")
	if second.ID != first.ID || second.Fingerprint != "SHA256:second" {
		t.Fatalf("known host update first=%+v second=%+v", first, second)
	}
	if err := db.KnownHostRemove(ctx, first.ID); err != nil {
		t.Fatal(err)
	}

	recordingID, err := db.RecordingStart(ctx, "session", "tab", "/tmp/recording.cast")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RecordingUpdateBytes(ctx, recordingID, 1234); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordingEnd(ctx, recordingID); err != nil {
		t.Fatal(err)
	}
	recordings, err := db.RecordingList(ctx)
	if err != nil || len(recordings) != 1 || recordings[0].Bytes != 1234 || recordings[0].EndedAt == nil {
		t.Fatalf("recordings=%+v err=%v", recordings, err)
	}
}
