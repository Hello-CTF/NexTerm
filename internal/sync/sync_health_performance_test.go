package sync

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"gorm.io/gorm"
)

func TestServiceSyncErrorThenSuccessClearsHealth(t *testing.T) {
	instance := newTestInstance(t, true)
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-password")
	ctx := context.Background()
	if _, err := instance.service.LinkSet(ctx, LinkPatch{
		URL: testPtr(server.URL), Username: testPtr("alice"), Password: testPtr("alice-password"),
	}); err != nil {
		t.Fatal(err)
	}
	originalHandler := server.Config.Handler
	failIDs := true
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failIDs && r.URL.Path == "/sync/v2/ids" {
			http.Error(w, "injected sync outage", http.StatusInternalServerError)
			return
		}
		originalHandler.ServeHTTP(w, r)
	})
	if _, err := instance.service.Sync(ctx); err == nil {
		t.Fatal("expected injected sync failure")
	}
	failed, err := instance.service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if failed.LastError == "" || failed.LastAttemptAt.IsZero() || failed.Paused {
		t.Fatalf("failure status = %+v, want an attempted non-paused failure", failed)
	}

	failIDs = false
	if _, err := instance.service.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := instance.service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.LastError != "" || recovered.LastSuccessAt.IsZero() || recovered.Paused {
		t.Fatalf("recovery status = %+v, want successful health", recovered)
	}
	if recovered.LastAttemptAt.Before(failed.LastAttemptAt) {
		t.Fatalf("attempt timestamp went backwards: %+v -> %+v", failed, recovered)
	}
}

func TestServiceStatusVaultLockedAndRecovery(t *testing.T) {
	instance := newMasterTestInstance(t)
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-password")
	ctx := context.Background()
	if _, err := instance.service.LinkSet(ctx, LinkPatch{
		URL: testPtr(server.URL), Username: testPtr("alice"), Password: testPtr("alice-password"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.service.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := instance.service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}

	instance.vault.Lock()
	instance.service.syncBackground(ctx)
	paused, err := instance.service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !paused.Configured || !paused.Paused || !paused.VaultLocked {
		t.Fatalf("locked status = %+v, want configured paused vault-locked status", paused)
	}
	if paused.LastError == "" || !paused.LastSuccessAt.Equal(before.LastSuccessAt) {
		t.Fatalf("locked status lost diagnostic or success history: %+v", paused)
	}

	if err := instance.vault.UnlockMaster(ctx, "sync-test-master"); err != nil {
		t.Fatal(err)
	}
	instance.service.syncBackground(ctx)
	recovered, err := instance.service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Paused || recovered.VaultLocked || recovered.LastError != "" {
		t.Fatalf("status did not recover after unlock: %+v", recovered)
	}
	if recovered.LastSuccessAt.Before(before.LastSuccessAt) {
		t.Fatalf("success timestamp went backwards: %+v -> %+v", before, recovered)
	}
}

func TestServiceStatusWarningsBelongToLatestRound(t *testing.T) {
	instance := newTestInstance(t, true)
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-password")
	ctx := context.Background()
	credentialID := ids.New()
	putTestCredential(t, instance, credentialID, "损坏凭据", "password", "secret")
	if _, err := instance.db.DB().ExecContext(ctx, "UPDATE credential SET blob=? WHERE id=?", []byte("tampered"), credentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.service.LinkSet(ctx, LinkPatch{
		URL: testPtr(server.URL), Username: testPtr("alice"), Password: testPtr("alice-password"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.service.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	withWarnings, err := instance.service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(withWarnings.Warnings) == 0 {
		t.Fatal("decrypt failure should be exposed as a warning from the latest round")
	}

	if err := instance.db.CredentialDelete(ctx, credentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.service.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	withoutWarnings, err := instance.service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutWarnings.Warnings) != 0 || withoutWarnings.LastError != "" {
		t.Fatalf("stale warning survived a clean round: %+v", withoutWarnings)
	}
}

func TestTranscriptPayloadCacheReusesBytesAndSkipsChunkRead(t *testing.T) {
	device := newTestDevice(t)
	ctx := context.Background()
	transcriptID := ids.New()
	endedAt := int64(200)
	putDeviceTranscript(t, device, transcriptID, 100, &endedAt, []byte("cached transcript output"), true)
	row, err := device.db.TranscriptGet(ctx, transcriptID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := device.engine.transcriptPayload(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	firstHash := device.engine.localPayloadHash(ctx, transcriptID, localObject{kind: KindTranscript, plaintext: first})
	if _, err := device.db.DB().ExecContext(ctx, "DELETE FROM transcript_chunk WHERE transcript_id=?", transcriptID); err != nil {
		t.Fatal(err)
	}

	second, err := device.engine.transcriptPayload(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || len(first) == 0 || &first[0] != &second[0] {
		t.Fatal("unchanged transcript did not reuse its original payload bytes")
	}
	secondHash := device.engine.localPayloadHash(ctx, transcriptID, localObject{kind: KindTranscript, plaintext: second})
	if firstHash != secondHash {
		t.Fatalf("cached payload hash changed: %q -> %q", firstHash, secondHash)
	}
}

func TestEngineSyncStateQueriesAreBatched(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	device := newTestDevice(t)
	for range 3 {
		putDeviceGroup(t, device, ids.New(), nil, "batch", 100)
	}
	queries := []string{}
	if err := device.db.GORM().Callback().Query().After("gorm:query").Register("test:sync-state-batch", func(tx *gorm.DB) {
		sql := tx.Statement.SQL.String()
		if strings.Contains(sql, "sync_state") {
			queries = append(queries, sql)
		}
	}); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, device, server, "alice", "alice-pw-123")
	if len(queries) == 0 || len(queries) > 2 {
		t.Fatalf("sync_state SELECT count = %d, want one or two batch reads: %v", len(queries), queries)
	}
	for _, sql := range queries {
		where := strings.ToLower(sql)
		if index := strings.Index(where, "where"); index >= 0 && strings.Contains(where[index:], "object_id") {
			t.Fatalf("per-object sync_state query remains: %s", sql)
		}
	}
}
