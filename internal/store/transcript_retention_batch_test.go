package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func transcriptRetentionFixtureRow(id, owner string, endedAt *int64, optedIn bool) TranscriptRow {
	startedAt := int64(1)
	if endedAt != nil {
		startedAt = *endedAt - 1
	}
	return TranscriptRow{
		ID: id, SessionID: "session", AssetID: "asset", StartedAt: startedAt,
		EndedAt: endedAt, SyncOptIn: optedIn, OwnerID: owner,
	}
}

func requireTranscriptRetentionOwnerCounts(t *testing.T, db *Store, owner string, transcripts, tombstones int64) {
	t.Helper()
	var transcriptCount int64
	if err := db.GORM().Model(&TranscriptRow{}).Where("owner_id = ?", owner).Count(&transcriptCount).Error; err != nil {
		t.Fatal(err)
	}
	if transcriptCount != transcripts {
		t.Fatalf("owner %s transcript count = %d, want %d", owner, transcriptCount, transcripts)
	}
	var tombstoneCount int64
	if err := db.GORM().Model(&SyncTombstone{}).Where("owner_id = ? AND kind = ?", owner, "transcript").Count(&tombstoneCount).Error; err != nil {
		t.Fatal(err)
	}
	if tombstoneCount != tombstones {
		t.Fatalf("owner %s tombstone count = %d, want %d", owner, tombstoneCount, tombstones)
	}
}

func TestTranscriptRetentionAgeAndCountUseBoundedTransactions(t *testing.T) {
	const total = 2*retentionBatchSize + 17
	for _, test := range []struct {
		name       string
		policy     TranscriptRetentionPolicy
		wantDelete int64
		wantOwnerA int64
		wantTombs  int64
	}{
		{name: "age", policy: TranscriptRetentionPolicy{MaxAge: time.Hour}, wantDelete: total, wantOwnerA: 1, wantTombs: 137},
		{name: "count", policy: TranscriptRetentionPolicy{MaxCount: 4}, wantDelete: total - 4, wantOwnerA: 5, wantTombs: 135},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, _ := fileStore(t)
			ctx := ipc.WithUserID(context.Background(), "owner-a")
			now := time.Now()
			rows := make([]TranscriptRow, 0, total+4)
			for i := 0; i < total; i++ {
				endedAt := now.UnixMilli()
				if test.name == "age" {
					endedAt = now.Add(-2 * time.Hour).UnixMilli()
				}
				id := fmt.Sprintf("%s-%03d", test.name, i)
				rows = append(rows, transcriptRetentionFixtureRow(id, "owner-a", &endedAt, i%2 == 0))
			}
			for i := 0; i < 3; i++ {
				endedAt := now.Add(-2 * time.Hour).UnixMilli()
				rows = append(rows, transcriptRetentionFixtureRow(fmt.Sprintf("other-%03d", i), "owner-b", &endedAt, true))
			}
			rows = append(rows, transcriptRetentionFixtureRow("active", "owner-a", nil, true))
			if err := db.GORM().CreateInBatches(rows, 64).Error; err != nil {
				t.Fatal(err)
			}

			recorder := &retentionTransactionRecorder{delay: 2 * time.Millisecond}
			recorder.install(db)
			started := time.Now()
			result, err := db.EnforceTranscriptRetention(ctx, test.policy)
			if err != nil {
				t.Fatal(err)
			}
			if result.Deleted != test.wantDelete {
				t.Fatalf("deleted = %d, want %d", result.Deleted, test.wantDelete)
			}
			requireTranscriptRetentionOwnerCounts(t, db, "owner-a", test.wantOwnerA, test.wantTombs)
			requireTranscriptRetentionOwnerCounts(t, db, "owner-b", 3, 0)
			assertNoSyncTombstone(t, db, "active")
			assertNoSyncTombstone(t, db, "other-000")
			if test.name == "count" {
				for i := total - 4; i < total; i++ {
					id := fmt.Sprintf("count-%03d", i)
					if _, err := db.TranscriptGet(ctx, id); err != nil {
						t.Fatalf("newest transcript %s did not survive: %v", id, err)
					}
				}
			}

			samples, maxActive := recorder.snapshot()
			var maxDeleted int64
			var maxTransaction, totalTransaction, maxBeginWait time.Duration
			for _, sample := range samples {
				if !sample.committed {
					t.Fatalf("unexpected rolled-back transaction: %+v", sample)
				}
				if sample.deleted > maxDeleted {
					maxDeleted = sample.deleted
				}
				if sample.duration > maxTransaction {
					maxTransaction = sample.duration
				}
				if sample.beginWait > maxBeginWait {
					maxBeginWait = sample.beginWait
				}
				totalTransaction += sample.duration
			}
			t.Logf("transcript retention metrics mode=%s wall=%s transactions=%d maxDeletedPerTx=%d totalTx=%s maxTx=%s maxBeginWait=%s maxActive=%d",
				test.name, time.Since(started), len(samples), maxDeleted, totalTransaction, maxTransaction, maxBeginWait, maxActive)
			if len(samples) < 3 {
				t.Fatalf("%s retention used %d transaction(s), want at least 3", test.name, len(samples))
			}
			if maxDeleted > retentionBatchSize {
				t.Fatalf("largest transaction deleted %d rows, want <= %d", maxDeleted, retentionBatchSize)
			}
			if maxTransaction >= totalTransaction {
				t.Fatalf("one transaction covers the complete cleanup: max=%s total=%s", maxTransaction, totalTransaction)
			}
			if maxActive != 1 {
				t.Fatalf("maximum active transactions = %d, want 1", maxActive)
			}

			repeated, err := db.EnforceTranscriptRetention(ctx, test.policy)
			if err != nil || repeated.Deleted != 0 {
				t.Fatalf("repeated result = %+v, %v", repeated, err)
			}
			requireTranscriptRetentionOwnerCounts(t, db, "owner-a", test.wantOwnerA, test.wantTombs)
			requireTranscriptRetentionOwnerCounts(t, db, "owner-b", 3, 0)
		})
	}
}

func TestTranscriptRetentionSecondBatchFailureRollsBackOnlyThatBatch(t *testing.T) {
	db := testStore(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	const total = 2*retentionBatchSize + 17
	old := time.Now().Add(-2 * time.Hour).UnixMilli()
	rows := make([]TranscriptRow, 0, total)
	for i := 0; i < total; i++ {
		rows = append(rows, transcriptRetentionFixtureRow(fmt.Sprintf("age-%03d", i), "owner-a", &old, i%2 == 0))
	}
	if err := db.GORM().CreateInBatches(rows, 64).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`CREATE TRIGGER fail_second_transcript_batch BEFORE DELETE ON transcript
WHEN OLD.id = 'age-128'
BEGIN SELECT RAISE(ABORT, 'injected second transcript batch failure'); END`); err != nil {
		t.Fatal(err)
	}
	policy := TranscriptRetentionPolicy{MaxAge: time.Hour}
	if _, err := db.EnforceTranscriptRetention(ctx, policy); err == nil {
		t.Fatal("expected second transcript batch failure")
	}
	requireTranscriptRetentionOwnerCounts(t, db, "owner-a", total-retentionBatchSize, retentionBatchSize/2)
	if _, err := db.TranscriptGet(ctx, "age-000"); err == nil {
		t.Fatal("first committed batch was rolled back")
	}
	assertSyncTombstone(t, db, "age-000", "transcript")
	if _, err := db.TranscriptGet(ctx, "age-128"); err != nil {
		t.Fatalf("failed batch row is missing: %v", err)
	}
	assertNoSyncTombstone(t, db, "age-128")

	if _, err := db.DB().Exec("DROP TRIGGER fail_second_transcript_batch"); err != nil {
		t.Fatal(err)
	}
	result, err := db.EnforceTranscriptRetention(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != total-retentionBatchSize {
		t.Fatalf("resumed deletion = %d, want %d", result.Deleted, total-retentionBatchSize)
	}
	requireTranscriptRetentionOwnerCounts(t, db, "owner-a", 0, 137)
	assertSyncTombstone(t, db, "age-128", "transcript")
	repeated, err := db.EnforceTranscriptRetention(ctx, policy)
	if err != nil || repeated.Deleted != 0 {
		t.Fatalf("repeated result = %+v, %v", repeated, err)
	}
	requireTranscriptRetentionOwnerCounts(t, db, "owner-a", 0, 137)
}
