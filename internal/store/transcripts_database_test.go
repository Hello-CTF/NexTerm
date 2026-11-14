package store_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

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

// A same-owner sync refresh that commits after background retention selected a
// row but before its guarded delete keeps the row alive: the guard skips the
// refreshed row, so retention must not tombstone it either — sync collects
// transcript tombstones, and one for a surviving row would push the deletion
// of live content. A refresh that keeps the identical old ended_at stays
// retention-eligible and is deleted together with its tombstone, never one
// without the other. The select-then-refresh window needs two concurrent
// writers, which SQLite's single-writer transactions cannot express
// (constructible on PostgreSQL, MySQL, and MariaDB).
//
//dbtest:backend postgres,mysql,mariadb
func TestTranscriptRetentionRefreshedRowSkipsDeleteAndTombstoneDatabase(t *testing.T) {
	switch strings.ToLower(os.Getenv("NEXTERM_TEST_DB_BACKEND")) {
	case "postgres", "mysql", "mariadb":
	default:
		t.Skip("the select-then-refresh window is not constructible on SQLite")
	}
	ctx := context.Background()
	ctxA := ipc.WithUserID(ctx, "owner-a")
	db := dbtest.NewFixture(t).OpenStore(t)

	now := time.Now().UnixMilli()
	old := now - 2*time.Hour.Milliseconds()
	refreshed := now - time.Minute.Milliseconds()
	insertSynced := func(id string, endedAt int64, content string) {
		t.Helper()
		row := store.TranscriptRow{ID: id, SessionID: "sess-" + id, AssetID: "asset-1", StartedAt: endedAt - 10, EndedAt: &endedAt, Bytes: int64(len(content)), Chunks: 1}
		chunks := []store.TranscriptChunkRow{{Seq: 0, TabID: "tab-1", TS: endedAt, Data: []byte(content)}}
		if err := db.TranscriptInsertSynced(ctxA, row, chunks); err != nil {
			t.Fatal(err)
		}
	}
	insertSynced("retention-refresh-recent", old, "stale content")
	insertSynced("retention-refresh-same-ms", old, "same-ms content")

	// Pause retention after selection, immediately before its first guarded
	// delete, so both refreshes commit inside the window.
	reached := make(chan struct{})
	resume := make(chan struct{})
	var resumeOnce sync.Once
	resumeNow := func() { resumeOnce.Do(func() { close(resume) }) }
	defer resumeNow()
	var reachedOnce sync.Once
	if err := db.GORM().Callback().Delete().Before("gorm:delete").Register("test:pause-transcript-retention", func(tx *gorm.DB) {
		if tx.Statement.Table != "transcript" {
			return
		}
		reachedOnce.Do(func() { close(reached) })
		<-resume
	}); err != nil {
		t.Fatal(err)
	}

	type retentionOutcome struct {
		result store.TranscriptRetentionResult
		err    error
	}
	done := make(chan retentionOutcome, 1)
	go func() {
		result, err := db.EnforceTranscriptRetention(ctx, store.TranscriptRetentionPolicy{MaxAge: time.Hour})
		done <- retentionOutcome{result: result, err: err}
	}()

	<-reached
	insertSynced("retention-refresh-recent", refreshed, "refreshed content")
	insertSynced("retention-refresh-same-ms", old, "same-ms refreshed content")
	resumeNow()

	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.result.Deleted != 1 {
		t.Fatalf("retention deleted = %d, want only the same-ended_at row", got.result.Deleted)
	}

	// The recently refreshed row survives with its refreshed content and no
	// tombstone, so sync has nothing to delete for it.
	row, err := db.TranscriptGet(ctx, "retention-refresh-recent")
	if err != nil {
		t.Fatalf("refreshed transcript must survive retention: %v", err)
	}
	if row.EndedAt == nil || *row.EndedAt != refreshed {
		t.Fatalf("refreshed transcript ended_at = %v, want %d", row.EndedAt, refreshed)
	}
	requireTranscriptTombstoneDatabase(t, db, "retention-refresh-recent", false)
	matches, err := db.TranscriptSearch(ctx, "retention-refresh-recent", []byte("refreshed content"), 0, 0)
	if err != nil || len(matches) != 1 {
		t.Fatalf("refreshed content must be searchable: matches=%v err=%v", matches, err)
	}

	// The same-ended_at refresh stayed retention-eligible: deleted and
	// tombstoned together in the same transaction.
	if _, err := db.TranscriptGet(ctx, "retention-refresh-same-ms"); err == nil {
		t.Fatal("same-ended_at refresh must stay retention-eligible")
	}
	requireTranscriptTombstoneDatabase(t, db, "retention-refresh-same-ms", true)
}

func requireTranscriptTombstoneDatabase(t *testing.T, db *store.Store, id string, want bool) {
	t.Helper()
	var count int64
	if err := db.GORM().Model(&store.SyncTombstone{}).
		Where("owner_id = ? AND id = ? AND kind = ?", "owner-a", id, "transcript").
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if (count == 1) != want {
		t.Fatalf("transcript %s tombstone exists = %v, want %v", id, count == 1, want)
	}
}

// The count keep window is re-read when the delete executes: a same-owner
// refresh that promotes a selected row into the window after selection must
// survive without a tombstone, exactly like the age-path guard. SQLite cannot
// express the select-then-refresh window with two concurrent writers.
//
//dbtest:backend postgres,mysql,mariadb
func TestTranscriptRetentionCountRefreshedRowSkipsDeleteAndTombstoneDatabase(t *testing.T) {
	switch strings.ToLower(os.Getenv("NEXTERM_TEST_DB_BACKEND")) {
	case "postgres", "mysql", "mariadb":
	default:
		t.Skip("the select-then-refresh window is not constructible on SQLite")
	}
	ctx := context.Background()
	ctxA := ipc.WithUserID(ctx, "owner-a")
	db := dbtest.NewFixture(t).OpenStore(t)

	now := time.Now().UnixMilli()
	old := now - 2*time.Hour.Milliseconds()
	keeper := now - 30*time.Minute.Milliseconds()
	refreshed := now - time.Minute.Milliseconds()
	insertSynced := func(id string, endedAt int64, content string) {
		t.Helper()
		row := store.TranscriptRow{ID: id, SessionID: "sess-" + id, AssetID: "asset-1", StartedAt: endedAt - 10, EndedAt: &endedAt, Bytes: int64(len(content)), Chunks: 1}
		chunks := []store.TranscriptChunkRow{{Seq: 0, TabID: "tab-1", TS: endedAt, Data: []byte(content)}}
		if err := db.TranscriptInsertSynced(ctxA, row, chunks); err != nil {
			t.Fatal(err)
		}
	}
	insertSynced("retention-count-keeper", keeper, "keeper content")
	insertSynced("retention-count-victim", old, "stale content")

	reached := make(chan struct{})
	resume := make(chan struct{})
	var resumeOnce sync.Once
	resumeNow := func() { resumeOnce.Do(func() { close(resume) }) }
	defer resumeNow()
	var reachedOnce sync.Once
	if err := db.GORM().Callback().Delete().Before("gorm:delete").Register("test:pause-transcript-count-retention", func(tx *gorm.DB) {
		if tx.Statement.Table != "transcript" {
			return
		}
		reachedOnce.Do(func() { close(reached) })
		<-resume
	}); err != nil {
		t.Fatal(err)
	}

	type retentionOutcome struct {
		result store.TranscriptRetentionResult
		err    error
	}
	done := make(chan retentionOutcome, 1)
	go func() {
		result, err := db.EnforceTranscriptRetention(ctx, store.TranscriptRetentionPolicy{MaxCount: 1})
		done <- retentionOutcome{result: result, err: err}
	}()

	<-reached
	insertSynced("retention-count-victim", refreshed, "refreshed content")
	resumeNow()

	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.result.Deleted != 0 {
		t.Fatalf("retention deleted = %d, want the promoted row to survive", got.result.Deleted)
	}
	row, err := db.TranscriptGet(ctx, "retention-count-victim")
	if err != nil {
		t.Fatalf("promoted transcript must survive count retention: %v", err)
	}
	if row.EndedAt == nil || *row.EndedAt != refreshed {
		t.Fatalf("promoted transcript ended_at = %v, want %d", row.EndedAt, refreshed)
	}
	requireTranscriptTombstoneDatabase(t, db, "retention-count-victim", false)
	if _, err := db.TranscriptGet(ctx, "retention-count-keeper"); err != nil {
		t.Fatalf("keeper transcript must survive count retention: %v", err)
	}
}

// A two-row keep-window reorder: with MaxCount=1, K is the window and V is
// selected; after selection, a same-owner sync demotes K below V and promotes
// V into the window with new content. The count keep set is evaluated inside
// each DELETE statement against the current committed ordering, so V must
// survive without a tombstone and the demoted K — never selected — must not
// cause V's deletion either. A separately read boundary would go stale here
// and delete the newest row with a sync-winning tombstone. SQLite cannot
// express the select-then-refresh window with two concurrent writers.
//
//dbtest:backend postgres,mysql,mariadb
func TestTranscriptRetentionCountReorderSkipsDeleteAndTombstoneDatabase(t *testing.T) {
	switch strings.ToLower(os.Getenv("NEXTERM_TEST_DB_BACKEND")) {
	case "postgres", "mysql", "mariadb":
	default:
		t.Skip("the select-then-refresh window is not constructible on SQLite")
	}
	ctx := context.Background()
	ctxA := ipc.WithUserID(ctx, "owner-a")
	db := dbtest.NewFixture(t).OpenStore(t)

	now := time.Now().UnixMilli()
	keeper := now - 30*time.Minute.Milliseconds()
	victim := now - 2*time.Hour.Milliseconds()
	demoted := now - 3*time.Hour.Milliseconds()
	promoted := now - time.Hour.Milliseconds()
	insertSynced := func(id string, endedAt int64, content string) {
		t.Helper()
		row := store.TranscriptRow{ID: id, SessionID: "sess-" + id, AssetID: "asset-1", StartedAt: endedAt - 10, EndedAt: &endedAt, Bytes: int64(len(content)), Chunks: 1}
		chunks := []store.TranscriptChunkRow{{Seq: 0, TabID: "tab-1", TS: endedAt, Data: []byte(content)}}
		if err := db.TranscriptInsertSynced(ctxA, row, chunks); err != nil {
			t.Fatal(err)
		}
	}
	insertSynced("retention-reorder-k", keeper, "keeper content")
	insertSynced("retention-reorder-v", victim, "stale content")

	reached := make(chan struct{})
	resume := make(chan struct{})
	var resumeOnce sync.Once
	resumeNow := func() { resumeOnce.Do(func() { close(resume) }) }
	defer resumeNow()
	var reachedOnce sync.Once
	if err := db.GORM().Callback().Delete().Before("gorm:delete").Register("test:pause-transcript-reorder-retention", func(tx *gorm.DB) {
		if tx.Statement.Table != "transcript" {
			return
		}
		reachedOnce.Do(func() { close(reached) })
		<-resume
	}); err != nil {
		t.Fatal(err)
	}

	type retentionOutcome struct {
		result store.TranscriptRetentionResult
		err    error
	}
	done := make(chan retentionOutcome, 1)
	go func() {
		result, err := db.EnforceTranscriptRetention(ctx, store.TranscriptRetentionPolicy{MaxCount: 1})
		done <- retentionOutcome{result: result, err: err}
	}()

	<-reached
	insertSynced("retention-reorder-k", demoted, "keeper demoted content")
	insertSynced("retention-reorder-v", promoted, "victim promoted content")
	resumeNow()

	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.result.Deleted != 0 {
		t.Fatalf("retention deleted = %d, want the reordered window to spare both rows", got.result.Deleted)
	}
	row, err := db.TranscriptGet(ctx, "retention-reorder-v")
	if err != nil {
		t.Fatalf("promoted transcript must survive count retention: %v", err)
	}
	if row.EndedAt == nil || *row.EndedAt != promoted {
		t.Fatalf("promoted transcript ended_at = %v, want %d", row.EndedAt, promoted)
	}
	requireTranscriptTombstoneDatabase(t, db, "retention-reorder-v", false)
	matches, err := db.TranscriptSearch(ctx, "retention-reorder-v", []byte("victim promoted content"), 0, 0)
	if err != nil || len(matches) != 1 {
		t.Fatalf("promoted content must be searchable: matches=%v err=%v", matches, err)
	}
	demotedRow, err := db.TranscriptGet(ctx, "retention-reorder-k")
	if err != nil {
		t.Fatalf("demoted transcript must survive this retention run: %v", err)
	}
	if demotedRow.EndedAt == nil || *demotedRow.EndedAt != demoted {
		t.Fatalf("demoted transcript ended_at = %v, want %d", demotedRow.EndedAt, demoted)
	}
	requireTranscriptTombstoneDatabase(t, db, "retention-reorder-k", false)
}

// A sync opt-in flip that commits after selection but before the guarded
// delete must skip this round: deleting on the stale snapshot's flag would
// remove an opted-in row without its tombstone, so the remote copy is never
// cleared and no later run can repair it. The next round deletes the row
// under its new state and writes the tombstone then. SQLite cannot express
// the select-then-flip window with two concurrent writers.
//
//dbtest:backend postgres,mysql,mariadb
func TestTranscriptRetentionOptInFlipSkipsDeleteUntilNextRoundDatabase(t *testing.T) {
	switch strings.ToLower(os.Getenv("NEXTERM_TEST_DB_BACKEND")) {
	case "postgres", "mysql", "mariadb":
	default:
		t.Skip("the select-then-flip window is not constructible on SQLite")
	}
	ctx := context.Background()
	ctxA := ipc.WithUserID(ctx, "owner-a")
	db := dbtest.NewFixture(t).OpenStore(t)

	old := time.Now().Add(-2 * time.Hour).UnixMilli()
	row := store.TranscriptRow{ID: "retention-optin-flip", SessionID: "sess-flip", AssetID: "asset-1", StartedAt: old - 1, EndedAt: &old, SyncOptIn: false, OwnerID: "owner-a"}
	if err := db.GORM().Create(&row).Error; err != nil {
		t.Fatal(err)
	}

	reached := make(chan struct{})
	resume := make(chan struct{})
	var resumeOnce sync.Once
	resumeNow := func() { resumeOnce.Do(func() { close(resume) }) }
	defer resumeNow()
	var reachedOnce sync.Once
	if err := db.GORM().Callback().Delete().Before("gorm:delete").Register("test:pause-transcript-optin-retention", func(tx *gorm.DB) {
		if tx.Statement.Table != "transcript" {
			return
		}
		reachedOnce.Do(func() { close(reached) })
		<-resume
	}); err != nil {
		t.Fatal(err)
	}

	type retentionOutcome struct {
		result store.TranscriptRetentionResult
		err    error
	}
	done := make(chan retentionOutcome, 1)
	go func() {
		result, err := db.EnforceTranscriptRetention(ctx, store.TranscriptRetentionPolicy{MaxAge: time.Hour})
		done <- retentionOutcome{result: result, err: err}
	}()

	<-reached
	if err := db.TranscriptSetSyncOptIn(ctxA, "retention-optin-flip", true); err != nil {
		t.Fatal(err)
	}
	resumeNow()

	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.result.Deleted != 0 {
		t.Fatalf("retention deleted = %d, want the flipped row to survive this round", got.result.Deleted)
	}
	survivor, err := db.TranscriptGet(ctx, "retention-optin-flip")
	if err != nil {
		t.Fatalf("opted-in transcript must survive the flip round: %v", err)
	}
	if !survivor.SyncOptIn {
		t.Fatal("surviving transcript must remain opted in")
	}
	requireTranscriptTombstoneDatabase(t, db, "retention-optin-flip", false)

	// The next round deletes the row under its new state and writes the
	// tombstone then.
	second, err := db.EnforceTranscriptRetention(ctx, store.TranscriptRetentionPolicy{MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if second.Deleted != 1 {
		t.Fatalf("second-round deletion = %d, want 1", second.Deleted)
	}
	requireTranscriptTombstoneDatabase(t, db, "retention-optin-flip", true)
}

// The opt-out direction: the selection snapshot says opted-in, but the row is
// opted out before the delete. The flip already wrote its own tombstone, so
// this round must skip; the next round deletes the now-unopted row without
// writing another tombstone.
//
//dbtest:backend postgres,mysql,mariadb
func TestTranscriptRetentionOptOutFlipSkipsDeleteUntilNextRoundDatabase(t *testing.T) {
	switch strings.ToLower(os.Getenv("NEXTERM_TEST_DB_BACKEND")) {
	case "postgres", "mysql", "mariadb":
	default:
		t.Skip("the select-then-flip window is not constructible on SQLite")
	}
	ctx := context.Background()
	ctxA := ipc.WithUserID(ctx, "owner-a")
	db := dbtest.NewFixture(t).OpenStore(t)

	now := time.Now().UnixMilli()
	old := now - 2*time.Hour.Milliseconds()
	keeper := now - 30*time.Minute.Milliseconds()
	insertSynced := func(id string, endedAt int64, content string) {
		t.Helper()
		row := store.TranscriptRow{ID: id, SessionID: "sess-" + id, AssetID: "asset-1", StartedAt: endedAt - 10, EndedAt: &endedAt, Bytes: int64(len(content)), Chunks: 1}
		chunks := []store.TranscriptChunkRow{{Seq: 0, TabID: "tab-1", TS: endedAt, Data: []byte(content)}}
		if err := db.TranscriptInsertSynced(ctxA, row, chunks); err != nil {
			t.Fatal(err)
		}
	}
	insertSynced("retention-optout-keeper", keeper, "keeper content")
	insertSynced("retention-optout-flip", old, "flip content")

	reached := make(chan struct{})
	resume := make(chan struct{})
	var resumeOnce sync.Once
	resumeNow := func() { resumeOnce.Do(func() { close(resume) }) }
	defer resumeNow()
	var reachedOnce sync.Once
	if err := db.GORM().Callback().Delete().Before("gorm:delete").Register("test:pause-transcript-optout-retention", func(tx *gorm.DB) {
		if tx.Statement.Table != "transcript" {
			return
		}
		reachedOnce.Do(func() { close(reached) })
		<-resume
	}); err != nil {
		t.Fatal(err)
	}

	type retentionOutcome struct {
		result store.TranscriptRetentionResult
		err    error
	}
	done := make(chan retentionOutcome, 1)
	go func() {
		result, err := db.EnforceTranscriptRetention(ctx, store.TranscriptRetentionPolicy{MaxCount: 1})
		done <- retentionOutcome{result: result, err: err}
	}()

	<-reached
	if err := db.TranscriptSetSyncOptIn(ctxA, "retention-optout-flip", false); err != nil {
		t.Fatal(err)
	}
	resumeNow()

	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.result.Deleted != 0 {
		t.Fatalf("retention deleted = %d, want the flipped row to survive this round", got.result.Deleted)
	}
	survivor, err := db.TranscriptGet(ctx, "retention-optout-flip")
	if err != nil {
		t.Fatalf("opted-out transcript must survive the flip round: %v", err)
	}
	if survivor.SyncOptIn {
		t.Fatal("surviving transcript must remain opted out")
	}
	// The flip wrote the tombstone; retention must not add another.
	requireTranscriptTombstoneDatabase(t, db, "retention-optout-flip", true)

	second, err := db.EnforceTranscriptRetention(ctx, store.TranscriptRetentionPolicy{MaxCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if second.Deleted != 1 {
		t.Fatalf("second-round deletion = %d, want 1", second.Deleted)
	}
	if _, err := db.TranscriptGet(ctx, "retention-optout-flip"); err == nil {
		t.Fatal("second round must delete the opted-out row")
	}
	var tombstones int64
	if err := db.GORM().Model(&store.SyncTombstone{}).
		Where("owner_id = ? AND id = ? AND kind = ?", "owner-a", "retention-optout-flip", "transcript").
		Count(&tombstones).Error; err != nil {
		t.Fatal(err)
	}
	if tombstones != 1 {
		t.Fatalf("tombstones for the opted-out row = %d, want only the flip's own tombstone", tombstones)
	}
}

// TranscriptCloseAbandoned settles what an earlier run left behind, for every
// owner: an open recording ends at its last chunk, marked truncated; a
// recording without chunks is deleted (with its sync tombstone under its own
// owner) unless it is starred or a synced copy whose content was left out.
func TestTranscriptCloseAbandonedDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)
	ownerA := ipc.WithUserID(ctx, "owner-a")
	ownerB := ipc.WithUserID(ctx, "owner-b")

	start := func(ownerCtx context.Context, id string, startedAt int64, chunkTS ...int64) {
		t.Helper()
		if err := db.TranscriptStart(ownerCtx, store.TranscriptRow{ID: id, SessionID: "sess-" + id, TabID: "tab-" + id, AssetID: "asset-1", StartedAt: startedAt}); err != nil {
			t.Fatal(err)
		}
		if len(chunkTS) == 0 {
			return
		}
		chunks := make([]store.TranscriptChunkRow, 0, len(chunkTS))
		for seq, ts := range chunkTS {
			chunks = append(chunks, store.TranscriptChunkRow{Seq: int64(seq), TabID: "tab-" + id, TS: ts, Data: []byte("output")})
		}
		if err := db.TranscriptAppendChunks(ownerCtx, id, chunks); err != nil {
			t.Fatal(err)
		}
	}
	end := func(ownerCtx context.Context, id string, endedAt int64) {
		t.Helper()
		if err := db.TranscriptEnd(ownerCtx, id, endedAt, false); err != nil {
			t.Fatal(err)
		}
	}
	set := func(id, column string) {
		t.Helper()
		if err := db.GORM().Model(&store.TranscriptRow{}).Where("id = ?", id).Update(column, true).Error; err != nil {
			t.Fatal(err)
		}
	}

	start(ownerA, "a-open-chunks", 1000, 1100, 1250)
	start(ownerB, "b-open-chunks", 2000, 2100)
	start(ctx, "ownerless-open-chunks", 3000, 3300)
	start(ownerB, "b-open-starred-empty", 4000)
	set("b-open-starred-empty", "starred")
	start(ownerA, "a-ended-empty", 5000)
	end(ownerA, "a-ended-empty", 5100)
	set("a-ended-empty", "sync_opt_in")
	start(ownerB, "b-open-empty", 6000)
	start(ownerA, "a-ended-starred-empty", 7000)
	end(ownerA, "a-ended-starred-empty", 7100)
	set("a-ended-starred-empty", "starred")
	start(ownerB, "b-ended-omitted-empty", 8000)
	end(ownerB, "b-ended-omitted-empty", 8100)
	set("b-ended-omitted-empty", "content_omitted")
	start(ownerA, "a-ended-chunks", 9000, 9100)
	end(ownerA, "a-ended-chunks", 9200)

	ended, deleted, err := db.TranscriptCloseAbandoned(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ended != 4 || deleted != 2 {
		t.Fatalf("settled ended=%d deleted=%d, want 4 ended and 2 deleted", ended, deleted)
	}

	rows := map[string]store.TranscriptRow{}
	var all []store.TranscriptRow
	if err := db.GORM().Find(&all).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range all {
		rows[row.ID] = row
	}
	for _, id := range []string{"a-ended-empty", "b-open-empty"} {
		if _, ok := rows[id]; ok {
			t.Fatalf("%s holds no chunk and must be deleted", id)
		}
	}
	for id, want := range map[string]struct {
		owner     string
		endedAt   int64
		truncated bool
	}{
		"a-open-chunks":         {"owner-a", 1250, true},
		"b-open-chunks":         {"owner-b", 2100, true},
		"ownerless-open-chunks": {"", 3300, true},
		"b-open-starred-empty":  {"owner-b", 4000, true},
		"a-ended-starred-empty": {"owner-a", 7100, false},
		"b-ended-omitted-empty": {"owner-b", 8100, false},
		"a-ended-chunks":        {"owner-a", 9200, false},
	} {
		row, ok := rows[id]
		if !ok {
			t.Fatalf("%s must be kept", id)
		}
		if row.OwnerID != want.owner || row.EndedAt == nil || *row.EndedAt != want.endedAt || row.Truncated != want.truncated {
			t.Fatalf("%s = owner %q ended %v truncated %v, want owner %q ended %d truncated %v",
				id, row.OwnerID, row.EndedAt, row.Truncated, want.owner, want.endedAt, want.truncated)
		}
	}
	if len(rows) != 7 {
		t.Fatalf("remaining transcripts = %d, want 7", len(rows))
	}
	var tombstones []store.SyncTombstone
	if err := db.GORM().Where("kind = ?", "transcript").Find(&tombstones).Error; err != nil {
		t.Fatal(err)
	}
	if len(tombstones) != 1 || tombstones[0].ID != "a-ended-empty" || tombstones[0].OwnerID != "owner-a" {
		t.Fatalf("tombstones = %+v, want the opted-in deleted row's under owner-a", tombstones)
	}

	ended, deleted, err = db.TranscriptCloseAbandoned(ctx)
	if err != nil || ended != 0 || deleted != 0 {
		t.Fatalf("a second pass must find nothing: ended=%d deleted=%d err=%v", ended, deleted, err)
	}
}
