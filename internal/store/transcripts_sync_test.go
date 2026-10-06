package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestTranscriptSyncOptInRules(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	ended := ids.NowMS()
	endedID := startTranscript(t, db, "asset-1", ended-1000)
	if err := db.TranscriptEnd(ctx, endedID, ended, false); err != nil {
		t.Fatal(err)
	}
	activeID := startTranscript(t, db, "asset-1", ended-500)

	if err := db.TranscriptSetSyncOptIn(ctx, endedID, true); err != nil {
		t.Fatalf("ended transcript opt-in: %v", err)
	}
	row, err := db.TranscriptGet(ctx, endedID)
	if err != nil || !row.SyncOptIn {
		t.Fatalf("row=%+v err=%v", row, err)
	}
	if err := db.TranscriptSetSyncOptIn(ctx, activeID, true); err == nil {
		t.Fatal("in-progress transcript must not opt in")
	}
	if err := db.TranscriptSetSyncOptIn(ctx, ids.New(), true); !isNotFoundErr(err) {
		t.Fatalf("missing transcript: %v", err)
	}

	// opt-out 写墓碑, opt-in 不写。
	if err := db.TranscriptSetSyncOptIn(ctx, endedID, false); err != nil {
		t.Fatal(err)
	}
	assertSyncTombstone(t, db, endedID, "transcript")
	activeRow, err := db.TranscriptGet(ctx, endedID)
	if err != nil || activeRow.SyncOptIn {
		t.Fatalf("opt-out failed: %+v err=%v", activeRow, err)
	}
}

func isNotFoundErr(err error) bool {
	var appErr *ipc.Error
	return errors.As(err, &appErr) && appErr.Code == ipc.CodeNotFound
}

func assertSyncTombstone(t *testing.T, db *Store, id, kind string) {
	t.Helper()
	var gotKind string
	var deletedAt int64
	err := db.DB().QueryRowContext(context.Background(),
		"SELECT kind, deleted_at FROM sync_tombstone WHERE id=?", id).Scan(&gotKind, &deletedAt)
	if err != nil {
		t.Fatalf("sync_tombstone %s: %v", id, err)
	}
	if gotKind != kind || deletedAt <= 0 {
		t.Fatalf("sync_tombstone %s = kind %s deleted_at %d", id, gotKind, deletedAt)
	}
}

func assertNoSyncTombstone(t *testing.T, db *Store, id string) {
	t.Helper()
	var count int
	if err := db.DB().QueryRowContext(context.Background(),
		"SELECT count(*) FROM sync_tombstone WHERE id=?", id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unexpected sync_tombstone for %s", id)
	}
}

func TestTranscriptDeleteWritesTombstoneOnlyWhenOptedIn(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	now := ids.NowMS()

	plain := startTranscript(t, db, "asset-1", now-2000)
	if err := db.TranscriptEnd(ctx, plain, now-1000, false); err != nil {
		t.Fatal(err)
	}
	if err := db.TranscriptDelete(ctx, plain); err != nil {
		t.Fatal(err)
	}
	assertNoSyncTombstone(t, db, plain)

	opted := startTranscript(t, db, "asset-1", now-1500)
	if err := db.TranscriptEnd(ctx, opted, now-900, false); err != nil {
		t.Fatal(err)
	}
	if err := db.TranscriptSetSyncOptIn(ctx, opted, true); err != nil {
		t.Fatal(err)
	}
	if err := db.TranscriptDelete(ctx, opted); err != nil {
		t.Fatal(err)
	}
	assertSyncTombstone(t, db, opted, "transcript")
}

func TestTranscriptRetentionWritesTombstonesForOptedIn(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	now := ids.NowMS()

	old := startTranscript(t, db, "asset-1", now-72*time.Hour.Milliseconds())
	if err := db.TranscriptEnd(ctx, old, now-48*time.Hour.Milliseconds(), false); err != nil {
		t.Fatal(err)
	}
	if err := db.TranscriptSetSyncOptIn(ctx, old, true); err != nil {
		t.Fatal(err)
	}
	plain := startTranscript(t, db, "asset-1", now-70*time.Hour.Milliseconds())
	if err := db.TranscriptEnd(ctx, plain, now-47*time.Hour.Milliseconds(), false); err != nil {
		t.Fatal(err)
	}
	active := startTranscript(t, db, "asset-1", now-1000)

	result, err := db.EnforceTranscriptRetention(ctx, TranscriptRetentionPolicy{MaxAge: 24 * time.Hour})
	if err != nil || result.Deleted != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertSyncTombstone(t, db, old, "transcript")
	assertNoSyncTombstone(t, db, plain)
	if _, err := db.TranscriptGet(ctx, active); err != nil {
		t.Fatalf("active transcript must survive retention: %v", err)
	}
}

func TestTranscriptInsertSyncedAndReplaceContent(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	ended := ids.NowMS() - 1000
	row := TranscriptRow{
		ID: ids.New(), SessionID: ids.New(), AssetID: "asset-9", AssetName: "db-01", AssetKind: "ssh",
		StartedAt: ended - 500, EndedAt: &ended, Bytes: 11, Chunks: 1,
		ContentOmitted: true,
	}
	if err := db.TranscriptInsertSynced(ctx, row, nil); err != nil {
		t.Fatal(err)
	}
	got, err := db.TranscriptGet(ctx, row.ID)
	if err != nil || !got.SyncOptIn || !got.ContentOmitted || got.Bytes != 11 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	optedIn, err := db.TranscriptListOptedIn(ctx)
	if err != nil || len(optedIn) != 1 || optedIn[0].ID != row.ID {
		t.Fatalf("optedIn=%+v err=%v", optedIn, err)
	}

	content := []TranscriptChunkRow{{Seq: 0, TabID: "tab-1", TS: ended - 400, Data: []byte("hello world")}}
	if err := db.TranscriptReplaceContent(ctx, row.ID, 11, 1, false, content); err != nil {
		t.Fatal(err)
	}
	got, err = db.TranscriptGet(ctx, row.ID)
	if err != nil || got.ContentOmitted || got.Bytes != 11 || got.Chunks != 1 {
		t.Fatalf("after replace: %+v err=%v", got, err)
	}
	chunks, err := db.TranscriptChunks(ctx, row.ID, 0, 1<<20)
	if err != nil || len(chunks) != 1 || string(chunks[0].Data) != "hello world" {
		t.Fatalf("chunks=%+v err=%v", chunks, err)
	}

	unended := TranscriptRow{ID: ids.New(), AssetID: "asset-9", StartedAt: ended}
	if err := db.TranscriptInsertSynced(ctx, unended, nil); err == nil {
		t.Fatal("unended transcript must be rejected")
	}
}
