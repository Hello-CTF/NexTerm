package store_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func appendPagedDatabaseChunks(t *testing.T, db *store.Store, id string, chunks []store.TranscriptChunkRow) {
	t.Helper()
	const batch = 64
	for start := 0; start < len(chunks); start += batch {
		end := min(start+batch, len(chunks))
		if err := db.TranscriptAppendChunks(context.Background(), id, chunks[start:end]); err != nil {
			t.Fatal(err)
		}
	}
}

func pagedDatabaseChunks(count int) []store.TranscriptChunkRow {
	chunks := make([]store.TranscriptChunkRow, 0, count)
	for seq := 0; seq < count; seq++ {
		ts := int64(1000 + seq)
		if seq%7 != 0 {
			ts = int64(1000 + seq - seq%7)
		}
		chunks = append(chunks, store.TranscriptChunkRow{
			Seq: int64(seq), TabID: "tab-a", TS: ts,
			Data: []byte(fmt.Sprintf("chunk-%04d-%s", seq, string(make([]byte, 91)))),
		})
	}
	return chunks
}

func TestTranscriptFirstScreenPageDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	id := "pg-first-screen-transcript"
	if err := db.TranscriptStart(ctx, store.TranscriptRow{ID: id, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	appendPagedDatabaseChunks(t, db, id, pagedDatabaseChunks(600))

	page, err := db.TranscriptFirstScreenPage(ctx, id, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) == 0 {
		t.Fatal("first screen is empty")
	}
	total := 0
	for _, chunk := range page.Chunks {
		total += len(chunk.Data)
	}
	if total < 1000 {
		t.Fatalf("first screen bytes = %d, want at least the budget", total)
	}
	if !page.HasBefore || page.HasAfter {
		t.Fatalf("first screen bounds = before %v after %v, want true/false", page.HasBefore, page.HasAfter)
	}
	ascending := true
	for index := 1; index < len(page.Chunks); index++ {
		if page.Chunks[index-1].Seq >= page.Chunks[index].Seq {
			ascending = false
		}
	}
	if !ascending {
		t.Fatalf("first screen chunks not ascending: %+v", page.Chunks)
	}
	if page.LastSeq != 599 {
		t.Fatalf("first screen last seq = %d, want the newest chunk 599", page.LastSeq)
	}

	whole, err := db.TranscriptFirstScreenPage(ctx, id, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(whole.Chunks) != 600 || whole.HasBefore || whole.HasAfter {
		t.Fatalf("whole first screen = %d chunks before %v after %v", len(whole.Chunks), whole.HasBefore, whole.HasAfter)
	}
}

func TestTranscriptSeekToTimePageDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	id := "pg-seek-transcript"
	if err := db.TranscriptStart(ctx, store.TranscriptRow{ID: id, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	appendPagedDatabaseChunks(t, db, id, pagedDatabaseChunks(600))

	// Duplicate timestamps resolve to the first chunk of the group (seq 7
	// opens the ts=1007 group).
	page, err := db.TranscriptSeekToTimePage(ctx, id, 1007, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) == 0 || page.Chunks[0].Seq != 7 {
		t.Fatalf("seek page starts at %+v, want seq 7", page.Chunks)
	}
	// A time past the last chunk yields the final page.
	tail, err := db.TranscriptSeekToTimePage(ctx, id, 9999, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail.Chunks) == 0 || tail.LastSeq != 599 || tail.HasAfter {
		t.Fatalf("past-end seek = %+v, want the tail page", tail)
	}
	// A time before the first chunk yields the opening page.
	head, err := db.TranscriptSeekToTimePage(ctx, id, 0, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(head.Chunks) == 0 || head.Chunks[0].Seq != 0 || head.HasBefore {
		t.Fatalf("before-start seek = %+v, want the opening page", head)
	}
}

func TestTranscriptPageBeforeDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	id := "pg-page-before-transcript"
	if err := db.TranscriptStart(ctx, store.TranscriptRow{ID: id, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	appendPagedDatabaseChunks(t, db, id, pagedDatabaseChunks(600))

	cursor := int64(1<<62 - 1)
	seen := 0
	pages := 0
	lastFirst := int64(-1)
	for {
		page, err := db.TranscriptPageBefore(ctx, id, cursor, 7000)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Chunks) == 0 {
			break
		}
		seen += len(page.Chunks)
		pages++
		if page.FirstSeq >= cursor {
			t.Fatalf("page first seq %d not before cursor %d", page.FirstSeq, cursor)
		}
		if lastFirst >= 0 && page.LastSeq >= lastFirst {
			t.Fatalf("page last seq %d not before previous first %d", page.LastSeq, lastFirst)
		}
		lastFirst = page.FirstSeq
		if !page.HasBefore {
			break
		}
		cursor = page.FirstSeq
	}
	if seen != 600 || pages < 2 {
		t.Fatalf("backward walk covered %d chunks in %d pages, want 600 in >=2", seen, pages)
	}
}
