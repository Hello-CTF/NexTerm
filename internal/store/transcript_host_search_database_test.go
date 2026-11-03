package store_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func seedHostTranscript(t *testing.T, db *store.Store, ctx context.Context, id, asset string, startedAt int64, data string) {
	t.Helper()
	if err := db.TranscriptStart(ctx, store.TranscriptRow{ID: id, SessionID: "s-" + id, AssetID: asset, StartedAt: startedAt}); err != nil {
		t.Fatal(err)
	}
	if err := db.TranscriptAppendChunks(ctx, id, []store.TranscriptChunkRow{{Seq: 0, TabID: "t", TS: startedAt, Data: []byte(data)}}); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptSearchHostGroupsAndFiltersDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)
	seedHostTranscript(t, db, ctx, "h1", "a", 1000, "needle one\r\n")
	seedHostTranscript(t, db, ctx, "h2", "a", 2000, "nothing here\r\n")
	seedHostTranscript(t, db, ctx, "h3", "a", 3000, "needle three\r\n")
	seedHostTranscript(t, db, ctx, "h4", "other", 4000, "needle other\r\n")

	res, err := db.TranscriptSearchHost(ctx, store.TranscriptHostSearch{AssetID: "a", Text: []byte("needle")})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 2 || res.Sessions[0].Transcript.ID != "h3" || res.Sessions[1].Transcript.ID != "h1" || res.ScannedSessions != 3 || res.Truncated {
		t.Fatalf("unexpected result: %+v", res)
	}
	if len(res.Sessions[0].Matches) != 1 || res.Sessions[0].MoreMatches {
		t.Fatalf("unexpected matches: %+v", res.Sessions[0])
	}
	from, to := int64(1500), int64(3000)
	res, err = db.TranscriptSearchHost(ctx, store.TranscriptHostSearch{AssetID: "a", Text: []byte("needle"), StartedFrom: &from, StartedTo: &to})
	if err != nil || res.ScannedSessions != 1 || len(res.Sessions) != 0 {
		t.Fatalf("range result=%+v err=%v", res, err)
	}
	if _, err := db.TranscriptSearchHost(ctx, store.TranscriptHostSearch{AssetID: "a"}); err == nil {
		t.Fatal("empty query must fail")
	}
}

func TestTranscriptSearchHostSessionLimitDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)
	for i := 0; i < 21; i++ {
		seedHostTranscript(t, db, ctx, fmt.Sprintf("s%02d", i), "a", int64(1000+i), "needle\r\n")
	}
	res, err := db.TranscriptSearchHost(ctx, store.TranscriptHostSearch{AssetID: "a", Text: []byte("needle")})
	if err != nil || !res.Truncated || res.ScannedSessions != 20 || len(res.Sessions) != 20 {
		t.Fatalf("result=%+v err=%v", res, err)
	}
}

func TestTranscriptSearchHostOwnerIsolationDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA, ctxB := ipc.WithUserID(ctx, "u-a"), ipc.WithUserID(ctx, "u-b")
	seedHostTranscript(t, db, ctxA, "oa", "a", 1000, "needle\r\n")
	res, err := db.TranscriptSearchHost(ctxB, store.TranscriptHostSearch{AssetID: "a", Text: []byte("needle")})
	if err != nil || len(res.Sessions) != 0 || res.ScannedSessions != 0 {
		t.Fatalf("owner b result=%+v err=%v", res, err)
	}
	res, err = db.TranscriptSearchHost(ctxA, store.TranscriptHostSearch{AssetID: "a", Text: []byte("needle")})
	if err != nil || len(res.Sessions) != 1 {
		t.Fatalf("owner a result=%+v err=%v", res, err)
	}
}
