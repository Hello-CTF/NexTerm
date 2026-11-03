package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestTranscriptSearchHostByteBudgetTruncates(t *testing.T) {
	ctx := context.Background()
	db, err := OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("b%d", i)
		if err := db.TranscriptStart(ctx, TranscriptRow{ID: id, SessionID: "s" + id, AssetID: "a", StartedAt: int64(1000 + i)}); err != nil {
			t.Fatal(err)
		}
		if err := db.TranscriptAppendChunks(ctx, id, []TranscriptChunkRow{{Seq: 0, TabID: "t", TS: 1, Data: []byte(strings.Repeat("x", 100))}, {Seq: 1, TabID: "t", TS: 2, Data: []byte(strings.Repeat("y", 100))}}); err != nil {
			t.Fatal(err)
		}
	}
	old := transcriptHostSearchByteBudget
	transcriptHostSearchByteBudget = 150
	t.Cleanup(func() { transcriptHostSearchByteBudget = old })
	res, err := db.TranscriptSearchHost(ctx, TranscriptHostSearch{AssetID: "a", Text: []byte("zzz")})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || res.ScannedBytes > 300 || res.ScannedSessions >= 3 {
		t.Fatalf("budget must truncate the scan: %+v", res)
	}
}

func TestTranscriptSearchHostMatchCapIsNotTruncation(t *testing.T) {
	ctx := context.Background()
	db, err := OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.TranscriptStart(ctx, TranscriptRow{ID: "m", SessionID: "sm", AssetID: "a", StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	var chunks []TranscriptChunkRow
	for i := 0; i < transcriptHostSearchMaxMatches+10; i++ {
		chunks = append(chunks, TranscriptChunkRow{Seq: int64(i), TabID: "t", TS: int64(i + 1), Data: []byte("needle\r\n")})
	}
	if err := db.TranscriptAppendChunks(ctx, "m", chunks); err != nil {
		t.Fatal(err)
	}
	res, err := db.TranscriptSearchHost(ctx, TranscriptHostSearch{AssetID: "a", Text: []byte("needle")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Truncated {
		t.Fatalf("a per-session match cap must not mark the whole result truncated: sessions=%d scanned=%d", len(res.Sessions), res.ScannedSessions)
	}
	if len(res.Sessions) != 1 || !res.Sessions[0].MoreMatches || len(res.Sessions[0].Matches) != transcriptHostSearchMaxMatches {
		t.Fatalf("session must report more matches: sessions=%d", len(res.Sessions))
	}
}
