package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestTranscriptAppendChunksDurableOffsetDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	transcript := store.TranscriptRow{ID: "pg-durable-transcript", SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}
	if err := db.TranscriptStart(ctx, transcript); err != nil {
		t.Fatal(err)
	}
	if err := db.TranscriptAppendChunks(ctx, transcript.ID,
		[]store.TranscriptChunkRow{{Seq: 1, TabID: "tab-1", TS: 1001, Data: []byte("abcd")}},
		map[string]int64{"durable-1": 4}); err != nil {
		t.Fatal(err)
	}
	if offset, err := db.DurableTranscriptOffsetGet(ctx, "durable-1"); err != nil || offset != 4 {
		t.Fatalf("offset after insert = %d, %v", offset, err)
	}
	if err := db.TranscriptAppendChunks(ctx, transcript.ID,
		[]store.TranscriptChunkRow{{Seq: 2, TabID: "tab-1", TS: 1002, Data: []byte("efgh")}},
		map[string]int64{"durable-1": 4}); err != nil {
		t.Fatal(err)
	}
	if offset, err := db.DurableTranscriptOffsetGet(ctx, "durable-1"); err != nil || offset != 8 {
		t.Fatalf("offset after conflict accumulate = %d, %v", offset, err)
	}
}

func TestDurableTabTitleRoundtripDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	if err := db.DurableTabTitleSet(ctx, "pg-title-tab", "构建机"); err != nil {
		t.Fatal(err)
	}
	if title, err := db.DurableTabTitleGet(ctx, "pg-title-tab"); err != nil || title != "构建机" {
		t.Fatalf("title after insert = %q, %v", title, err)
	}
	if err := db.DurableTabTitleSet(ctx, "pg-title-tab", "部署终端"); err != nil {
		t.Fatal(err)
	}
	if title, err := db.DurableTabTitleGet(ctx, "pg-title-tab"); err != nil || title != "部署终端" {
		t.Fatalf("title after conflict update = %q, %v", title, err)
	}
	if err := db.DurableTabTitleDelete(ctx, "pg-title-tab"); err != nil {
		t.Fatal(err)
	}
	if title, err := db.DurableTabTitleGet(ctx, "pg-title-tab"); err != nil || title != "" {
		t.Fatalf("title after delete = %q, %v", title, err)
	}
}

func TestTranscriptHostsGroupByDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	row := store.TranscriptRow{ID: "pg-hosts-transcript", SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}
	if err := db.TranscriptStart(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := db.TranscriptEnd(ctx, row.ID, 2000, false); err != nil {
		t.Fatal(err)
	}
	deviceAssetID := "device/01JDEVICE00000000000000001"
	device := store.TranscriptRow{ID: "pg-device-hosts-transcript", SessionID: "sess-2", AssetID: deviceAssetID, StartedAt: 3000}
	if err := db.TranscriptStart(ctx, device); err != nil {
		t.Fatal(err)
	}
	if err := db.TranscriptEnd(ctx, device.ID, 4000, false); err != nil {
		t.Fatal(err)
	}
	hosts, err := db.TranscriptHosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 {
		t.Fatalf("hosts = %+v", hosts)
	}
	byID := make(map[string]store.TranscriptHostRow, len(hosts))
	for _, host := range hosts {
		byID[host.AssetID] = host
	}
	if host := byID["asset-1"]; !host.AssetDeleted || host.Transcripts != 1 {
		t.Fatalf("hard-missing asset must remain deleted: %+v", host)
	}
	if host, ok := byID[deviceAssetID]; !ok || host.AssetDeleted || host.Transcripts != 1 {
		t.Fatalf("device pseudo asset must not be marked deleted: %+v", host)
	}
}

func TestTranscriptInsertSyncedConflictBranchDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	ended := int64(2000)
	row := store.TranscriptRow{ID: "pg-synced-transcript", SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000, EndedAt: &ended, Bytes: 4, Chunks: 1}
	chunks := []store.TranscriptChunkRow{{Seq: 0, TabID: "tab-1", TS: 1001, Data: []byte("abcd")}}
	if err := db.TranscriptInsertSynced(ctx, row, chunks); err != nil {
		t.Fatal(err)
	}
	row.Bytes = 8
	row.Chunks = 2
	if err := db.TranscriptInsertSynced(ctx, row, chunks); err != nil {
		t.Fatalf("conflict upsert: %v", err)
	}
	got, err := db.TranscriptGet(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bytes != 8 || got.Chunks != 2 || got.StoredBytes <= 0 || !got.SyncOptIn {
		t.Fatalf("after conflict upsert = %+v", got)
	}
}

func TestTranscriptRetentionBackgroundOwnersDatabase(t *testing.T) {
	// Background retention (no owner in the context) must delete and tombstone
	// every owner's rows under that row's own owner on each supported backend.
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	now := time.Now()
	old := now.Add(-2 * time.Hour).UnixMilli()
	recentA := now.Add(-10 * time.Minute).UnixMilli()
	recentB := now.Add(-5 * time.Minute).UnixMilli()
	recentC := now.Add(-2 * time.Minute).UnixMilli()
	rows := []store.TranscriptRow{
		transcriptRetentionDatabaseRow("bg-a-old-0", "owner-a", &old, true),
		transcriptRetentionDatabaseRow("bg-a-old-1", "owner-a", &old, true),
		transcriptRetentionDatabaseRow("bg-a-old-2", "owner-a", &old, false),
		transcriptRetentionDatabaseRow("bg-b-old-0", "owner-b", &old, true),
		transcriptRetentionDatabaseRow("bg-b-old-1", "owner-b", &old, true),
		transcriptRetentionDatabaseRow("bg-a-recent", "owner-a", &recentA, true),
		transcriptRetentionDatabaseRow("bg-b-recent", "owner-b", &recentB, false),
		transcriptRetentionDatabaseRow("bg-c-recent", "owner-c", &recentC, true),
		transcriptRetentionDatabaseRow("bg-active", "owner-a", nil, true),
	}
	if err := db.GORM().CreateInBatches(rows, 64).Error; err != nil {
		t.Fatal(err)
	}

	result, err := db.EnforceTranscriptRetention(ctx, store.TranscriptRetentionPolicy{MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 5 {
		t.Fatalf("background age retention deleted %d, want 5", result.Deleted)
	}
	requireTranscriptRetentionDatabaseCounts(t, db, "owner-a", 2, 2)
	requireTranscriptRetentionDatabaseCounts(t, db, "owner-b", 1, 2)
	requireTranscriptRetentionDatabaseCounts(t, db, "owner-c", 1, 0)
	requireTranscriptRetentionDatabaseTombstone(t, db, "owner-a", "bg-a-old-0", true)
	requireTranscriptRetentionDatabaseTombstone(t, db, "owner-a", "bg-a-old-1", true)
	requireTranscriptRetentionDatabaseTombstone(t, db, "owner-b", "bg-b-old-0", true)
	requireTranscriptRetentionDatabaseTombstone(t, db, "owner-b", "bg-b-old-1", true)
	requireTranscriptRetentionDatabaseTombstone(t, db, "owner-a", "bg-a-old-2", false)
	requireTranscriptRetentionDatabaseTombstone(t, db, "owner-b", "bg-b-recent", false)
	var ownerless int64
	if err := db.GORM().Model(&store.SyncTombstone{}).
		Where("owner_id = ? AND kind = ?", "", "transcript").
		Count(&ownerless).Error; err != nil {
		t.Fatal(err)
	}
	if ownerless != 0 {
		t.Fatalf("transcript tombstones with an empty owner = %d, want 0", ownerless)
	}

	// Count retention without an owner keeps the newest rows across every owner.
	result, err = db.EnforceTranscriptRetention(ctx, store.TranscriptRetentionPolicy{MaxCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 {
		t.Fatalf("background count retention deleted %d, want 1", result.Deleted)
	}
	requireTranscriptRetentionDatabaseCounts(t, db, "owner-a", 1, 3)
	requireTranscriptRetentionDatabaseCounts(t, db, "owner-b", 1, 2)
	requireTranscriptRetentionDatabaseCounts(t, db, "owner-c", 1, 0)
	requireTranscriptRetentionDatabaseTombstone(t, db, "owner-a", "bg-a-recent", true)
	requireTranscriptRetentionDatabaseTombstone(t, db, "owner-b", "bg-b-recent", false)
	requireTranscriptRetentionDatabaseTombstone(t, db, "owner-c", "bg-c-recent", false)

	for _, id := range []string{"bg-active", "bg-b-recent", "bg-c-recent"} {
		if _, err := db.TranscriptGet(ctx, id); err != nil {
			t.Fatalf("survivor %s: %v", id, err)
		}
	}
	if _, err := db.TranscriptGet(ctx, "bg-a-recent"); err == nil {
		t.Fatal("oldest recent transcript survived background count retention")
	}

	repeated, err := db.EnforceTranscriptRetention(ctx, store.TranscriptRetentionPolicy{MaxAge: time.Hour, MaxCount: 2})
	if err != nil || repeated.Deleted != 0 {
		t.Fatalf("repeated background result = %+v, %v", repeated, err)
	}
	requireTranscriptRetentionDatabaseCounts(t, db, "owner-a", 1, 3)
	requireTranscriptRetentionDatabaseCounts(t, db, "owner-b", 1, 2)
	requireTranscriptRetentionDatabaseCounts(t, db, "owner-c", 1, 0)
}

func requireDurableOffsetRowState(t *testing.T, db *store.Store, durableID, owner string, offset int64) {
	t.Helper()
	var row store.DurableTranscriptOffset
	if err := db.GORM().Model(&store.DurableTranscriptOffset{}).
		Where("durable_id = ?", durableID).
		First(&row).Error; err != nil {
		t.Fatalf("durable offset row %s: %v", durableID, err)
	}
	if row.OwnerID != owner || row.Offset != offset {
		t.Fatalf("durable offset row %s = owner %q offset %d, want owner %q offset %d", durableID, row.OwnerID, row.Offset, owner, offset)
	}
}

func TestTranscriptAppendChunksDurableOffsetOwnerIsolationDatabase(t *testing.T) {
	// The offset advance is a guarded UPDATE, not ON CONFLICT ... WHERE (which
	// MySQL and MariaDB ignore): a foreign-owned row must never move.
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	now := time.Now().UnixMilli()

	transcriptA := store.TranscriptRow{ID: "iso-transcript-a", SessionID: "sess-a", AssetID: "asset", StartedAt: now}
	if err := db.TranscriptStart(ctxA, transcriptA); err != nil {
		t.Fatal(err)
	}
	transcriptB := store.TranscriptRow{ID: "iso-transcript-b", SessionID: "sess-b", AssetID: "asset", StartedAt: now}
	if err := db.TranscriptStart(ctxB, transcriptB); err != nil {
		t.Fatal(err)
	}

	// owner-b owns the existing offset row for the shared durable ID.
	if err := db.GORM().Create(&store.DurableTranscriptOffset{DurableID: "shared-durable", Offset: 10, UpdatedAt: now, OwnerID: "owner-b"}).Error; err != nil {
		t.Fatal(err)
	}

	// owner-a's append referencing the same durable ID persists chunks but
	// must not move the foreign-owned offset on any backend.
	if err := db.TranscriptAppendChunks(ctxA, transcriptA.ID,
		[]store.TranscriptChunkRow{{Seq: 0, TabID: "shared-durable", TS: now, Data: []byte("aaaaa")}},
		map[string]int64{"shared-durable": 5}); err != nil {
		t.Fatal(err)
	}
	requireDurableOffsetRowState(t, db, "shared-durable", "owner-b", 10)
	if row, err := db.TranscriptGet(ctxA, transcriptA.ID); err != nil || row.Chunks != 1 {
		t.Fatalf("owner-a chunks must persist: row=%+v err=%v", row, err)
	}

	// owner-b's own append accumulates under owner-b.
	if err := db.TranscriptAppendChunks(ctxB, transcriptB.ID,
		[]store.TranscriptChunkRow{{Seq: 0, TabID: "shared-durable", TS: now, Data: []byte("bbbbb")}},
		map[string]int64{"shared-durable": 5}); err != nil {
		t.Fatal(err)
	}
	requireDurableOffsetRowState(t, db, "shared-durable", "owner-b", 15)

	// A fresh durable ID is created and accumulated under the appending owner.
	if err := db.TranscriptAppendChunks(ctxA, transcriptA.ID,
		[]store.TranscriptChunkRow{{Seq: 1, TabID: "fresh-durable", TS: now, Data: []byte("cccc")}},
		map[string]int64{"fresh-durable": 4}); err != nil {
		t.Fatal(err)
	}
	requireDurableOffsetRowState(t, db, "fresh-durable", "owner-a", 4)

	// A legacy ownerless row is claimed and accumulated by the appending owner.
	if err := db.GORM().Create(&store.DurableTranscriptOffset{DurableID: "legacy-durable", Offset: 3, UpdatedAt: now, OwnerID: ""}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.TranscriptAppendChunks(ctxA, transcriptA.ID,
		[]store.TranscriptChunkRow{{Seq: 2, TabID: "legacy-durable", TS: now, Data: []byte("dd")}},
		map[string]int64{"legacy-durable": 2}); err != nil {
		t.Fatal(err)
	}
	requireDurableOffsetRowState(t, db, "legacy-durable", "owner-a", 5)
}

func TestTranscriptInsertSyncedForeignOwnerRejectedDatabase(t *testing.T) {
	// A synced transcript ID that already exists under another owner must be
	// rejected before any chunk write, on every backend (including MySQL and
	// MariaDB, where ON CONFLICT ... WHERE is ignored by the driver).
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	ended := int64(2000)

	rowB := store.TranscriptRow{ID: "synced-shared", SessionID: "sess-b", AssetID: "asset", StartedAt: 1000, EndedAt: &ended, Bytes: 4, Chunks: 1}
	chunksB := []store.TranscriptChunkRow{{Seq: 0, TabID: "tab-b", TS: 1001, Data: []byte("bbbb")}}
	if err := db.TranscriptInsertSynced(ctxB, rowB, chunksB); err != nil {
		t.Fatal(err)
	}

	rowA := store.TranscriptRow{ID: "synced-shared", SessionID: "sess-a", AssetID: "asset", StartedAt: 1000, EndedAt: &ended, Bytes: 2, Chunks: 1}
	chunksA := []store.TranscriptChunkRow{{Seq: 0, TabID: "tab-a", TS: 1001, Data: []byte("aa")}}
	if err := db.TranscriptInsertSynced(ctxA, rowA, chunksA); err == nil {
		t.Fatal("foreign-owned transcript ID must be rejected")
	}

	// owner-b's metadata and chunks are unchanged, and no owner-a chunk was written.
	got, err := db.TranscriptGet(ctxB, "synced-shared")
	if err != nil || got.Bytes != 4 || got.Chunks != 1 || got.SessionID != "sess-b" {
		t.Fatalf("foreign-owned row changed: row=%+v err=%v", got, err)
	}
	stored, err := db.TranscriptChunks(ctxB, "synced-shared", 0, 1<<20)
	if err != nil || len(stored) != 1 || string(stored[0].Data) != "bbbb" {
		t.Fatalf("foreign-owned chunks changed: chunks=%+v err=%v", stored, err)
	}
	if _, err := db.TranscriptGet(ctxA, "synced-shared"); err == nil {
		t.Fatal("owner-a scoped read must not see owner-b's transcript")
	}

	// A legacy ownerless row is claimed by the first owner that syncs it.
	legacy := store.TranscriptRow{ID: "synced-legacy", SessionID: "sess-legacy", AssetID: "asset", StartedAt: 1000, EndedAt: &ended, Bytes: 9, Chunks: 1, SyncOptIn: true, OwnerID: ""}
	if err := db.GORM().Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GORM().Create(&store.TranscriptChunkRow{TranscriptID: legacy.ID, Seq: 0, TabID: "tab-legacy", TS: 1001, Data: []byte("legacy!!")}).Error; err != nil {
		t.Fatal(err)
	}
	rowLegacy := store.TranscriptRow{ID: "synced-legacy", SessionID: "sess-a", AssetID: "asset", StartedAt: 1000, EndedAt: &ended, Bytes: 2, Chunks: 1}
	if err := db.TranscriptInsertSynced(ctxA, rowLegacy, chunksA); err != nil {
		t.Fatalf("legacy ownerless row must be claimable: %v", err)
	}
	claimed, err := db.TranscriptGet(ctxA, "synced-legacy")
	if err != nil || claimed.OwnerID != "owner-a" || claimed.Bytes != 2 || !claimed.SyncOptIn {
		t.Fatalf("claimed row = %+v err=%v", claimed, err)
	}
	replaced, err := db.TranscriptChunks(ctxA, "synced-legacy", 0, 1<<20)
	if err != nil || len(replaced) != 1 || string(replaced[0].Data) != "aa" {
		t.Fatalf("claimed chunks = %+v err=%v", replaced, err)
	}
}

func TestDurableTabTitleOwnerIsolationDatabase(t *testing.T) {
	// Guarded title update and delete: portable owner isolation on every
	// backend, with an explicit owner_id = '' fallback for legacy rows.
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	ctxA := ipc.WithUserID(ctx, "owner-a")
	ctxB := ipc.WithUserID(ctx, "owner-b")
	now := time.Now().UnixMilli()

	if err := db.DurableTabTitleSet(ctxB, "db-title", "bravo"); err != nil {
		t.Fatal(err)
	}
	if err := db.DurableTabTitleSet(ctxA, "db-title", "alpha"); err != nil {
		t.Fatal(err)
	}
	requireDurableTabTitleRowState(t, db, "db-title", "owner-b", "bravo")
	if title, err := db.DurableTabTitleGet(ctxA, "db-title"); err != nil || title != "" {
		t.Fatalf("owner-a scoped title = %q, %v", title, err)
	}

	if err := db.GORM().Create(&store.DurableTabTitle{DurableID: "db-title-legacy", Title: "legacy", UpdatedAt: now, OwnerID: ""}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.DurableTabTitleSet(ctxA, "db-title-legacy", "claimed"); err != nil {
		t.Fatal(err)
	}
	requireDurableTabTitleRowState(t, db, "db-title-legacy", "owner-a", "claimed")

	if err := db.DurableTabTitleDeleteIfOwned(ctx, "db-title", "owner-a"); err != nil {
		t.Fatal(err)
	}
	requireDurableTabTitleRowState(t, db, "db-title", "owner-b", "bravo")
	if err := db.DurableTabTitleDeleteIfOwned(ctx, "db-title", "owner-b"); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.GORM().Model(&store.DurableTabTitle{}).Where("durable_id = ?", "db-title").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("owner-b's guarded delete must remove the row")
	}
}

func requireDurableTabTitleRowState(t *testing.T, db *store.Store, durableID, owner, title string) {
	t.Helper()
	var row store.DurableTabTitle
	if err := db.GORM().Model(&store.DurableTabTitle{}).Where("durable_id = ?", durableID).First(&row).Error; err != nil {
		t.Fatalf("durable tab title row %s: %v", durableID, err)
	}
	if row.OwnerID != owner || row.Title != title {
		t.Fatalf("durable tab title row %s = owner %q title %q, want owner %q title %q", durableID, row.OwnerID, row.Title, owner, title)
	}
}

type syncedRaceCall struct {
	ctx    context.Context
	row    store.TranscriptRow
	chunks []store.TranscriptChunkRow
}

func runSyncedClaimRace(t *testing.T, db *store.Store, id string, calls []syncedRaceCall) (winner string) {
	t.Helper()
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, len(calls))
	for i, call := range calls {
		wg.Add(1)
		go func(i int, call syncedRaceCall) {
			defer wg.Done()
			<-start
			errs[i] = db.TranscriptInsertSynced(call.ctx, call.row, call.chunks)
		}(i, call)
	}
	close(start)
	wg.Wait()
	success := 0
	for _, err := range errs {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("concurrent claim %s: successes = %d, errs = %v", id, success, errs)
	}
	var owner string
	if err := db.GORM().Model(&store.TranscriptRow{}).Select("owner_id").Where("id = ?", id).Scan(&owner).Error; err != nil {
		t.Fatal(err)
	}
	return owner
}

func requireSyncedRaceChunks(t *testing.T, db *store.Store, id, winner string, seq int64, data string) {
	t.Helper()
	var stored []store.TranscriptChunkRow
	if err := db.GORM().Model(&store.TranscriptChunkRow{}).Where("transcript_id = ?", id).Find(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].OwnerID != winner {
		t.Fatalf("claimed chunk blocks = %+v, want a single block under winner %q", stored, winner)
	}
	decoded, err := db.TranscriptChunks(ipc.WithUserID(context.Background(), winner), id, 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded[0].Seq != seq || string(decoded[0].Data) != data {
		t.Fatalf("decoded winner chunks = %+v, want seq %d data %q", decoded, seq, data)
	}
}

func TestTranscriptInsertSyncedConcurrentClaimDatabase(t *testing.T) {
	// Two owners sync the same fresh ID concurrently with different seqs:
	// exactly one claim wins and the loser writes nothing, on every backend.
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	ended := int64(2000)
	rowA := store.TranscriptRow{ID: "synced-race", SessionID: "sess-a", AssetID: "asset", StartedAt: 1000, EndedAt: &ended, Bytes: 4, Chunks: 1}
	rowB := store.TranscriptRow{ID: "synced-race", SessionID: "sess-b", AssetID: "asset", StartedAt: 1000, EndedAt: &ended, Bytes: 2, Chunks: 1}
	calls := []syncedRaceCall{
		{ctx: ctxA, row: rowA, chunks: []store.TranscriptChunkRow{{Seq: 1, TabID: "tab-a", TS: 1001, Data: []byte("aaaa")}}},
		{ctx: ctxB, row: rowB, chunks: []store.TranscriptChunkRow{{Seq: 2, TabID: "tab-b", TS: 1001, Data: []byte("bb")}}},
	}
	winner := runSyncedClaimRace(t, db, "synced-race", calls)
	if winner == "owner-a" {
		requireSyncedRaceChunks(t, db, "synced-race", winner, 1, "aaaa")
	} else if winner == "owner-b" {
		requireSyncedRaceChunks(t, db, "synced-race", winner, 2, "bb")
	} else {
		t.Fatalf("race row owner = %q", winner)
	}
	loserCtx := ctxA
	if winner == "owner-a" {
		loserCtx = ctxB
	}
	if _, err := db.TranscriptGet(loserCtx, "synced-race"); err == nil {
		t.Fatal("the losing owner must not see the claimed transcript")
	}
}

func TestTranscriptInsertSyncedLegacyClaimRaceDatabase(t *testing.T) {
	// A legacy ownerless row is claimed by exactly one concurrent sync; the
	// loser's payload never lands, on every backend.
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	ended := int64(2000)
	legacy := store.TranscriptRow{ID: "synced-legacy-race", SessionID: "sess-legacy", AssetID: "asset", StartedAt: 999, EndedAt: &ended, Bytes: 6, Chunks: 1, SyncOptIn: true, OwnerID: ""}
	if err := db.GORM().Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GORM().Create(&store.TranscriptChunkRow{TranscriptID: legacy.ID, Seq: 0, TabID: "tab-legacy", TS: 1000, Data: []byte("legacy"), OwnerID: ""}).Error; err != nil {
		t.Fatal(err)
	}
	rowA := store.TranscriptRow{ID: legacy.ID, SessionID: "sess-a", AssetID: "asset", StartedAt: 1000, EndedAt: &ended, Bytes: 4, Chunks: 1}
	rowB := store.TranscriptRow{ID: legacy.ID, SessionID: "sess-b", AssetID: "asset", StartedAt: 1000, EndedAt: &ended, Bytes: 2, Chunks: 1}
	calls := []syncedRaceCall{
		{ctx: ctxA, row: rowA, chunks: []store.TranscriptChunkRow{{Seq: 1, TabID: "tab-a", TS: 1001, Data: []byte("aaaa")}}},
		{ctx: ctxB, row: rowB, chunks: []store.TranscriptChunkRow{{Seq: 2, TabID: "tab-b", TS: 1001, Data: []byte("bb")}}},
	}
	winner := runSyncedClaimRace(t, db, legacy.ID, calls)
	var claimed store.TranscriptRow
	if err := db.GORM().Model(&store.TranscriptRow{}).Where("id = ?", legacy.ID).First(&claimed).Error; err != nil {
		t.Fatal(err)
	}
	if claimed.OwnerID != winner {
		t.Fatalf("claimed row owner = %q, race winner %q", claimed.OwnerID, winner)
	}
	wantBytes := int64(2)
	wantSeq, wantData := int64(2), "bb"
	if winner == "owner-a" {
		wantBytes, wantSeq, wantData = 4, 1, "aaaa"
	}
	if claimed.Bytes != wantBytes {
		t.Fatalf("claimed row bytes = %d, want winner's %d", claimed.Bytes, wantBytes)
	}
	requireSyncedRaceChunks(t, db, legacy.ID, winner, wantSeq, wantData)
}

func requireIPCCode(t *testing.T, err error, code ipc.Code) {
	t.Helper()
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("expected code %s, got %v", code, err)
	}
}

func requireNoOwnerlessChunks(t *testing.T, db *store.Store, transcriptID string) {
	t.Helper()
	var count int64
	if err := db.GORM().Model(&store.TranscriptChunkRow{}).
		Where("transcript_id = ? AND owner_id = ?", transcriptID, "").
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("loser wrote %d ownerless chunk rows for %s", count, transcriptID)
	}
}

func requireOwnerlessOutcome(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	var appErr *ipc.Error
	if errors.As(err, &appErr) && appErr.Code != ipc.CodeForbidden {
		t.Fatalf("ownerless caller got code %s, want %s", appErr.Code, ipc.CodeForbidden)
	}
}

func TestTranscriptInsertSyncedOwnerlessClaimRejectedDatabase(t *testing.T) {
	// owner-a commits first; the ownerless caller must be rejected with
	// CodeForbidden and write nothing, on every backend.
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ended := int64(2000)
	rowA := store.TranscriptRow{ID: "synced-ownerless-fresh", SessionID: "sess-a", AssetID: "asset", StartedAt: 1000, EndedAt: &ended, Bytes: 2, Chunks: 1}
	if err := db.TranscriptInsertSynced(ctxA, rowA,
		[]store.TranscriptChunkRow{{Seq: 2, TabID: "tab-a", TS: 1001, Data: []byte("bb")}}); err != nil {
		t.Fatal(err)
	}
	rowOwnerless := store.TranscriptRow{ID: rowA.ID, SessionID: "sess-x", AssetID: "asset", StartedAt: 1000, EndedAt: &ended, Bytes: 4, Chunks: 1}
	err := db.TranscriptInsertSynced(context.Background(), rowOwnerless,
		[]store.TranscriptChunkRow{{Seq: 1, TabID: "tab-x", TS: 1001, Data: []byte("aaaa")}})
	requireIPCCode(t, err, ipc.CodeForbidden)

	var owner string
	if err := db.GORM().Model(&store.TranscriptRow{}).Select("owner_id").Where("id = ?", rowA.ID).Scan(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if owner != "owner-a" {
		t.Fatalf("row owner = %q, want owner-a", owner)
	}
	requireNoOwnerlessChunks(t, db, rowA.ID)
	requireSyncedRaceChunks(t, db, rowA.ID, "owner-a", 2, "bb")
}

func TestTranscriptInsertSyncedOwnerlessLegacyClaimRejectedDatabase(t *testing.T) {
	// A legacy ownerless row is claimed by owner-a first; the ownerless
	// caller is then rejected with CodeForbidden and writes nothing.
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ended := int64(2000)
	legacy := store.TranscriptRow{ID: "synced-ownerless-legacy", SessionID: "sess-legacy", AssetID: "asset", StartedAt: 999, EndedAt: &ended, Bytes: 6, Chunks: 1, SyncOptIn: true, OwnerID: ""}
	if err := db.GORM().Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GORM().Create(&store.TranscriptChunkRow{TranscriptID: legacy.ID, Seq: 0, TabID: "tab-legacy", TS: 1000, Data: []byte("legacy"), OwnerID: ""}).Error; err != nil {
		t.Fatal(err)
	}
	rowA := store.TranscriptRow{ID: legacy.ID, SessionID: "sess-a", AssetID: "asset", StartedAt: 1000, EndedAt: &ended, Bytes: 2, Chunks: 1}
	if err := db.TranscriptInsertSynced(ctxA, rowA,
		[]store.TranscriptChunkRow{{Seq: 2, TabID: "tab-a", TS: 1001, Data: []byte("bb")}}); err != nil {
		t.Fatal(err)
	}
	rowOwnerless := store.TranscriptRow{ID: legacy.ID, SessionID: "sess-x", AssetID: "asset", StartedAt: 1000, EndedAt: &ended, Bytes: 4, Chunks: 1}
	err := db.TranscriptInsertSynced(context.Background(), rowOwnerless,
		[]store.TranscriptChunkRow{{Seq: 1, TabID: "tab-x", TS: 1001, Data: []byte("aaaa")}})
	requireIPCCode(t, err, ipc.CodeForbidden)

	var owner string
	if err := db.GORM().Model(&store.TranscriptRow{}).Select("owner_id").Where("id = ?", legacy.ID).Scan(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if owner != "owner-a" {
		t.Fatalf("claimed row owner = %q, want owner-a", owner)
	}
	requireNoOwnerlessChunks(t, db, legacy.ID)
	requireSyncedRaceChunks(t, db, legacy.ID, "owner-a", 2, "bb")
}

func runOwnerlessMixedRace(t *testing.T, db *store.Store, id string, calls []syncedRaceCall) []error {
	t.Helper()
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, len(calls))
	for i, call := range calls {
		wg.Add(1)
		go func(i int, call syncedRaceCall) {
			defer wg.Done()
			<-start
			errs[i] = db.TranscriptInsertSynced(call.ctx, call.row, call.chunks)
		}(i, call)
	}
	close(start)
	wg.Wait()
	return errs
}

func requireOwnerlessMixedOutcome(t *testing.T, db *store.Store, id string) {
	t.Helper()
	// An owned caller can always claim an ownerless row through the legacy
	// branch, while an ownerless caller can never claim an owned row, so the
	// owned caller's claim is always final: the row and its chunks carry
	// exactly owner-a, and no ownerless chunk ever persists.
	var owner string
	if err := db.GORM().Model(&store.TranscriptRow{}).Select("owner_id").Where("id = ?", id).Scan(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if owner != "owner-a" {
		t.Fatalf("mixed race final owner = %q, want owner-a", owner)
	}
	requireNoOwnerlessChunks(t, db, id)
	requireSyncedRaceChunks(t, db, id, "owner-a", 2, "bb")
}

func TestTranscriptInsertSyncedOwnerlessConcurrentClaimDatabase(t *testing.T) {
	// owner-a and an ownerless caller race the same fresh ID with different
	// seqs on every backend: the final state is owner-a's exclusively, and
	// the ownerless caller never has persistent chunks.
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ended := int64(2000)
	rowA := store.TranscriptRow{ID: "synced-ownerless-race", SessionID: "sess-a", AssetID: "asset", StartedAt: 1000, EndedAt: &ended, Bytes: 2, Chunks: 1}
	rowOwnerless := store.TranscriptRow{ID: rowA.ID, SessionID: "sess-x", AssetID: "asset", StartedAt: 1000, EndedAt: &ended, Bytes: 4, Chunks: 1}
	calls := []syncedRaceCall{
		{ctx: ctxA, row: rowA, chunks: []store.TranscriptChunkRow{{Seq: 2, TabID: "tab-a", TS: 1001, Data: []byte("bb")}}},
		{ctx: context.Background(), row: rowOwnerless, chunks: []store.TranscriptChunkRow{{Seq: 1, TabID: "tab-x", TS: 1001, Data: []byte("aaaa")}}},
	}
	errs := runOwnerlessMixedRace(t, db, rowA.ID, calls)
	requireOwnerlessOutcome(t, errs[1])
	requireOwnerlessMixedOutcome(t, db, rowA.ID)
}

func TestTranscriptInsertSyncedOwnerlessLegacyConcurrentClaimDatabase(t *testing.T) {
	// The same mixed race on a legacy ownerless row with a legacy chunk:
	// the final state is owner-a's exclusively; no ownerless chunk persists.
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ended := int64(2000)
	legacy := store.TranscriptRow{ID: "synced-ownerless-legacy-race", SessionID: "sess-legacy", AssetID: "asset", StartedAt: 999, EndedAt: &ended, Bytes: 6, Chunks: 1, SyncOptIn: true, OwnerID: ""}
	if err := db.GORM().Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GORM().Create(&store.TranscriptChunkRow{TranscriptID: legacy.ID, Seq: 0, TabID: "tab-legacy", TS: 1000, Data: []byte("legacy"), OwnerID: ""}).Error; err != nil {
		t.Fatal(err)
	}
	rowA := store.TranscriptRow{ID: legacy.ID, SessionID: "sess-a", AssetID: "asset", StartedAt: 1000, EndedAt: &ended, Bytes: 2, Chunks: 1}
	rowOwnerless := store.TranscriptRow{ID: legacy.ID, SessionID: "sess-x", AssetID: "asset", StartedAt: 1000, EndedAt: &ended, Bytes: 4, Chunks: 1}
	calls := []syncedRaceCall{
		{ctx: ctxA, row: rowA, chunks: []store.TranscriptChunkRow{{Seq: 2, TabID: "tab-a", TS: 1001, Data: []byte("bb")}}},
		{ctx: context.Background(), row: rowOwnerless, chunks: []store.TranscriptChunkRow{{Seq: 1, TabID: "tab-x", TS: 1001, Data: []byte("aaaa")}}},
	}
	errs := runOwnerlessMixedRace(t, db, legacy.ID, calls)
	requireOwnerlessOutcome(t, errs[1])
	requireOwnerlessMixedOutcome(t, db, legacy.ID)
}
