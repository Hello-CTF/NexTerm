package store_test

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
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
