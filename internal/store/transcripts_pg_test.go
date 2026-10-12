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

func TestTranscriptHostsGroupByPostgres(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	row := store.TranscriptRow{ID: "pg-hosts-transcript", SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}
	if err := db.TranscriptStart(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := db.TranscriptEnd(ctx, row.ID, 2000, false); err != nil {
		t.Fatal(err)
	}
	hosts, err := db.TranscriptHosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].AssetID != "asset-1" || !hosts[0].AssetDeleted || hosts[0].Transcripts != 1 {
		t.Fatalf("hosts = %+v", hosts)
	}
}

func TestTranscriptInsertSyncedConflictBranchPostgres(t *testing.T) {
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
	if got.Bytes != 8 || got.Chunks != 2 || !got.SyncOptIn {
		t.Fatalf("after conflict upsert = %+v", got)
	}
}
