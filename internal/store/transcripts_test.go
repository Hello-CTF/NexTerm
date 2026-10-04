package store

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func startTranscript(t *testing.T, db *Store, assetID string, startedAt int64) string {
	t.Helper()
	id := ids.New()
	if err := db.TranscriptStart(context.Background(), TranscriptRow{
		ID: id, SessionID: ids.New(), AssetID: assetID, AssetName: "web-01", AssetKind: "ssh", StartedAt: startedAt,
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func appendTranscriptChunks(t *testing.T, db *Store, id string, chunks ...TranscriptChunkRow) {
	t.Helper()
	if err := db.TranscriptAppendChunks(context.Background(), id, chunks); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptLifecycleAndPagination(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)

	appendTranscriptChunks(t, db, id,
		TranscriptChunkRow{Seq: 0, TabID: "tab-a", TS: 1001, Data: []byte("hello ")},
		TranscriptChunkRow{Seq: 1, TabID: "tab-a", TS: 1002, Data: []byte("world")},
	)

	row, err := db.TranscriptGet(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Bytes != 11 || row.Chunks != 2 || row.EndedAt != nil || row.Truncated {
		t.Fatalf("unexpected transcript row: %+v", row)
	}

	page, err := db.TranscriptChunks(ctx, id, 0, 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].Seq != 0 || string(page[0].Data) != "hello " || page[1].Seq != 1 {
		t.Fatalf("unexpected first page: %+v", page)
	}
	page, err = db.TranscriptChunks(ctx, id, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].Seq != 0 {
		t.Fatalf("byte budget must stop the page after the first chunk: %+v", page)
	}
	page, err = db.TranscriptChunks(ctx, id, 1, 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].Seq != 1 {
		t.Fatalf("unexpected second page: %+v", page)
	}
	page, err = db.TranscriptChunks(ctx, id, 2, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 0 {
		t.Fatalf("expected empty page after last seq, got %+v", page)
	}

	if err := db.TranscriptEnd(ctx, id, 2000, false); err != nil {
		t.Fatal(err)
	}
	row, err = db.TranscriptGet(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.EndedAt == nil || *row.EndedAt != 2000 {
		t.Fatalf("expected ended_at=2000, got %+v", row.EndedAt)
	}
	if err := db.TranscriptEnd(ctx, id, 3000, true); err != nil {
		t.Fatal(err)
	}
	row, err = db.TranscriptGet(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if *row.EndedAt != 2000 || row.Truncated {
		t.Fatalf("second end must be a no-op: %+v", row)
	}
}

func TestTranscriptListByAssetChronologicalAndIsolated(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	old := startTranscript(t, db, "asset-1", 1000)
	recent := startTranscript(t, db, "asset-1", 3000)
	other := startTranscript(t, db, "asset-2", 2000)

	rows, err := db.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != recent || rows[1].ID != old {
		t.Fatalf("expected [recent old], got %+v", rows)
	}
	rows, err = db.TranscriptListByAsset(ctx, "asset-2", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != other {
		t.Fatalf("expected [other], got %+v", rows)
	}
}

func TestTranscriptSearchMatchesAcrossChunks(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendTranscriptChunks(t, db, id,
		TranscriptChunkRow{Seq: 0, TabID: "tab", TS: 1001, Data: []byte("error: disk full\nerror: retrying\n")},
		TranscriptChunkRow{Seq: 1, TabID: "tab", TS: 1002, Data: []byte("done\n")},
	)

	matches, err := db.TranscriptSearch(ctx, id, []byte("error"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].Seq != 0 || matches[1].Seq != 0 {
		t.Fatalf("expected two matches in chunk 0, got %+v", matches)
	}
	if !bytes.Contains(matches[0].Preview, []byte("error: disk full")) {
		t.Fatalf("unexpected preview %q", matches[0].Preview)
	}
	matches, err = db.TranscriptSearch(ctx, id, []byte("done"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Seq != 1 {
		t.Fatalf("expected one match in chunk 1, got %+v", matches)
	}
	matches, err = db.TranscriptSearch(ctx, id, []byte("missing"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no matches, got %+v", matches)
	}
	_, err = db.TranscriptSearch(ctx, id, nil, 0, 0)
	requireCode(t, err, ipc.CodeBadParam)
}

func TestTranscriptDeleteCascadesChunks(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendTranscriptChunks(t, db, id, TranscriptChunkRow{Seq: 0, TabID: "tab", TS: 1001, Data: []byte("data")})

	if err := db.TranscriptDelete(ctx, id); err != nil {
		t.Fatal(err)
	}
	requireTableCount(t, db, "transcript", 0)
	requireTableCount(t, db, "transcript_chunk", 0)
	requireCode(t, db.TranscriptDelete(ctx, id), ipc.CodeNotFound)
	_, err := db.TranscriptGet(ctx, id)
	requireCode(t, err, ipc.CodeNotFound)
}

func TestTranscriptRetentionAgeAndCount(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	now := time.Now().UnixMilli()
	oldEnded := startTranscript(t, db, "asset-1", now-72*time.Hour.Milliseconds())
	if err := db.TranscriptEnd(ctx, oldEnded, now-48*time.Hour.Milliseconds(), false); err != nil {
		t.Fatal(err)
	}
	recentEnded := startTranscript(t, db, "asset-1", now-time.Hour.Milliseconds())
	if err := db.TranscriptEnd(ctx, recentEnded, now, false); err != nil {
		t.Fatal(err)
	}
	active := startTranscript(t, db, "asset-1", now-72*time.Hour.Milliseconds())

	result, err := db.EnforceTranscriptRetention(ctx, TranscriptRetentionPolicy{MaxAge: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 {
		t.Fatalf("expected age retention to delete 1, got %+v", result)
	}
	if _, err := db.TranscriptGet(ctx, oldEnded); err == nil {
		t.Fatal("old ended transcript must be deleted")
	}
	if _, err := db.TranscriptGet(ctx, recentEnded); err != nil {
		t.Fatal("recent ended transcript must be kept")
	}
	if _, err := db.TranscriptGet(ctx, active); err != nil {
		t.Fatal("active transcript must be kept by age retention")
	}

	for index := 0; index < 3; index++ {
		id := startTranscript(t, db, "asset-1", now+int64(index))
		if err := db.TranscriptEnd(ctx, id, now+int64(index), false); err != nil {
			t.Fatal(err)
		}
	}
	result, err = db.EnforceTranscriptRetention(ctx, TranscriptRetentionPolicy{MaxCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 2 {
		t.Fatalf("expected count retention to delete 2, got %+v", result)
	}
	rows, err := db.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected active + 2 newest ended, got %d", len(rows))
	}
	if _, err := db.EnforceTranscriptRetention(ctx, TranscriptRetentionPolicy{MaxAge: -1}); err == nil {
		t.Fatal("negative max age must be rejected")
	}
	if _, err := db.EnforceTranscriptRetention(ctx, TranscriptRetentionPolicy{MaxCount: -1}); err == nil {
		t.Fatal("negative max count must be rejected")
	}
}

func TestTranscriptDatabaseFilePermissions(t *testing.T) {
	if !ownerOnlyPermissionsSupported() {
		t.Skip("owner-only permissions are not supported on this platform")
	}
	db, path := fileStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendTranscriptChunks(t, db, id, TranscriptChunkRow{Seq: 0, TabID: "tab", TS: 1001, Data: []byte("secret output")})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("%s permission %o, want 600", name, perm)
		}
	}
}
