package store_test

import (
	"context"
	"fmt"
	"math"
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

// Flush-failure holes and non-zero seq starts report real chunk existence
// on every backend: direction flags never trust seq adjacency.
func TestTranscriptPageHoleAndOffsetStartDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	holesID := "pg-holes-transcript"
	if err := db.TranscriptStart(ctx, store.TranscriptRow{ID: holesID, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	appendPagedDatabaseChunks(t, db, holesID, []store.TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("zero")},
		{Seq: 1, TabID: "tab-a", TS: 1001, Data: []byte("one")},
	})
	appendPagedDatabaseChunks(t, db, holesID, []store.TranscriptChunkRow{
		{Seq: 4, TabID: "tab-a", TS: 1004, Data: []byte("four")},
		{Seq: 5, TabID: "tab-a", TS: 1005, Data: []byte("five")},
	})
	// The page [0,1] must report the post-hole chunks as later history.
	head, err := db.TranscriptPageBefore(ctx, holesID, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(head.Chunks) != 2 || head.Chunks[0].Seq != 0 || head.Chunks[1].Seq != 1 {
		t.Fatalf("head page = %+v", head.Chunks)
	}
	if head.HasBefore || !head.HasAfter {
		t.Fatalf("head page neighbors = before %v after %v, want false/true", head.HasBefore, head.HasAfter)
	}
	// The page [4,5] must report the pre-hole chunks as earlier history.
	tail, err := db.TranscriptFirstScreenPage(ctx, holesID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail.Chunks) != 2 || tail.Chunks[0].Seq != 4 || tail.Chunks[1].Seq != 5 {
		t.Fatalf("tail page = %+v", tail.Chunks)
	}
	if !tail.HasBefore || tail.HasAfter {
		t.Fatalf("tail page neighbors = before %v after %v, want true/false", tail.HasBefore, tail.HasAfter)
	}

	offsetID := "pg-offset-start-transcript"
	if err := db.TranscriptStart(ctx, store.TranscriptRow{ID: offsetID, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	appendPagedDatabaseChunks(t, db, offsetID, []store.TranscriptChunkRow{
		{Seq: 1000, TabID: "tab-a", TS: 1000, Data: []byte("a")},
		{Seq: 1001, TabID: "tab-a", TS: 1001, Data: []byte("b")},
		{Seq: 1002, TabID: "tab-a", TS: 1002, Data: []byte("c")},
	})
	whole, err := db.TranscriptFirstScreenPage(ctx, offsetID, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(whole.Chunks) != 3 {
		t.Fatalf("offset-start page = %+v", whole.Chunks)
	}
	if whole.HasBefore || whole.HasAfter {
		t.Fatalf("offset-start page neighbors = before %v after %v, want false/false", whole.HasBefore, whole.HasAfter)
	}
}

// A distant seek on a transcript far beyond the old scan budget resolves
// through the bounded index on every backend: the last block's timestamp is
// found with no budget error, and a past-end time yields the tail page.
func TestTranscriptSeekLargeTranscriptDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	id := "pg-seek-large-transcript"
	if err := db.TranscriptStart(ctx, store.TranscriptRow{ID: id, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	const total = 4097 * 256
	chunks := make([]store.TranscriptChunkRow, 0, 65536)
	flush := func() {
		if len(chunks) == 0 {
			return
		}
		if err := db.TranscriptAppendChunks(ctx, id, chunks); err != nil {
			t.Fatal(err)
		}
		chunks = chunks[:0]
	}
	for seq := 0; seq < total; seq++ {
		chunks = append(chunks, store.TranscriptChunkRow{Seq: int64(seq), TabID: "tab-a", TS: int64(1000 + seq), Data: []byte("x")})
		if len(chunks) == 65536 {
			flush()
		}
	}
	flush()

	// The timestamp inside the final (4097th) block is found exactly.
	page, err := db.TranscriptSeekToTimePage(ctx, id, 1000+total-10, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) == 0 || page.Chunks[0].Seq != total-10 {
		t.Fatalf("late seek starts at %+v, want seq %d", page.Chunks, total-10)
	}
	// A mid-transcript time resolves through the bounded index as well.
	mid, err := db.TranscriptSeekToTimePage(ctx, id, 1000+3000*256, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(mid.Chunks) == 0 || mid.Chunks[0].Seq != 3000*256 {
		t.Fatalf("mid seek starts at %+v, want seq %d", mid.Chunks, 3000*256)
	}
	// A past-end time yields the tail page, never an unsupported error.
	tail, err := db.TranscriptSeekToTimePage(ctx, id, 1000+total+1000, 4096)
	if err != nil {
		t.Fatalf("past-end seek: %v", err)
	}
	if len(tail.Chunks) == 0 || tail.LastSeq != total-1 || tail.HasAfter {
		t.Fatalf("past-end seek = %+v, want the tail page", tail)
	}
}

// The first-screen tail read is inclusive on every backend: a chunk stored
// at the maximum seq belongs to the tail page, never dropped by the
// exclusive cursor.
func TestTranscriptFirstScreenMaxSeqDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	id := "pg-max-seq-transcript"
	if err := db.TranscriptStart(ctx, store.TranscriptRow{ID: id, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	appendPagedDatabaseChunks(t, db, id, []store.TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")},
		{Seq: 1, TabID: "tab-a", TS: 1001, Data: []byte("b")},
	})
	appendPagedDatabaseChunks(t, db, id, []store.TranscriptChunkRow{
		{Seq: int64(math.MaxInt64 - 1), TabID: "tab-a", TS: 1002, Data: []byte("c")},
		{Seq: int64(math.MaxInt64), TabID: "tab-a", TS: 1003, Data: []byte("d")},
	})

	tail, err := db.TranscriptFirstScreenPage(ctx, id, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail.Chunks) != 3 || tail.Chunks[2].Seq != int64(math.MaxInt64) {
		t.Fatalf("tail page = %+v, want the max-seq chunk included", tail.Chunks)
	}
	if !tail.HasBefore || tail.HasAfter {
		t.Fatalf("tail page neighbors = before %v after %v, want true/false", tail.HasBefore, tail.HasAfter)
	}
	exclusive, err := db.TranscriptPageBefore(ctx, id, int64(math.MaxInt64), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(exclusive.Chunks) != 3 || exclusive.Chunks[2].Seq != int64(math.MaxInt64-1) {
		t.Fatalf("exclusive page = %+v, want seqs 0, 1, and max-1", exclusive.Chunks)
	}
	if !exclusive.HasAfter {
		t.Fatal("exclusive page must report the max-seq chunk as later history")
	}
}
