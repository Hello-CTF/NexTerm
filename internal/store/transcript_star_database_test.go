package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func createEndedTranscripts(t *testing.T, db *store.Store, rows ...store.TranscriptRow) {
	t.Helper()
	for index := range rows {
		rows[index].SessionID = "session-" + rows[index].ID
		rows[index].AssetID = "asset-1"
	}
	if err := db.GORM().WithContext(context.Background()).Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
}

func transcriptIDs(t *testing.T, db *store.Store) map[string]bool {
	t.Helper()
	var ids []string
	if err := db.GORM().Model(&store.TranscriptRow{}).Pluck("id", &ids).Error; err != nil {
		t.Fatal(err)
	}
	result := map[string]bool{}
	for _, id := range ids {
		result[id] = true
	}
	return result
}

func TestTranscriptRetentionKeepsStarredDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)
	now := time.Now().UnixMilli()
	old := now - 48*time.Hour.Milliseconds()
	ended := func(offset int64) *int64 { value := now - offset; return &value }
	createEndedTranscripts(t, db,
		store.TranscriptRow{ID: "old-plain", StartedAt: old - 1, EndedAt: &old},
		store.TranscriptRow{ID: "old-starred", StartedAt: old - 1, EndedAt: &old, Starred: true},
		store.TranscriptRow{ID: "old-starred-synced", StartedAt: old - 1, EndedAt: &old, Starred: true, SyncOptIn: true},
	)
	if _, err := db.EnforceTranscriptRetention(ctx, store.TranscriptRetentionPolicy{MaxAge: 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	if got := transcriptIDs(t, db); got["old-plain"] || !got["old-starred"] || !got["old-starred-synced"] {
		t.Fatalf("age retention must delete only the unstarred transcript: %v", got)
	}

	// Newest first: two starred transcripts are the newest, and must not use up the two kept places.
	createEndedTranscripts(t, db,
		store.TranscriptRow{ID: "new-starred-1", StartedAt: now - 10, EndedAt: ended(1), Starred: true},
		store.TranscriptRow{ID: "new-starred-2", StartedAt: now - 10, EndedAt: ended(2), Starred: true},
		store.TranscriptRow{ID: "plain-1", StartedAt: now - 10, EndedAt: ended(3)},
		store.TranscriptRow{ID: "plain-2", StartedAt: now - 10, EndedAt: ended(4)},
		store.TranscriptRow{ID: "plain-3", StartedAt: now - 10, EndedAt: ended(5)},
	)
	if _, err := db.EnforceTranscriptRetention(ctx, store.TranscriptRetentionPolicy{MaxCount: 2}); err != nil {
		t.Fatal(err)
	}
	got := transcriptIDs(t, db)
	for _, id := range []string{"new-starred-1", "new-starred-2", "old-starred", "old-starred-synced", "plain-1", "plain-2"} {
		if !got[id] {
			t.Fatalf("count retention deleted %s: %v", id, got)
		}
	}
	if got["plain-3"] {
		t.Fatalf("count retention must keep the two newest unstarred transcripts only: %v", got)
	}
}

func TestTranscriptPurgeAndUsageSkipStarredAndActiveDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)
	now := time.Now().UnixMilli()
	old := now - 10*24*time.Hour.Milliseconds()
	recent := now - time.Hour.Milliseconds()
	createEndedTranscripts(t, db,
		store.TranscriptRow{ID: "old-plain", StartedAt: old - 1, EndedAt: &old, StoredBytes: 100},
		store.TranscriptRow{ID: "old-starred", StartedAt: old - 1, EndedAt: &old, StoredBytes: 10, Starred: true},
		store.TranscriptRow{ID: "recent-plain", StartedAt: recent - 1, EndedAt: &recent, StoredBytes: 1},
		store.TranscriptRow{ID: "active", StartedAt: old, StoredBytes: 1000},
	)
	cutoff := now - 7*24*time.Hour.Milliseconds()
	usage, err := db.TranscriptUsageBefore(ctx, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	want := store.TranscriptUsage{Total: 4, Starred: 1, StoredBytes: 1111, Purgeable: 1, PurgeableBytes: 100}
	if usage != want {
		t.Fatalf("usage = %+v, want %+v", usage, want)
	}
	deleted, err := db.TranscriptPurgeEndedBefore(ctx, cutoff)
	if err != nil || deleted != 1 {
		t.Fatalf("purge deleted %d, err %v", deleted, err)
	}
	got := transcriptIDs(t, db)
	if got["old-plain"] || !got["old-starred"] || !got["recent-plain"] || !got["active"] {
		t.Fatalf("purge must delete only the old unstarred ended transcript: %v", got)
	}
	// Purging everything ended up to now still keeps starred and active transcripts.
	if _, err := db.TranscriptPurgeEndedBefore(ctx, now+1); err != nil {
		t.Fatal(err)
	}
	if got := transcriptIDs(t, db); len(got) != 2 || !got["old-starred"] || !got["active"] {
		t.Fatalf("a full purge must keep starred and active transcripts: %v", got)
	}
}

func TestTranscriptStarredListIsNotCappedDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)
	now := time.Now().UnixMilli()
	rows := make([]store.TranscriptRow, 0, 205)
	for index := 0; index < 205; index++ {
		ended := now - int64(index)
		rows = append(rows, store.TranscriptRow{ID: fmt.Sprintf("t-%03d", index), StartedAt: ended - 1, EndedAt: &ended})
	}
	createEndedTranscripts(t, db, rows...)
	// The oldest transcript is past the 200-row recent list; starring it must still list it.
	if err := db.TranscriptSetStarred(ctx, "t-204", true); err != nil {
		t.Fatal(err)
	}
	recent, err := db.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range recent {
		if row.ID == "t-204" {
			t.Fatal("fixture must put the starred transcript outside the recent list")
		}
	}
	starred, err := db.TranscriptListStarredByAsset(ctx, "asset-1")
	if err != nil || len(starred) != 1 || starred[0].ID != "t-204" || !starred[0].Starred {
		t.Fatalf("starred list = %+v, err %v", starred, err)
	}
	if err := db.TranscriptSetStarred(ctx, "missing", true); err == nil {
		t.Fatal("starring a missing transcript must fail")
	}
}
