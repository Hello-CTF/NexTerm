package store_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
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

// Blocks holding sparse chunks with huge seq gaps still straddle correctly
// on every backend: the first block at or above the cursor is the only one
// that can carry older chunks.
func TestTranscriptPageSparseStraddleDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	id := "pg-sparse-straddle-transcript"
	if err := db.TranscriptStart(ctx, store.TranscriptRow{ID: id, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	appendPagedDatabaseChunks(t, db, id, []store.TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")},
		{Seq: 5000, TabID: "tab-a", TS: 1001, Data: []byte("b")},
		{Seq: 10000, TabID: "tab-a", TS: 1002, Data: []byte("c")},
	})

	page, err := db.TranscriptPageBefore(ctx, id, 6000, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) != 2 || page.Chunks[0].Seq != 0 || page.Chunks[1].Seq != 5000 {
		t.Fatalf("page = %+v, want seqs 0 and 5000", page.Chunks)
	}
	if !page.HasAfter {
		t.Fatal("page must report the seq-10000 chunk as later history")
	}
	tail, err := db.TranscriptFirstScreenPage(ctx, id, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail.Chunks) != 3 || tail.LastSeq != 10000 || tail.HasBefore || tail.HasAfter {
		t.Fatalf("tail page = %+v", tail)
	}
}

// A legacy transcript (pre-index data, simulated by dropping the index
// rows) answers its first seek from the bounded verification pass and then
// from the completed index after the backfill catches up — on every
// backend.
func TestTranscriptSeekLegacyBackfillDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	id := "pg-legacy-backfill-transcript"
	if err := db.TranscriptStart(ctx, store.TranscriptRow{ID: id, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	appendPagedDatabaseChunks(t, db, id, []store.TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")},
	})
	appendPagedDatabaseChunks(t, db, id, []store.TranscriptChunkRow{
		{Seq: 1, TabID: "tab-a", TS: 1001, Data: []byte("b")},
	})
	// Simulate legacy data: the transcript's blocks exist but its index
	// rows and state row are gone, so the index is incomplete.
	if err := db.GORM().WithContext(ctx).Exec("DELETE FROM transcript_chunk_ts WHERE transcript_id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GORM().WithContext(ctx).Exec("DELETE FROM transcript_ts_index WHERE transcript_id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	// The first seek answers from the bounded verification pass.
	page, err := db.TranscriptSeekToTimePage(ctx, id, 1001, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) == 0 || page.Chunks[0].Seq != 1 {
		t.Fatalf("legacy seek = %+v, want it to start at seq 1", page.Chunks)
	}
	// Wait for the backfill to complete; the next seek answers from the
	// completed index.
	deadline := time.Now().Add(30 * time.Second)
	for {
		complete, err := db.TranscriptTSIndexCompleteForTest(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if complete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("backfill never completed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	again, err := db.TranscriptSeekToTimePage(ctx, id, 1001, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Chunks) == 0 || again.Chunks[0].Seq != 1 {
		t.Fatalf("post-backfill seek = %+v, want it to start at seq 1", again.Chunks)
	}
}

// A corrupt index row fails the seek explicitly on every backend, never a
// silent tail page.
func TestTranscriptSeekCorruptionDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	id := "pg-corrupt-index-transcript"
	if err := db.TranscriptStart(ctx, store.TranscriptRow{ID: id, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	appendPagedDatabaseChunks(t, db, id, []store.TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")},
	})
	appendPagedDatabaseChunks(t, db, id, []store.TranscriptChunkRow{
		{Seq: 1, TabID: "tab-a", TS: 1001, Data: []byte("b")},
	})
	// Corrupt the index: block 0's max_ts claims the block reaches far
	// beyond its content.
	if err := db.GORM().WithContext(ctx).Exec("UPDATE transcript_chunk_ts SET max_ts = ? WHERE transcript_id = ? AND block_seq = 0", 2000, id).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := db.TranscriptSeekToTimePage(ctx, id, 1001, 1<<20); err == nil {
		t.Fatal("seek over a corrupted index = nil error, want explicit corruption")
	}
}

// A legacy transcript whose verification pass exceeds the scan budget
// reports pending, then answers once the backfill completes — on every
// backend.
func TestTranscriptBackfillPendingRecoveryDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	oldBudget := store.TranscriptSeekScanBudgetForTest()
	store.SetTranscriptSeekScanBudgetForTest(8)
	defer store.SetTranscriptSeekScanBudgetForTest(oldBudget)

	id := "pg-pending-recovery-transcript"
	if err := db.TranscriptStart(ctx, store.TranscriptRow{ID: id, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	chunks := make([]store.TranscriptChunkRow, 0, 16*256)
	for seq := 0; seq < 16*256; seq++ {
		chunks = append(chunks, store.TranscriptChunkRow{Seq: int64(seq), TabID: "tab-a", TS: int64(1000 + seq), Data: []byte("x")})
	}
	appendPagedDatabaseChunks(t, db, id, chunks)
	// Simulate legacy data: drop the index rows and the state row.
	if err := db.GORM().WithContext(ctx).Exec("DELETE FROM transcript_chunk_ts WHERE transcript_id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GORM().WithContext(ctx).Exec("DELETE FROM transcript_ts_index WHERE transcript_id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	// The late seek reports pending (the pass cannot reach the answer).
	if _, err := db.TranscriptSeekToTimePage(ctx, id, 1000+3000, 1<<20); err == nil {
		t.Fatal("late seek over a large legacy transcript = nil error, want pending")
	}
	// Wait for the backfill to complete; the next seek answers.
	deadline := time.Now().Add(30 * time.Second)
	for {
		complete, err := db.TranscriptTSIndexCompleteForTest(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if complete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("backfill never completed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The recovered seek drives the bounded verification through its
	// explicit pending state until the watermark reaches the answer.
	var page store.TranscriptPage
	for {
		var err error
		page, err = db.TranscriptSeekToTimePage(ctx, id, 1000+3000, 1<<20)
		if errors.Is(err, store.ErrTranscriptSeekIndexing) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if len(page.Chunks) == 0 || page.Chunks[0].Seq != 3000 {
		t.Fatalf("recovered seek = %+v, want it to start at seq 3000", page.Chunks)
	}
}

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

// The complete-index seek never scans an unbounded prefix on any backend:
// verification is bounded per call, the watermark advance commits with the
// seek, and the next call resumes — pending is explicit and the final
// answer is exact; once the watermark covers the candidate the seek
// answers immediately.
func TestTranscriptSeekVerificationBoundedDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	oldBudget := store.TranscriptSeekScanBudgetForTest()
	store.SetTranscriptSeekScanBudgetForTest(2)
	defer store.SetTranscriptSeekScanBudgetForTest(oldBudget)

	id := "pg-seek-verification-bounded"
	if err := db.TranscriptStart(ctx, store.TranscriptRow{ID: id, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	for seq := 0; seq < 10; seq++ {
		if err := db.TranscriptAppendChunks(ctx, id, []store.TranscriptChunkRow{{Seq: int64(seq), TabID: "tab-a", TS: int64(1000 + seq), Data: []byte("x")}}); err != nil {
			t.Fatal(err)
		}
	}

	pending := 0
	var page store.TranscriptPage
	for {
		var err error
		page, err = db.TranscriptSeekToTimePage(ctx, id, 1008, 4096)
		if errors.Is(err, store.ErrTranscriptSeekIndexing) {
			pending++
			if pending > 10 {
				t.Fatal("verification never advanced")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if pending == 0 {
		t.Fatal("a far seek on an unverified index answered without a bounded verification pass")
	}
	if len(page.Chunks) == 0 || page.Chunks[0].Seq != 8 {
		t.Fatalf("page = %+v, want it to start at seq 8", page.Chunks)
	}
	again, err := db.TranscriptSeekToTimePage(ctx, id, 1008, 4096)
	if err != nil {
		t.Fatalf("repeat seek = %v, want an immediate answer from the watermark", err)
	}
	if len(again.Chunks) == 0 || again.Chunks[0].Seq != 8 {
		t.Fatalf("repeat page = %+v, want it to start at seq 8", again.Chunks)
	}
}

// A replace rebuilds the index in the same transaction and bumps the
// content generation on every backend: a backfill insert that raced the
// replace and landed afterwards carries the old generation and is never
// read — the seek answers from the current content, never from the stale
// row.
func TestTranscriptSeekStaleIndexRowDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	id := "pg-stale-index-row"
	row := store.TranscriptRow{ID: id, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}
	if err := db.TranscriptStart(ctx, row); err != nil {
		t.Fatal(err)
	}
	ended := int64(2000)
	row.EndedAt = &ended
	row.ContentOmitted = true
	if err := db.TranscriptInsertSynced(ctx, row, []store.TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("old0")},
		{Seq: 1, TabID: "tab-a", TS: 1001, Data: []byte("old1")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.TranscriptReplaceContent(ctx, id, 2, 2, false, []store.TranscriptChunkRow{
		{Seq: 10, TabID: "tab-a", TS: 2000, Data: []byte("new0")},
		{Seq: 11, TabID: "tab-a", TS: 2001, Data: []byte("new1")},
	}); err != nil {
		t.Fatal(err)
	}
	// The racing backfill's insert: a row for a block of the OLD content,
	// stamped with the OLD generation, landing after the replace.
	if err := db.GORM().WithContext(ctx).Exec(
		"INSERT INTO transcript_chunk_ts (transcript_id, block_seq, max_ts, gen, owner_id) VALUES (?, ?, ?, ?, ?)",
		id, 1, 99999, 0, "").Error; err != nil {
		t.Fatal(err)
	}
	// The stale row is invisible to the seek: a time past the new content
	// yields its tail page, never a corruption or a missing-block error.
	page, err := db.TranscriptSeekToTimePage(ctx, id, 5000, 1<<20)
	if err != nil {
		t.Fatalf("seek with a stale index row present = %v, want the current content's answer", err)
	}
	if len(page.Chunks) == 0 || page.Chunks[0].Seq != 10 {
		t.Fatalf("page = %+v, want it to start at the new content's seq 10", page)
	}
}

// databaseLockedBuffer is a goroutine-safe log sink for the backfill
// scheduling assertions: the loop writes logs from its own goroutine while
// the test reads them.
type databaseLockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *databaseLockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *databaseLockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The backfill scheduling is owner-explainable on every backend: with 64
// corrupt blocks sorting before the healthy transcript, a seek naming the
// healthy owner serves it before the corrupt window — the ownerID actually
// drives the scheduling, and the service order is logged.
func TestTranscriptBackfillOwnerPriorityDatabase(t *testing.T) {
	ctx := context.Background()
	fixture := dbtest.NewFixture(t)
	// The fixture's store takes the process default logger; swap in a
	// buffer for this test so the scheduling logs can be asserted.
	logs := &databaseLockedBuffer{}
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(restore)
	db := fixture.OpenStore(t)

	ownerA := ipc.WithUserID(ctx, "user-a")
	ownerB := ipc.WithUserID(ctx, "user-b")
	// Owner A: 64 corrupt blocks that can never be indexed, sorting before
	// every healthy block in (owner, transcript, seq) order.
	corruptID := "pg-priority-corrupt"
	if err := db.TranscriptStart(ownerA, store.TranscriptRow{ID: corruptID, SessionID: "sess-a", AssetID: "asset-a", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	for seq := 0; seq < 64; seq++ {
		if err := db.GORM().WithContext(ctx).Create(&store.TranscriptChunkRow{
			TranscriptID: corruptID, Seq: int64(seq), TabID: "tab-a", TS: int64(1000 + seq), Data: []byte("corrupt"), OwnerID: "user-a",
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Owner B: a healthy transcript, then simulated legacy (index rows and
	// the state row dropped) so the seek kicks the backfill.
	healthyID := "pg-priority-healthy"
	if err := db.TranscriptStart(ownerB, store.TranscriptRow{ID: healthyID, SessionID: "sess-b", AssetID: "asset-b", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []store.TranscriptChunkRow{
		{Seq: 0, TabID: "tab-b", TS: 1000, Data: []byte("b0"), OwnerID: "user-b"},
		{Seq: 1, TabID: "tab-b", TS: 1001, Data: []byte("b1"), OwnerID: "user-b"},
	} {
		if err := db.TranscriptAppendChunks(ownerB, healthyID, []store.TranscriptChunkRow{chunk}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.GORM().WithContext(ctx).Exec("DELETE FROM transcript_chunk_ts WHERE transcript_id = ?", healthyID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GORM().WithContext(ctx).Exec("DELETE FROM transcript_ts_index WHERE transcript_id = ?", healthyID).Error; err != nil {
		t.Fatal(err)
	}

	// The seek kicks the backfill with the caller's owner; the healthy
	// transcript is served before the corrupt window and the loop exits.
	if _, err := db.TranscriptSeekToTimePage(ownerB, healthyID, 1000, 1<<20); err != nil && !errors.Is(err, store.ErrTranscriptSeekIndexing) {
		t.Fatalf("seek = %v, want the pending state or an answer", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for db.TranscriptBackfillLoopRunningForTest() {
		if time.Now().After(deadline) {
			t.Fatal("backfill loop never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
	text := logs.String()
	priority := strings.Index(text, "prioritizing the kicked owner")
	healthy := strings.Index(text, "owners=user-b")
	corrupt := strings.Index(text, "owners=user-a")
	if priority < 0 || healthy < 0 || corrupt < 0 {
		t.Fatalf("backfill logs missing scheduling evidence:\n%s", text)
	}
	if !(priority < healthy && healthy < corrupt) {
		t.Fatalf("the healthy owner was not served first:\n%s", text)
	}
}

// A legal out-of-order append can insert blocks at or before the
// verification watermark on any backend: those blocks are unverified, so
// the watermark must roll back to keep them inside the next bounded
// verification pass — a lowered index value on such a block is explicit
// corruption, never a silently later page.
func TestTranscriptSeekWatermarkRollbackEarlyAppendDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	id := "pg-watermark-rollback"
	if err := db.TranscriptStart(ctx, store.TranscriptRow{ID: id, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []store.TranscriptChunkRow{
		{Seq: 100, TabID: "tab-a", TS: 1000, Data: []byte("a")},
		{Seq: 200, TabID: "tab-a", TS: 2000, Data: []byte("b")},
	} {
		if err := db.TranscriptAppendChunks(ctx, id, []store.TranscriptChunkRow{chunk}); err != nil {
			t.Fatal(err)
		}
	}
	// The seek to at=1500 establishes the watermark on block 200.
	page, err := db.TranscriptSeekToTimePage(ctx, id, 1500, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) == 0 || page.Chunks[0].Seq != 200 {
		t.Fatalf("page = %+v, want it to start at seq 200", page)
	}

	// A legal early-seq append inserts a block before the watermark: the
	// next seek must verify it, and a lowered index value on it is explicit
	// corruption — never a silently later page.
	if err := db.TranscriptAppendChunks(ctx, id, []store.TranscriptChunkRow{{Seq: 0, TabID: "tab-a", TS: 1500, Data: []byte("c")}}); err != nil {
		t.Fatal(err)
	}
	if err := db.GORM().WithContext(ctx).Exec(
		"UPDATE transcript_chunk_ts SET max_ts = 0 WHERE transcript_id = ? AND block_seq = 0", id).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := db.TranscriptSeekToTimePage(ctx, id, 1500, 4096); err == nil {
		t.Fatal("seek after a low-corrupted early-seq append = nil error, want explicit corruption")
	}
}

// A stale-gen index row left by a backfill that raced a whole-content
// replace must not block a later legal append at the same seq on any
// backend: the append's current-generation upsert repairs the row, the
// watermark rolls back, and the seek answers from the appended block
// (naive parity).
func TestTranscriptSeekAppendRepairsStaleIndexRowDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	id := "pg-stale-append"
	row := store.TranscriptRow{ID: id, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}
	if err := db.TranscriptStart(ctx, row); err != nil {
		t.Fatal(err)
	}
	ended := int64(2000)
	row.EndedAt = &ended
	row.ContentOmitted = true
	if err := db.TranscriptInsertSynced(ctx, row, []store.TranscriptChunkRow{
		{Seq: 50, TabID: "tab-a", TS: 900, Data: []byte("old")},
	}); err != nil {
		t.Fatal(err)
	}
	// The replace lands new content with a new generation.
	if err := db.TranscriptReplaceContent(ctx, id, 1, 1, false, []store.TranscriptChunkRow{
		{Seq: 200, TabID: "tab-a", TS: 2000, Data: []byte("new")},
	}); err != nil {
		t.Fatal(err)
	}
	// The racing backfill's late insert: a stale-gen row for the old block.
	if err := db.GORM().WithContext(ctx).Exec(
		"INSERT INTO transcript_chunk_ts (transcript_id, block_seq, max_ts, gen, owner_id) VALUES (?, ?, ?, ?, ?)",
		id, 50, 900, 0, "").Error; err != nil {
		t.Fatal(err)
	}
	// A seek establishes a watermark on the new content.
	if _, err := db.TranscriptSeekToTimePage(ctx, id, 2000, 1<<20); err != nil {
		t.Fatal(err)
	}

	// A legal append reuses the old block's seq: the seek must answer from
	// the appended block (naive parity), not the later block the stale row
	// used to hide it behind.
	if err := db.TranscriptAppendChunks(ctx, id, []store.TranscriptChunkRow{
		{Seq: 50, TabID: "tab-a", TS: 1000, Data: []byte("appended")},
	}); err != nil {
		t.Fatal(err)
	}
	page, err := db.TranscriptSeekToTimePage(ctx, id, 1000, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) == 0 || page.Chunks[0].Seq != 50 {
		t.Fatalf("page = %+v, want it to start at the appended seq 50", page)
	}
}

// The watermark advance is guarded by the content generation on every
// backend: a seek whose snapshot verified the old content must not write
// its watermark into the new generation after an interleaved
// whole-content replace — the advance is discarded and the seek reports
// the explicit retry state, and a low corruption on the new content's
// early block is explicit corruption on the retry, never a wrong page.
func TestTranscriptSeekAdvanceDiscardsStaleGenerationDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	id := "pg-advance-cas"
	row := store.TranscriptRow{ID: id, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}
	if err := db.TranscriptStart(ctx, row); err != nil {
		t.Fatal(err)
	}
	ended := int64(2000)
	row.EndedAt = &ended
	row.ContentOmitted = true
	if err := db.TranscriptInsertSynced(ctx, row, []store.TranscriptChunkRow{
		{Seq: 100, TabID: "tab-a", TS: 1000, Data: []byte("old")},
	}); err != nil {
		t.Fatal(err)
	}

	// The hook interleaves a whole-content replace between the seek's
	// snapshot verification and its watermark advance. The new content's
	// first chunk is oversized so it forms its own early block.
	var replaced atomic.Bool
	restore := store.SetTranscriptSeekBeforeAdvanceHookForTest(func() {
		if replaced.CompareAndSwap(false, true) {
			if err := db.TranscriptReplaceContent(ctx, id, 2, 2, false, []store.TranscriptChunkRow{
				{Seq: 0, TabID: "tab-a", TS: 1500, Data: bytes.Repeat([]byte("w"), (4<<20)+1)},
				{Seq: 200, TabID: "tab-a", TS: 2000, Data: []byte("new1")},
			}); err != nil {
				t.Errorf("interleaved replace = %v", err)
			}
		}
	})
	defer restore()

	// The seek verifies the old content, then the replace lands before the
	// advance: the advance must be discarded (explicit retry), never
	// written into the new generation's watermark.
	if _, err := db.TranscriptSeekToTimePage(ctx, id, 1000, 1<<20); !errors.Is(err, store.ErrTranscriptSeekIndexing) {
		t.Fatalf("seek with an interleaved replace = %v, want the explicit retry state", err)
	}

	// A lowered index value on the new content's early block is explicit
	// corruption on the retry, never a silently later page.
	if err := db.GORM().WithContext(ctx).Exec(
		"UPDATE transcript_chunk_ts SET max_ts = 0 WHERE transcript_id = ? AND block_seq = 0", id).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := db.TranscriptSeekToTimePage(ctx, id, 1500, 1<<20); err == nil || errors.Is(err, store.ErrTranscriptSeekIndexing) {
		t.Fatalf("retry after a low-corrupted new-content block = %v, want explicit corruption", err)
	}
}

// A stale advance must not re-cover blocks an interleaved append rolled
// back, on any backend: the seek's snapshot verified the old content, a
// legal early-seq append rolled the watermark back, and the advance
// computed from the old snapshot would push the watermark forward again
// over the new unverified block — hiding a lowered index value on it and
// returning a later page. The advance is discarded (explicit retry) and
// the retry's bounded pass reports the corruption instead.
func TestTranscriptSeekAdvanceDiscardsRolledBackWatermarkDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	id := "pg-advance-rollback-cas"
	if err := db.TranscriptStart(ctx, store.TranscriptRow{ID: id, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []store.TranscriptChunkRow{
		{Seq: 100, TabID: "tab-a", TS: 1000, Data: []byte("a")},
		{Seq: 200, TabID: "tab-a", TS: 2000, Data: []byte("b")},
	} {
		if err := db.TranscriptAppendChunks(ctx, id, []store.TranscriptChunkRow{chunk}); err != nil {
			t.Fatal(err)
		}
	}
	// The first seek establishes verified_through=100.
	if _, err := db.TranscriptSeekToTimePage(ctx, id, 1000, 1<<20); err != nil {
		t.Fatal(err)
	}

	// The hook interleaves a legal early-seq append between the seek's
	// snapshot verification and its watermark advance.
	var appended atomic.Bool
	restore := store.SetTranscriptSeekBeforeAdvanceHookForTest(func() {
		if appended.CompareAndSwap(false, true) {
			if err := db.TranscriptAppendChunks(ctx, id, []store.TranscriptChunkRow{
				{Seq: 50, TabID: "tab-a", TS: 1500, Data: []byte("c")},
			}); err != nil {
				t.Errorf("interleaved append = %v", err)
			}
		}
	})
	defer restore()

	// The seek verifies the old content to seq=200, then the append rolls
	// the watermark back: the stale advance must be discarded (explicit
	// retry), never re-covering the new unverified block.
	if _, err := db.TranscriptSeekToTimePage(ctx, id, 1500, 1<<20); !errors.Is(err, store.ErrTranscriptSeekIndexing) {
		t.Fatalf("seek with an interleaved early-seq append = %v, want the explicit retry state", err)
	}

	// A lowered index value on the new block is explicit corruption on the
	// retry, never a silently later page.
	if err := db.GORM().WithContext(ctx).Exec(
		"UPDATE transcript_chunk_ts SET max_ts = 0 WHERE transcript_id = ? AND block_seq = 50", id).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := db.TranscriptSeekToTimePage(ctx, id, 1500, 1<<20); err == nil || errors.Is(err, store.ErrTranscriptSeekIndexing) {
		t.Fatalf("retry after a low-corrupted appended block = %v, want explicit corruption", err)
	}
}
