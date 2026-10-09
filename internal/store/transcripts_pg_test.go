package store_test

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestTranscriptAppendChunksDurableOffsetPostgres(t *testing.T) {
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
