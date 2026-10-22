package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func transcriptRetentionDatabaseRow(id, owner string, endedAt *int64, optedIn bool) store.TranscriptRow {
	startedAt := int64(1)
	if endedAt != nil {
		startedAt = *endedAt - 1
	}
	return store.TranscriptRow{
		ID: id, SessionID: "session", AssetID: "asset", StartedAt: startedAt,
		EndedAt: endedAt, SyncOptIn: optedIn, OwnerID: owner,
	}
}

func requireTranscriptRetentionDatabaseCounts(t *testing.T, db *store.Store, owner string, transcripts, tombstones int64) {
	t.Helper()
	var transcriptCount int64
	if err := db.GORM().Model(&store.TranscriptRow{}).Where("owner_id = ?", owner).Count(&transcriptCount).Error; err != nil {
		t.Fatal(err)
	}
	if transcriptCount != transcripts {
		t.Fatalf("owner %s transcript count = %d, want %d", owner, transcriptCount, transcripts)
	}
	var tombstoneCount int64
	if err := db.GORM().Model(&store.SyncTombstone{}).Where("owner_id = ? AND kind = ?", owner, "transcript").Count(&tombstoneCount).Error; err != nil {
		t.Fatal(err)
	}
	if tombstoneCount != tombstones {
		t.Fatalf("owner %s tombstone count = %d, want %d", owner, tombstoneCount, tombstones)
	}
}

func requireTranscriptRetentionDatabaseTombstone(t *testing.T, db *store.Store, owner, id string, want bool) {
	t.Helper()
	var count int64
	if err := db.GORM().Model(&store.SyncTombstone{}).
		Where("owner_id = ? AND id = ? AND kind = ?", owner, id, "transcript").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if (count == 1) != want {
		t.Fatalf("owner %s transcript %s tombstone exists = %v, want %v", owner, id, count == 1, want)
	}
}

func TestTranscriptRetentionBatchSemanticsDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	const total = 2*128 + 7
	now := time.Now()
	oldEndedAt := now.Add(-2 * time.Hour).UnixMilli()
	recentEndedAt := now.UnixMilli()
	rows := make([]store.TranscriptRow, 0, 2*total+4)
	for i := 0; i < total; i++ {
		rows = append(rows,
			transcriptRetentionDatabaseRow(fmt.Sprintf("a-age-%03d", i), "owner-a", &oldEndedAt, i%2 == 0),
			transcriptRetentionDatabaseRow(fmt.Sprintf("a-recent-%03d", i), "owner-a", &recentEndedAt, i%2 == 0),
		)
	}
	for i := 0; i < 3; i++ {
		rows = append(rows, transcriptRetentionDatabaseRow(fmt.Sprintf("b-old-%03d", i), "owner-b", &oldEndedAt, true))
	}
	rows = append(rows, transcriptRetentionDatabaseRow("a-active", "owner-a", nil, true))
	if err := db.GORM().CreateInBatches(rows, 64).Error; err != nil {
		t.Fatal(err)
	}

	policy := store.TranscriptRetentionPolicy{MaxAge: time.Hour, MaxCount: 4}
	result, err := db.EnforceTranscriptRetention(ctxA, policy)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(2*total - 4); result.Deleted != want {
		t.Fatalf("deleted = %d, want %d", result.Deleted, want)
	}
	wantTombstones := int64(0)
	for i := 0; i < total; i++ {
		if i%2 == 0 {
			wantTombstones++
		}
	}
	for i := 0; i < total-4; i++ {
		if i%2 == 0 {
			wantTombstones++
		}
	}
	requireTranscriptRetentionDatabaseCounts(t, db, "owner-a", 5, wantTombstones)
	requireTranscriptRetentionDatabaseCounts(t, db, "owner-b", 3, 0)
	for i := total - 4; i < total; i++ {
		id := fmt.Sprintf("a-recent-%03d", i)
		if _, err := db.TranscriptGet(ctxA, id); err != nil {
			t.Fatalf("newest transcript %s did not survive: %v", id, err)
		}
	}
	if _, err := db.TranscriptGet(ctxA, "a-recent-000"); err == nil {
		t.Fatal("oldest recent transcript survived count retention")
	}
	if _, err := db.TranscriptGet(ctxA, "a-active"); err != nil {
		t.Fatalf("active transcript did not survive: %v", err)
	}
	if _, err := db.TranscriptGet(ctxB, "b-old-000"); err != nil {
		t.Fatalf("other owner transcript was deleted: %v", err)
	}
	requireTranscriptRetentionDatabaseTombstone(t, db, "owner-a", "a-age-000", true)
	requireTranscriptRetentionDatabaseTombstone(t, db, "owner-a", "a-recent-000", true)
	requireTranscriptRetentionDatabaseTombstone(t, db, "owner-a", "a-recent-260", false)
	requireTranscriptRetentionDatabaseTombstone(t, db, "owner-a", "a-active", false)
	requireTranscriptRetentionDatabaseTombstone(t, db, "owner-b", "b-old-000", false)

	repeated, err := db.EnforceTranscriptRetention(ctxA, policy)
	if err != nil || repeated.Deleted != 0 {
		t.Fatalf("repeated result = %+v, %v", repeated, err)
	}
	requireTranscriptRetentionDatabaseCounts(t, db, "owner-a", 5, wantTombstones)
	requireTranscriptRetentionDatabaseCounts(t, db, "owner-b", 3, 0)
}
