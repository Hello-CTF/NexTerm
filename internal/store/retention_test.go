package store

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func insertAuditAt(t *testing.T, db *Store, ts int64, kind string) {
	t.Helper()
	_, err := db.DB().Exec(`INSERT INTO audit_log(ts, source, kind, payload_json)
VALUES(?, 'user', ?, '{"test":true}')`, ts, kind)
	if err != nil {
		t.Fatal(err)
	}
}

func insertRecordingAt(t *testing.T, db *Store, id, path string, startedAt int64, endedAt *int64) {
	t.Helper()
	_, err := db.DB().Exec(`INSERT INTO terminal_recording(id, session_id, tab_id, path, bytes, started_at, ended_at)
VALUES(?, 'session', 'tab', ?, 128, ?, ?)`, id, path, startedAt, endedAt)
	if err != nil {
		t.Fatal(err)
	}
}

func requireRecordingExists(t *testing.T, db *Store, id string, want bool) {
	t.Helper()
	var count int
	if err := db.DB().QueryRow("SELECT count(*) FROM terminal_recording WHERE id=?", id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if (count == 1) != want {
		t.Fatalf("recording %s exists=%v, want %v", id, count == 1, want)
	}
}

func requireTableCount(t *testing.T, db *Store, table string, want int) {
	t.Helper()
	var count int
	if err := db.DB().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("%s count=%d, want %d", table, count, want)
	}
}

func TestRetentionAgeAndCountPreserveActiveAndExternalFiles(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	now := time.Now()
	for _, row := range []struct {
		kind string
		ts   int64
	}{
		{"old-1", now.Add(-3 * time.Hour).UnixMilli()},
		{"old-2", now.Add(-2 * time.Hour).UnixMilli()},
		{"recent-1", now.Add(-30 * time.Minute).UnixMilli()},
		{"recent-2", now.Add(-20 * time.Minute).UnixMilli()},
		{"recent-3", now.Add(-10 * time.Minute).UnixMilli()},
	} {
		insertAuditAt(t, db, row.ts, row.kind)
	}

	externalDir := t.TempDir()
	paths := make(map[string]string)
	addRecording := func(id string, endedAt *int64) {
		path := filepath.Join(externalDir, id+".cast")
		if err := os.WriteFile(path, []byte("recording-"+id), 0o600); err != nil {
			t.Fatal(err)
		}
		paths[id] = path
		insertRecordingAt(t, db, id, path, now.Add(-4*time.Hour).UnixMilli(), endedAt)
	}
	addRecording("active-old", nil)
	for _, row := range []struct {
		id      string
		endedAt int64
	}{
		{"ended-old-1", now.Add(-3 * time.Hour).UnixMilli()},
		{"ended-old-2", now.Add(-2 * time.Hour).UnixMilli()},
		{"ended-recent-1", now.Add(-30 * time.Minute).UnixMilli()},
		{"ended-recent-2", now.Add(-20 * time.Minute).UnixMilli()},
		{"ended-recent-3", now.Add(-10 * time.Minute).UnixMilli()},
	} {
		endedAt := row.endedAt
		addRecording(row.id, &endedAt)
	}

	policy := RetentionPolicy{
		AuditMaxAge:       time.Hour,
		AuditMaxCount:     2,
		RecordingMaxAge:   time.Hour,
		RecordingMaxCount: 2,
	}
	result, err := db.EnforceRetention(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	if result.AuditDeleted != 3 || result.RecordingsDeleted != 3 {
		t.Fatalf("retention result=%+v, want three deletions per table", result)
	}
	audits, err := db.AuditQuery(ctx, AuditQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 2 || audits[0].Kind != "recent-3" || audits[1].Kind != "recent-2" {
		t.Fatalf("remaining audits=%+v", audits)
	}
	for _, id := range []string{"active-old", "ended-recent-2", "ended-recent-3"} {
		requireRecordingExists(t, db, id, true)
	}
	for _, id := range []string{"ended-old-1", "ended-old-2", "ended-recent-1"} {
		requireRecordingExists(t, db, id, false)
	}
	for id, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("external recording %s: %v", id, err)
		}
		if string(contents) != "recording-"+id {
			t.Fatalf("external recording %s was modified: %q", id, contents)
		}
	}
	recordings, err := db.RecordingList(ctx)
	if err != nil || len(recordings) != 3 {
		t.Fatalf("recording metadata list=%+v err=%v", recordings, err)
	}

	repeated, err := db.EnforceRetention(ctx, policy)
	if err != nil || repeated != (RetentionResult{}) {
		t.Fatalf("repeat result=%+v err=%v", repeated, err)
	}
	status := db.RetentionStatus()
	if status.LastError != "" || status.LastAttemptAt.IsZero() || status.LastSuccessAt.IsZero() {
		t.Fatalf("retention status=%+v", status)
	}
}

func TestRetentionRollbackLogsAndRecovers(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	db, err := OpenInMemoryWithOptions(ctx, OpenOptions{Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	old := time.Now().Add(-2 * time.Hour).UnixMilli()
	insertAuditAt(t, db, old, "expired")
	insertRecordingAt(t, db, "expired", filepath.Join(t.TempDir(), "external.cast"), old, &old)
	_, err = db.DB().Exec(`CREATE TRIGGER fail_recording_retention BEFORE DELETE ON terminal_recording
BEGIN SELECT RAISE(ABORT, 'injected retention failure'); END`)
	if err != nil {
		t.Fatal(err)
	}

	policy := RetentionPolicy{AuditMaxAge: time.Hour, RecordingMaxAge: time.Hour}
	if _, err := db.EnforceRetention(ctx, policy); err == nil {
		t.Fatal("expected injected cleanup failure")
	}
	requireTableCount(t, db, "audit_log", 1)
	requireTableCount(t, db, "terminal_recording", 1)
	status := db.RetentionStatus()
	if !strings.Contains(status.LastError, "injected retention failure") || status.LastAttemptAt.IsZero() || !status.LastSuccessAt.IsZero() {
		t.Fatalf("failure status=%+v", status)
	}
	if output := logs.String(); !strings.Contains(output, "store retention cleanup failed") || !strings.Contains(output, "injected retention failure") {
		t.Fatalf("cleanup logs=%q", output)
	}

	if _, err := db.DB().Exec("DROP TRIGGER fail_recording_retention"); err != nil {
		t.Fatal(err)
	}
	result, err := db.EnforceRetention(ctx, policy)
	if err != nil || result.AuditDeleted != 1 || result.RecordingsDeleted != 1 {
		t.Fatalf("recovery result=%+v err=%v", result, err)
	}
	requireTableCount(t, db, "audit_log", 0)
	requireTableCount(t, db, "terminal_recording", 0)
	status = db.RetentionStatus()
	if status.LastError != "" || status.LastSuccessAt.IsZero() {
		t.Fatalf("recovery status=%+v", status)
	}
}

func TestRetentionDisabledAndRejectsNegativeLimits(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := OpenInMemoryWithOptions(ctx, OpenOptions{Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	old := time.Now().Add(-2 * time.Hour).UnixMilli()
	insertAuditAt(t, db, old, "old")
	insertRecordingAt(t, db, "old", "external.cast", old, &old)
	if result, err := db.EnforceRetention(ctx, RetentionPolicy{}); err != nil || result != (RetentionResult{}) {
		t.Fatalf("disabled result=%+v err=%v", result, err)
	}
	requireTableCount(t, db, "audit_log", 1)
	requireTableCount(t, db, "terminal_recording", 1)

	for name, policy := range map[string]RetentionPolicy{
		"audit age":       {AuditMaxAge: -time.Second},
		"audit count":     {AuditMaxCount: -1},
		"recording age":   {RecordingMaxAge: -time.Second},
		"recording count": {RecordingMaxCount: -1},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := db.EnforceRetention(ctx, policy)
			requireCode(t, err, ipc.CodeBadParam)
			if db.RetentionStatus().LastError == "" {
				t.Fatal("validation failure was not health-visible")
			}
		})
	}
}

func TestRetentionSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour).UnixMilli()
	insertAuditAt(t, db, old, "old")
	insertRecordingAt(t, db, "ended-old", "external.cast", old, &old)
	insertRecordingAt(t, db, "active-old", "active.cast", old, nil)
	policy := RetentionPolicy{AuditMaxAge: time.Hour, RecordingMaxAge: time.Hour}
	if result, err := db.EnforceRetention(ctx, policy); err != nil || result.AuditDeleted != 1 || result.RecordingsDeleted != 1 {
		t.Fatalf("first result=%+v err=%v", result, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	requireTableCount(t, reopened, "audit_log", 0)
	requireRecordingExists(t, reopened, "ended-old", false)
	requireRecordingExists(t, reopened, "active-old", true)
	if result, err := reopened.EnforceRetention(ctx, policy); err != nil || result != (RetentionResult{}) {
		t.Fatalf("post-restart result=%+v err=%v", result, err)
	}
}

func TestRetentionMigrationPreservesStoreAndVaultRows(t *testing.T) {
	ctx := context.Background()
	db, path := fileStore(t)
	credentialID := "credential-retention-migration"
	if _, err := db.CredentialPut(ctx, CredentialInput{
		ID: credentialID, Name: "kept", Kind: "password", Nonce: []byte{1, 2, 3}, Blob: []byte{4, 5, 6}, KEKHint: "master:test",
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	insertAuditAt(t, db, now, "kept")
	insertRecordingAt(t, db, "kept", "external.cast", now, nil)
	_, err := db.DB().Exec(`CREATE INDEX idx_audit_ts ON audit_log(ts DESC);
DROP INDEX idx_audit_retention;
DROP INDEX idx_terminal_recording_retention;
DROP INDEX idx_ai_msg_conv_seq;
ALTER TABLE ai_message DROP COLUMN seq;
DROP TABLE ai_run_event;
DROP TABLE ai_run;
DROP TABLE ai_checkpoint;
DROP TABLE ai_hitl_run;
DROP TABLE transcript_chunk;
DROP TABLE transcript;
DROP TABLE durable_transcript_offset;
DELETE FROM schema_migrations WHERE version IN (3, 4, 5, 6, 7, 8, 9)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	credential, err := reopened.CredentialGetRow(ctx, credentialID)
	if err != nil || !bytes.Equal(credential.Nonce, []byte{1, 2, 3}) || !bytes.Equal(credential.Blob, []byte{4, 5, 6}) || credential.KEKHint != "master:test" {
		t.Fatalf("credential=%+v err=%v", credential, err)
	}
	requireTableCount(t, reopened, "audit_log", 1)
	requireRecordingExists(t, reopened, "kept", true)
	for _, index := range []string{"idx_audit_retention", "idx_terminal_recording_retention"} {
		var count int
		if err := reopened.DB().QueryRow("SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?", index).Scan(&count); err != nil || count != 1 {
			t.Fatalf("index %s count=%d err=%v", index, count, err)
		}
	}
	requireTableCount(t, reopened, migrationsTable, 9)
}

func TestRetentionConcurrentWithRecordingEnd(t *testing.T) {
	ctx := context.Background()
	db, _ := fileStore(t)
	old := time.Now().Add(-2 * time.Hour).UnixMilli()
	insertRecordingAt(t, db, "expired", "expired.cast", old, &old)
	const activeCount = 8
	for i := 0; i < activeCount; i++ {
		insertRecordingAt(t, db, fmt.Sprintf("active-%d", i), "active.cast", old, nil)
	}

	start := make(chan struct{})
	errors := make(chan error, activeCount+32)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 8; j++ {
				if _, err := db.EnforceRetention(ctx, RetentionPolicy{RecordingMaxAge: time.Hour}); err != nil {
					errors <- err
					return
				}
			}
		}()
	}
	for i := 0; i < activeCount; i++ {
		id := fmt.Sprintf("active-%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := db.RecordingEnd(ctx, id); err != nil {
				errors <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	requireRecordingExists(t, db, "expired", false)
	for i := 0; i < activeCount; i++ {
		requireRecordingExists(t, db, fmt.Sprintf("active-%d", i), true)
	}
}
