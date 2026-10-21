package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"go.uber.org/goleak"
)

// naiveTranscriptChunks reads the full chunk history through the production
// forward reader; it is the reference model the paged reads must agree with.
func naiveTranscriptChunks(t *testing.T, db *Store, id string) []TranscriptChunkRow {
	t.Helper()
	all := []TranscriptChunkRow{}
	after := int64(0)
	for {
		page, err := db.TranscriptChunks(context.Background(), id, after, 8<<20)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			return all
		}
		all = append(all, page...)
		last := page[len(page)-1].Seq
		if last == math.MaxInt64 {
			return all
		}
		after = last + 1
	}
}

// naiveFirstScreen mirrors the reference model's tail read: walk backward
// from the newest chunk, always including it, and stop once the byte budget
// is crossed or the chunk cap is hit.
func naiveFirstScreen(all []TranscriptChunkRow, maxBytes int) []TranscriptChunkRow {
	page := []TranscriptChunkRow{}
	budget := maxBytes
	for index := len(all) - 1; index >= 0; index-- {
		page = append([]TranscriptChunkRow{all[index]}, page...)
		budget -= len(all[index].Data)
		if budget <= 0 || len(page) >= 4096 {
			break
		}
	}
	return page
}

// naiveSeekToTime mirrors the reference model's time seek: the page starts
// at the first chunk with TS >= at; a time past the last chunk yields the
// final (tail) page.
func naiveSeekToTime(all []TranscriptChunkRow, at int64, maxBytes int) []TranscriptChunkRow {
	target := -1
	for index := range all {
		if all[index].TS >= at {
			target = index
			break
		}
	}
	if target < 0 {
		return naiveFirstScreen(all, maxBytes)
	}
	page := []TranscriptChunkRow{}
	budget := maxBytes
	for index := target; index < len(all); index++ {
		page = append(page, all[index])
		budget -= len(all[index].Data)
		if budget <= 0 || len(page) >= 4096 {
			break
		}
	}
	return page
}

func appendPagedFixture(t *testing.T, db *Store, id string, chunks []TranscriptChunkRow) {
	t.Helper()
	appendPagedFixtureCtx(t, db, context.Background(), id, chunks)
}

func appendPagedFixtureCtx(t *testing.T, db *Store, ctx context.Context, id string, chunks []TranscriptChunkRow) {
	t.Helper()
	const batch = 64
	for start := 0; start < len(chunks); start += batch {
		end := min(start+batch, len(chunks))
		if err := db.TranscriptAppendChunks(ctx, id, chunks[start:end]); err != nil {
			t.Fatal(err)
		}
	}
}

// pagedFixtureChunks builds count fixed-size chunks with non-decreasing
// timestamps; every seventh chunk shares the previous timestamp so duplicate
// groups exist for the seek parity checks.
func pagedFixtureChunks(count int) []TranscriptChunkRow {
	chunks := make([]TranscriptChunkRow, 0, count)
	for seq := 0; seq < count; seq++ {
		ts := int64(1000 + seq)
		if seq%7 != 0 {
			ts = int64(1000 + seq - seq%7)
		}
		chunks = append(chunks, TranscriptChunkRow{
			Seq: int64(seq), TabID: "tab-a", TS: ts,
			Data: []byte(fmt.Sprintf("chunk-%04d-%s", seq, string(make([]byte, 91)))),
		})
	}
	return chunks
}

func requirePageParity(t *testing.T, page TranscriptPage, want []TranscriptChunkRow, all []TranscriptChunkRow) {
	t.Helper()
	if len(page.Chunks) != len(want) {
		t.Fatalf("page has %d chunks, want %d (first %d last %d)", len(page.Chunks), len(want), page.FirstSeq, page.LastSeq)
	}
	for index := range want {
		if page.Chunks[index].Seq != want[index].Seq {
			t.Fatalf("page chunk %d seq = %d, want %d", index, page.Chunks[index].Seq, want[index].Seq)
		}
	}
	if len(want) == 0 {
		if page.HasBefore || page.HasAfter {
			t.Fatalf("empty page reports neighbors: %+v", page)
		}
		return
	}
	if page.FirstSeq != want[0].Seq || page.LastSeq != want[len(want)-1].Seq {
		t.Fatalf("page bounds = [%d,%d], want [%d,%d]", page.FirstSeq, page.LastSeq, want[0].Seq, want[len(want)-1].Seq)
	}
	// Direction flags answer real chunk existence, never adjacency
	// assumptions: flush failures can leave holes and imported transcripts
	// can start at any seq.
	wantBefore, wantAfter := false, false
	for _, chunk := range all {
		if chunk.Seq < page.FirstSeq {
			wantBefore = true
		}
		if chunk.Seq > page.LastSeq {
			wantAfter = true
		}
	}
	if page.HasBefore != wantBefore || page.HasAfter != wantAfter {
		t.Fatalf("page neighbors = before %v after %v, want before %v after %v (first %d last %d)",
			page.HasBefore, page.HasAfter, wantBefore, wantAfter, page.FirstSeq, page.LastSeq)
	}
}

func TestTranscriptFirstScreenEmpty(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	page, err := db.TranscriptFirstScreenPage(context.Background(), id, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) != 0 || page.HasBefore || page.HasAfter {
		t.Fatalf("empty first screen = %+v", page)
	}
}

func TestTranscriptFirstScreenTailParity(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	chunks := pagedFixtureChunks(600)
	appendPagedFixture(t, db, id, chunks)
	all := naiveTranscriptChunks(t, db, id)
	if len(all) != 600 {
		t.Fatalf("reference read %d chunks, want 600", len(all))
	}

	page, err := db.TranscriptFirstScreenPage(context.Background(), id, 1000)
	if err != nil {
		t.Fatal(err)
	}
	requirePageParity(t, page, naiveFirstScreen(all, 1000), all)
	if !page.HasBefore || page.HasAfter {
		t.Fatalf("first screen bounds = before %v after %v, want true/false", page.HasBefore, page.HasAfter)
	}

	whole, err := db.TranscriptFirstScreenPage(context.Background(), id, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	requirePageParity(t, whole, naiveFirstScreen(all, 8<<20), all)
	if whole.HasBefore || whole.HasAfter {
		t.Fatalf("whole-history first screen bounds = before %v after %v, want false/false", whole.HasBefore, whole.HasAfter)
	}
}

func TestTranscriptPageBeforeWalksBackwards(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	chunks := pagedFixtureChunks(600)
	appendPagedFixture(t, db, id, chunks)
	all := naiveTranscriptChunks(t, db, id)

	// Walk backward from the tail: every page matches the reference tail read
	// anchored at its cursor, the pages tile the whole history exactly once,
	// and the final page reports no earlier history.
	cursor := int64(1<<62 - 1)
	seen := 0
	pages := 0
	for {
		page, err := db.TranscriptPageBefore(context.Background(), id, cursor, 7000)
		if err != nil {
			t.Fatal(err)
		}
		var want []TranscriptChunkRow
		remaining := all[:min(len(all), int(cursor))]
		if len(remaining) > 0 {
			want = naiveFirstScreen(remaining, 7000)
		}
		requirePageParity(t, page, want, all)
		if len(page.Chunks) == 0 {
			if page.HasBefore {
				t.Fatalf("empty page reports earlier history: %+v", page)
			}
			break
		}
		seen += len(page.Chunks)
		pages++
		if !page.HasBefore {
			break
		}
		cursor = page.FirstSeq
	}
	if seen != len(all) || pages < 2 {
		t.Fatalf("backward walk covered %d chunks in %d pages, want %d in >=2", seen, pages, len(all))
	}
}

func TestTranscriptSeekToTimeParity(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	chunks := pagedFixtureChunks(600)
	appendPagedFixture(t, db, id, chunks)
	all := naiveTranscriptChunks(t, db, id)

	for _, at := range []int64{0, 999, 1000, 1007, 1008, 1014, 1050, 1300, 1599, 1600, 9999} {
		page, err := db.TranscriptSeekToTimePage(context.Background(), id, at, 4096)
		if err != nil {
			t.Fatalf("seek %d: %v", at, err)
		}
		requirePageParity(t, page, naiveSeekToTime(all, at, 4096), all)
	}
}

func TestTranscriptPageOwnerIsolation(t *testing.T) {
	db := testStore(t)
	ownerCtx := ipc.WithUserID(context.Background(), "user-1")
	otherCtx := ipc.WithUserID(context.Background(), "user-2")
	id := ids.New()
	if err := db.TranscriptStart(ownerCtx, TranscriptRow{ID: id, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	appendPagedFixtureCtx(t, db, ownerCtx, id, pagedFixtureChunks(10))

	// Cross-owner reads fail closed with the same not-found answer as a
	// missing transcript: no page, seek, or cursor leaks the content.
	requireCode(t, firstScreenErr(db, otherCtx, id), ipc.CodeNotFound)
	requireCode(t, seekErr(db, otherCtx, id), ipc.CodeNotFound)
	requireCode(t, pageBeforeErr(db, otherCtx, id), ipc.CodeNotFound)
	requireCode(t, pageAfterErr(db, otherCtx, id), ipc.CodeNotFound)
}

func firstScreenErr(db *Store, ctx context.Context, id string) error {
	_, err := db.TranscriptFirstScreenPage(ctx, id, 1<<20)
	return err
}

func seekErr(db *Store, ctx context.Context, id string) error {
	_, err := db.TranscriptSeekToTimePage(ctx, id, 0, 1<<20)
	return err
}

func pageBeforeErr(db *Store, ctx context.Context, id string) error {
	_, err := db.TranscriptPageBefore(ctx, id, 1<<62-1, 1<<20)
	return err
}

func pageAfterErr(db *Store, ctx context.Context, id string) error {
	_, err := db.TranscriptPageAfter(ctx, id, 0, 1<<20)
	return err
}

func TestTranscriptPageRandomizedParity(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	random := rand.New(rand.NewSource(20261021))
	chunks := make([]TranscriptChunkRow, 0, 900)
	ts := int64(1000)
	for seq := 0; seq < 900; seq++ {
		ts += int64(random.Intn(4)) // non-decreasing with duplicate groups
		size := 1 + random.Intn(512)
		chunks = append(chunks, TranscriptChunkRow{
			Seq: int64(seq), TabID: "tab-a", TS: ts,
			Data: make([]byte, size),
		})
	}
	appendPagedFixture(t, db, id, chunks)
	all := naiveTranscriptChunks(t, db, id)

	for _, maxBytes := range []int{1, 100, 4096, 1 << 20} {
		page, err := db.TranscriptFirstScreenPage(context.Background(), id, maxBytes)
		if err != nil {
			t.Fatal(err)
		}
		requirePageParity(t, page, naiveFirstScreen(all, maxBytes), all)
	}
	for _, at := range []int64{999, 1000, 1001, 1002, 1003, 1005, 1010, 1050, 1100} {
		page, err := db.TranscriptSeekToTimePage(context.Background(), id, at, 2048)
		if err != nil {
			t.Fatal(err)
		}
		requirePageParity(t, page, naiveSeekToTime(all, at, 2048), all)
	}
	cursor := all[len(all)-1].Seq + 1
	remaining := append([]TranscriptChunkRow(nil), all...)
	for len(remaining) > 0 {
		page, err := db.TranscriptPageBefore(context.Background(), id, cursor, 3000)
		if err != nil {
			t.Fatal(err)
		}
		want := naiveFirstScreen(remaining, 3000)
		requirePageParity(t, page, want, all)
		if len(page.Chunks) == 0 {
			break
		}
		remaining = remaining[:len(remaining)-len(page.Chunks)]
		cursor = page.FirstSeq
	}
	if len(remaining) != 0 {
		t.Fatalf("backward walk left %d chunks uncovered", len(remaining))
	}
}

// The per-block timestamp computation mirrors the encoder's block grouping
// exactly: every computed row matches the decoded block's true maximum
// timestamp, including across the item and byte boundaries.
func TestTranscriptBlockMaxTimestamps(t *testing.T) {
	chunks := make([]TranscriptChunkRow, 0, 600)
	for seq := 0; seq < 600; seq++ {
		ts := int64(1000 + seq)
		if seq == 300 {
			ts = 9999 // out-of-order spike: the block max is not the last chunk's TS
		}
		chunks = append(chunks, TranscriptChunkRow{Seq: int64(seq), TabID: "tab-a", TS: ts, Data: []byte("x")})
	}
	rows := transcriptBlockMaxTimestamps(chunks)
	blocks, err := encodeTranscriptChunks(chunks)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(blocks) {
		t.Fatalf("index rows = %d, want one per stored block (%d)", len(rows), len(blocks))
	}
	for index, block := range blocks {
		decoded, err := decodeTranscriptBlock(block)
		if err != nil {
			t.Fatal(err)
		}
		want := decoded[0].TS
		for _, chunk := range decoded[1:] {
			if chunk.TS > want {
				want = chunk.TS
			}
		}
		if rows[index].blockSeq != block.Seq || rows[index].maxTS != want {
			t.Fatalf("row %d = %+v, want block seq %d with true max TS %d", index, rows[index], block.Seq, want)
		}
	}
}

// A stored block whose timestamp metadata does not match its last chunk is
// corrupt: verified decoding fails explicitly instead of trusting the row
// for time-indexed reads.
func TestDecodeTranscriptBlockVerifiedRejectsTimestampMismatch(t *testing.T) {
	blocks, err := encodeTranscriptChunks([]TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")},
		{Seq: 1, TabID: "tab-a", TS: 1001, Data: []byte("b")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeTranscriptBlockVerified(blocks[0]); err != nil {
		t.Fatalf("verified decode of a healthy block = %v", err)
	}
	corrupt := blocks[0]
	corrupt.TS = 9999
	if _, err := decodeTranscriptBlockVerified(corrupt); err == nil {
		t.Fatal("verified decode of a timestamp-mismatched block = nil error")
	}
}

// Out-of-order timestamps are legitimate input — a clock step or an older
// writer must never lose data: the append accepts them and the seek answers
// correctly through the timestamp index, which is independent of TS order.
func TestTranscriptAppendAcceptsOutOfOrderTimestamps(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendPagedFixture(t, db, id, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")},
		{Seq: 1, TabID: "tab-a", TS: 1002, Data: []byte("b")},
	})
	// A later batch steps below the newest block's stored TS — accepted.
	appendPagedFixture(t, db, id, []TranscriptChunkRow{
		{Seq: 2, TabID: "tab-a", TS: 1001, Data: []byte("c")},
		{Seq: 3, TabID: "tab-a", TS: 9999, Data: []byte("d")},
	})
	all := naiveTranscriptChunks(t, db, id)
	if len(all) != 4 {
		t.Fatalf("stored chunks = %d, want all 4 accepted", len(all))
	}
	// The seek finds the first qualifying chunk by seq under the disorder.
	page, err := db.TranscriptSeekToTimePage(context.Background(), id, 1002, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) == 0 || page.Chunks[0].Seq != 1 {
		t.Fatalf("seek page = %+v, want it to start at seq 1", page.Chunks)
	}
	// The spike at seq 3 is found from its timestamp alone.
	spike, err := db.TranscriptSeekToTimePage(context.Background(), id, 5000, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(spike.Chunks) == 0 || spike.Chunks[0].Seq != 3 {
		t.Fatalf("spike seek = %+v, want it to start at seq 3", spike.Chunks)
	}
}

// Synced and replacement content with out-of-order timestamps is accepted
// and indexed like any other content.
func TestTranscriptSyncedAndReplaceAcceptOutOfOrderTimestamps(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	row := TranscriptRow{ID: ids.New(), SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}
	if err := db.TranscriptStart(ctx, row); err != nil {
		t.Fatal(err)
	}
	ended := int64(2000)
	row.EndedAt = &ended
	row.ContentOmitted = true
	if err := db.TranscriptInsertSynced(ctx, row, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1001, Data: []byte("a")},
		{Seq: 1, TabID: "tab-a", TS: 1000, Data: []byte("b")},
	}); err != nil {
		t.Fatalf("synced insert with out-of-order timestamps = %v", err)
	}
	if err := db.TranscriptReplaceContent(ctx, row.ID, 2, 2, false, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1002, Data: []byte("c")},
		{Seq: 1, TabID: "tab-a", TS: 1001, Data: []byte("d")},
	}); err != nil {
		t.Fatalf("replace with out-of-order timestamps = %v", err)
	}
	page, err := db.TranscriptSeekToTimePage(ctx, row.ID, 1002, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) == 0 || page.Chunks[0].Seq != 0 {
		t.Fatalf("seek = %+v, want it to start at seq 0", page.Chunks)
	}
}

// The per-block timestamp index is maintained on every append: the distant
// seek eliminates blocks by their true maximum timestamp and decodes only
// the candidate, matching the naive full-scan reference under ordered,
// duplicate, and out-of-order data.
func TestTranscriptSeekUsesTimestampIndex(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendPagedFixture(t, db, id, pagedFixtureChunks(600))
	// A later append with a disorder is indexed too.
	appendPagedFixture(t, db, id, []TranscriptChunkRow{
		{Seq: 600, TabID: "tab-a", TS: 500, Data: []byte("dip")},
		{Seq: 601, TabID: "tab-a", TS: 9999, Data: []byte("spike")},
	})
	all := naiveTranscriptChunks(t, db, id)
	for _, at := range []int64{0, 500, 999, 1000, 1007, 1050, 1300, 1595, 1596, 5000, 9999, 10000} {
		page, err := db.TranscriptSeekToTimePage(context.Background(), id, at, 4096)
		if err != nil {
			t.Fatalf("seek %d: %v", at, err)
		}
		requirePageParity(t, page, naiveSeekToTime(all, at, 4096), all)
	}
}

// Flush failures can leave holes in the chunk seq space: direction flags
// must answer real chunk existence, never seq adjacency.
func TestTranscriptPageBeforeWithFlushHoles(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendPagedFixture(t, db, id, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("zero")},
		{Seq: 1, TabID: "tab-a", TS: 1001, Data: []byte("one")},
	})
	appendPagedFixture(t, db, id, []TranscriptChunkRow{
		{Seq: 4, TabID: "tab-a", TS: 1004, Data: []byte("four")},
		{Seq: 5, TabID: "tab-a", TS: 1005, Data: []byte("five")},
	})
	all := naiveTranscriptChunks(t, db, id)

	// The page ending at seq 4 must not claim later history: seq 5 exists.
	page, err := db.TranscriptPageBefore(context.Background(), id, 5, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	requirePageParity(t, page, naiveFirstScreen(all[:3], 1<<20), all)
	if !page.HasAfter {
		t.Fatal("page before seq 5 must report the existing seq-5 chunk as later history")
	}
	// The page [4,5] has real earlier history despite the hole at 2,3.
	tail, err := db.TranscriptFirstScreenPage(context.Background(), id, 5)
	if err != nil {
		t.Fatal(err)
	}
	requirePageParity(t, tail, naiveFirstScreen(all, 5), all)
	if !tail.HasBefore {
		t.Fatal("tail page must report the existing pre-hole chunks as earlier history")
	}
	// A page entirely below the hole reports no earlier history.
	head, err := db.TranscriptPageBefore(context.Background(), id, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	requirePageParity(t, head, naiveFirstScreen(all[:2], 1<<20), all)
	if head.HasBefore {
		t.Fatal("page [0,1] must not report earlier history")
	}
}

// Imported transcripts can start at any seq: an opening page at seq 1000
// must not claim earlier history that does not exist.
func TestTranscriptPageNonZeroStart(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	chunks := make([]TranscriptChunkRow, 0, 6)
	for seq := 1000; seq < 1006; seq++ {
		chunks = append(chunks, TranscriptChunkRow{Seq: int64(seq), TabID: "tab-a", TS: int64(seq), Data: []byte("x")})
	}
	appendPagedFixture(t, db, id, chunks)
	all := naiveTranscriptChunks(t, db, id)

	whole, err := db.TranscriptFirstScreenPage(context.Background(), id, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	requirePageParity(t, whole, naiveFirstScreen(all, 1<<20), all)
	if whole.HasBefore || whole.HasAfter {
		t.Fatalf("whole page neighbors = before %v after %v, want false/false", whole.HasBefore, whole.HasAfter)
	}
	head, err := db.TranscriptPageBefore(context.Background(), id, 1002, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	requirePageParity(t, head, naiveFirstScreen(all[:2], 1<<20), all)
	if head.HasBefore {
		t.Fatal("page starting at the first chunk must not report earlier history")
	}
	if !head.HasAfter {
		t.Fatal("page [1000,1001] must report the later chunks")
	}
	after, err := db.TranscriptPageAfter(context.Background(), id, 1002, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	requirePageParity(t, after, all[2:], all)
	if !after.HasBefore || after.HasAfter {
		t.Fatalf("tail page neighbors = before %v after %v, want true/false", after.HasBefore, after.HasAfter)
	}
}

// insertRawTranscriptBlocks writes chunk blocks directly through the DB
// handle, bypassing the store's append invariants — the faithful shape of
// legacy data written before the timestamp-order invariant existed. The
// block rows carry the chunks' owner.
func insertRawTranscriptBlocks(t *testing.T, db *Store, transcriptID string, chunks []TranscriptChunkRow) {
	t.Helper()
	blocks, err := encodeTranscriptChunks(chunks)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range blocks {
		block.TranscriptID = transcriptID
		block.OwnerID = chunks[0].OwnerID
		if err := db.db.WithContext(context.Background()).Create(&block).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// Out-of-order timestamps (clock steps, concurrent writers, legacy data)
// must not break the seek: it resolves the first chunk by seq with TS >= at
// under any timestamp order, and duplicate groups spanning block boundaries
// resolve to the first matching chunk. The disorder is written as legacy
// data: the current store rejects it at append time.
func TestTranscriptSeekToTimeDisorderParity(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	chunks := make([]TranscriptChunkRow, 0, 600)
	for seq := 0; seq < 600; seq++ {
		ts := int64(1000 + seq)
		switch {
		case seq == 300:
			ts = 9999 // out-of-order spike
		case seq == 301:
			ts = 500 // out-of-order dip
		case seq%7 != 0:
			ts = int64(1000 + seq - seq%7)
		}
		chunks = append(chunks, TranscriptChunkRow{Seq: int64(seq), TabID: "tab-a", TS: ts, Data: []byte("x")})
	}
	insertRawTranscriptBlocks(t, db, id, chunks)
	all := naiveTranscriptChunks(t, db, id)

	for _, at := range []int64{0, 500, 999, 1000, 1007, 1050, 1299, 1300, 1595, 1596, 9999, 10000} {
		page, err := db.TranscriptSeekToTimePage(context.Background(), id, at, 4096)
		if err != nil {
			t.Fatalf("seek %d: %v", at, err)
		}
		requirePageParity(t, page, naiveSeekToTime(all, at, 4096), all)
	}
}

// Legacy ordered data (written before the timestamp index) is backfilled on
// first seek and then takes the bounded index path, with parity against
// the naive scan.
func TestTranscriptSeekLegacyOrderedValidation(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	insertRawTranscriptBlocks(t, db, id, pagedFixtureChunks(600))
	all := naiveTranscriptChunks(t, db, id)

	for _, at := range []int64{0, 999, 1000, 1007, 1050, 1300, 1595, 1596, 9999} {
		page, err := db.TranscriptSeekToTimePage(context.Background(), id, at, 4096)
		if err != nil {
			t.Fatalf("seek %d: %v", at, err)
		}
		requirePageParity(t, page, naiveSeekToTime(all, at, 4096), all)
	}
	// The validation verdict is cached: a second seek agrees.
	page, err := db.TranscriptSeekToTimePage(context.Background(), id, 1050, 4096)
	if err != nil {
		t.Fatal(err)
	}
	requirePageParity(t, page, naiveSeekToTime(all, 1050, 4096), all)
}

// A distant seek on a transcript far beyond the old scan budget answers
// through the bounded index: the last block's timestamp is found with no
// budget error, and a past-end time yields the tail page.
func TestTranscriptSeekLargeTranscriptBeyondOldBudget(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	const total = 4097 * 256
	chunks := make([]TranscriptChunkRow, 0, 65536)
	flush := func() {
		if len(chunks) == 0 {
			return
		}
		if err := db.TranscriptAppendChunks(context.Background(), id, chunks); err != nil {
			t.Fatal(err)
		}
		chunks = chunks[:0]
	}
	for seq := 0; seq < total; seq++ {
		chunks = append(chunks, TranscriptChunkRow{Seq: int64(seq), TabID: "tab-a", TS: int64(1000 + seq), Data: []byte("x")})
		if len(chunks) == 65536 {
			flush()
		}
	}
	flush()

	// The timestamp inside the final (4097th) block is found exactly.
	page, err := db.TranscriptSeekToTimePage(context.Background(), id, 1000+total-10, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) == 0 || page.Chunks[0].Seq != total-10 {
		t.Fatalf("late seek starts at %+v, want seq %d", page.Chunks, total-10)
	}
	// A past-end time yields the tail page, never an unsupported error.
	tail, err := db.TranscriptSeekToTimePage(context.Background(), id, 1000+total+1000, 4096)
	if err != nil {
		t.Fatalf("past-end seek: %v", err)
	}
	if len(tail.Chunks) == 0 || tail.LastSeq != total-1 || tail.HasAfter {
		t.Fatalf("past-end seek = %+v, want the tail page", tail)
	}
	// A mid-transcript time resolves through the bounded index as well.
	mid, err := db.TranscriptSeekToTimePage(context.Background(), id, 1000+3000*256, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(mid.Chunks) == 0 || mid.Chunks[0].Seq != 3000*256 {
		t.Fatalf("mid seek starts at %+v, want seq %d", mid.Chunks, 3000*256)
	}
}

// The seek and the backfill are one consistent read: a legacy block the
// backfill indexes mid-seek is never skipped between two queries, so the
// review's interleave — empty candidate, backfill indexes seq 0, remainder
// would have skipped it — cannot produce a wrong page.
func TestTranscriptSeekConsistentUnderBackfillInterleave(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	// Legacy data: two single-chunk blocks, the first holding the match.
	insertRawTranscriptBlocks(t, db, id, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")},
	})
	insertRawTranscriptBlocks(t, db, id, []TranscriptChunkRow{
		{Seq: 1, TabID: "tab-a", TS: 0, Data: []byte("b")},
	})
	// Kick the backfill concurrently with the seek: whichever way the
	// interleave lands, the answer is the reference's — seq 0, never the
	// seq-1 tail page.
	db.kickTranscriptTSIndexBackfill("")
	page, err := db.TranscriptSeekToTimePage(context.Background(), id, 1000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) == 0 || page.Chunks[0].Seq != 0 {
		t.Fatalf("seek page = %+v, want it to start at seq 0", page.Chunks)
	}
	// A later seek after the backfill completes agrees.
	waitForTranscriptTSIndexComplete(t, db, id)
	again, err := db.TranscriptSeekToTimePage(context.Background(), id, 1000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Chunks) == 0 || again.Chunks[0].Seq != 0 {
		t.Fatalf("post-backfill seek = %+v, want it to start at seq 0", again.Chunks)
	}
}

// waitForTranscriptTSIndexComplete polls the completeness flag until the
// backfill finishes or the deadline expires.
func waitForTranscriptTSIndexComplete(t *testing.T, db *Store, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		complete, err := db.transcriptTSIndexComplete(context.Background(), db.db, id)
		if err != nil {
			t.Fatal(err)
		}
		if complete {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("backfill never completed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A lowered index value on ANY eliminated block — not just the immediate
// predecessor — is caught by the verification scan: the block before the
// candidate is decoded and its true maximum timestamp is checked against
// its index row, so a hidden match is explicit corruption, never a later
// page.
func TestTranscriptSeekNonAdjacentLowCorruptionFails(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendPagedFixture(t, db, id, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")},
	})
	appendPagedFixture(t, db, id, []TranscriptChunkRow{
		{Seq: 1, TabID: "tab-a", TS: 0, Data: []byte("b")},
	})
	appendPagedFixture(t, db, id, []TranscriptChunkRow{
		{Seq: 2, TabID: "tab-a", TS: 2000, Data: []byte("c")},
	})
	// Corrupt the index: block 0's max_ts is lowered below its true max.
	if err := db.db.WithContext(context.Background()).Model(&model.TranscriptChunkTS{}).
		Where("transcript_id = ? AND block_seq = 0", id).
		Update("max_ts", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.transcriptTSIndexStateComplete(context.Background(), db.db, id); err != nil {
		t.Fatal(err)
	}
	// The naive answer is seq 0; the seek must report corruption, not
	// return the later seq-2 page.
	if _, err := db.TranscriptSeekToTimePage(context.Background(), id, 1000, 1<<20); err == nil {
		t.Fatal("seek over a non-adjacent low-corrupted index = nil error, want explicit corruption")
	}
}

// With no candidate the verification scan still checks every block before
// the tail page: a lowered index value on the only matching block is
// explicit corruption, not a silent tail page.
func TestTranscriptSeekNoCandidateLowCorruptionFails(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendPagedFixture(t, db, id, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")},
	})
	appendPagedFixture(t, db, id, []TranscriptChunkRow{
		{Seq: 1, TabID: "tab-a", TS: 0, Data: []byte("b")},
	})
	// Corrupt the index: block 0's max_ts is lowered below the target.
	if err := db.db.WithContext(context.Background()).Model(&model.TranscriptChunkTS{}).
		Where("transcript_id = ? AND block_seq = 0", id).
		Update("max_ts", 500).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.transcriptTSIndexStateComplete(context.Background(), db.db, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.TranscriptSeekToTimePage(context.Background(), id, 1000, 1<<20); err == nil {
		t.Fatal("seek with a low-corrupted no-candidate index = nil error, want explicit corruption")
	}
}

// A healthy replace is never a corruption, and the answer is always one
// consistent version: with the same blockSeq and the same index max_ts but
// different content (an ABA rewrite), the seek's snapshot yields one
// coherent page, not a mix of old and new.
func TestTranscriptSeekABAReplaceStaysConsistent(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	id := "dbg-aba-replace"
	row := TranscriptRow{ID: id, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}
	if err := db.TranscriptStart(ctx, row); err != nil {
		t.Fatal(err)
	}
	ended := int64(2000)
	row.EndedAt = &ended
	row.ContentOmitted = true
	// Old block: chunks (seq0,TS0),(seq1,TS1000).
	if err := db.TranscriptInsertSynced(ctx, row, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 0, Data: []byte("old0")},
		{Seq: 1, TabID: "tab-a", TS: 1000, Data: []byte("old1")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.transcriptTSIndexStateComplete(ctx, db.db, id); err != nil {
		t.Fatal(err)
	}
	// New block: same blockSeq and the same index max_ts, different content
	// (seq0,TS1000),(seq1,TS0) — the ABA rewrite.
	if err := db.TranscriptReplaceContent(ctx, id, 2, 2, false, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("new0")},
		{Seq: 1, TabID: "tab-a", TS: 0, Data: []byte("new1")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.transcriptTSIndexStateComplete(ctx, db.db, id); err != nil {
		t.Fatal(err)
	}
	// The seek's snapshot yields one coherent version: either the old
	// block's seq1 (TS1000) or the new block's seq0 (TS1000), never a mix.
	page, err := db.TranscriptSeekToTimePage(ctx, id, 1000, 1<<20)
	if err != nil {
		t.Fatalf("seek over an ABA-replaced block = %v, want one consistent page", err)
	}
	if len(page.Chunks) == 0 {
		t.Fatal("seek returned an empty page")
	}
	first := page.Chunks[0]
	if first.TS != 1000 {
		t.Fatalf("first chunk = seq %d TS %d, want a consistent version with TS 1000", first.Seq, first.TS)
	}
	// And the answer is consistent with the current content: the new
	// block's seq0 is the first-by-seq match.
	if first.Seq != 0 {
		t.Fatalf("first chunk seq = %d, want the new block's seq 0", first.Seq)
	}
}

// A healthy replace that lowers the block's timestamp is never a
// corruption: the seek's snapshot answers from one consistent version.
func TestTranscriptSeekHealthyReplaceLowersTS(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	id := "dbg-healthy-replace"
	row := TranscriptRow{ID: id, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}
	if err := db.TranscriptStart(ctx, row); err != nil {
		t.Fatal(err)
	}
	ended := int64(2000)
	row.EndedAt = &ended
	row.ContentOmitted = true
	if err := db.TranscriptInsertSynced(ctx, row, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 2000, Data: []byte("old")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.transcriptTSIndexStateComplete(ctx, db.db, id); err != nil {
		t.Fatal(err)
	}
	// Healthy replace lowering the block's timestamp from 2000 to 0.
	if err := db.TranscriptReplaceContent(ctx, id, 1, 1, false, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 0, Data: []byte("new")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.transcriptTSIndexStateComplete(ctx, db.db, id); err != nil {
		t.Fatal(err)
	}
	// The seek's snapshot answers from the current content: the new block's
	// TS=0 predates the target, so the answer is the tail page, never a
	// corruption error and never the stale TS=2000 page.
	page, err := db.TranscriptSeekToTimePage(ctx, id, 1000, 1<<20)
	if err != nil {
		t.Fatalf("seek over a healthy TS-lowering replace = %v", err)
	}
	if len(page.Chunks) == 0 || page.Chunks[0].TS != 0 {
		t.Fatalf("page = %+v, want the tail page from the current content", page.Chunks)
	}
}

// The backfill loop's defers run in the right order (Done last) and the
// lifecycle lock keeps Close's Add/Wait contract: an old loop finishing
// cannot unregister a new loop, and a kick racing Close cannot start work
// that Close would not join.
func TestBackfillDeferOrderAndCloseInterleave(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	insertRawTranscriptBlocks(t, db, id, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")},
	})
	// Kick a loop, let it finish, then kick again while Close runs: the
	// second loop is joined by Close and no duplicate escapes.
	db.kickTranscriptTSIndexBackfill("")
	waitForTranscriptTSIndexComplete(t, db, id)
	db.kickTranscriptTSIndexBackfill("")
	closed := make(chan error, 1)
	go func() { closed <- db.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close never returned; the second backfill loop was not joined")
	}
	goleak.VerifyNone(t)
}

// Persistently corrupt blocks never fill the selection window or stall the
// loop: after 64 corrupt blocks, another owner's large healthy legacy
// transcript still completes its backfill and its repeated seeks recover.
func TestTranscriptBackfillCorruptBlocksDontStallRecovery(t *testing.T) {
	db := testStore(t)
	ownerA := ipc.WithUserID(context.Background(), "user-a")
	ownerB := ipc.WithUserID(context.Background(), "user-b")
	// Owner B: 64 corrupt blocks that can never be indexed.
	corruptID := ids.New()
	if err := db.TranscriptStart(ownerB, TranscriptRow{ID: corruptID, SessionID: "sess-b", AssetID: "asset-b", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	for seq := 0; seq < 64; seq++ {
		if err := db.db.WithContext(context.Background()).Create(&TranscriptChunkRow{
			TranscriptID: corruptID, Seq: int64(seq), TabID: "tab-b", TS: int64(1000 + seq), Data: []byte("corrupt"), OwnerID: "user-b",
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Owner A: a large healthy legacy transcript (over the lowered scan
	// budget) that must still complete and recover.
	healthyID := ids.New()
	if err := db.TranscriptStart(ownerA, TranscriptRow{ID: healthyID, SessionID: "sess-a", AssetID: "asset-a", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	chunks := make([]TranscriptChunkRow, 0, 128*256)
	for seq := 0; seq < 128*256; seq++ {
		chunks = append(chunks, TranscriptChunkRow{Seq: int64(seq), TabID: "tab-a", TS: int64(1000 + seq), Data: []byte("x"), OwnerID: "user-a"})
		if len(chunks) == 65536 {
			if err := db.TranscriptAppendChunks(ownerA, healthyID, chunks); err != nil {
				t.Fatal(err)
			}
			chunks = chunks[:0]
		}
	}
	if len(chunks) > 0 {
		if err := db.TranscriptAppendChunks(ownerA, healthyID, chunks); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate legacy: drop the index rows and the state row so the
	// transcript is incomplete like pre-index data.
	if err := db.db.WithContext(context.Background()).Exec("DELETE FROM transcript_chunk_ts WHERE transcript_id = ?", healthyID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.db.WithContext(context.Background()).Exec("DELETE FROM transcript_ts_index WHERE transcript_id = ?", healthyID).Error; err != nil {
		t.Fatal(err)
	}
	// Lower the scan budget so the healthy transcript's seeks hit the
	// pending path until the backfill catches up.
	oldBudget := transcriptSeekScanBudget
	transcriptSeekScanBudget = 64
	defer func() { transcriptSeekScanBudget = oldBudget }()
	// Owner A's first seek reports pending (bounded pass cannot reach the
	// answer) and kicks the backfill; the corrupt blocks are skipped and
	// the healthy transcript completes.
	if _, err := db.TranscriptSeekToTimePage(ownerA, healthyID, 1000+30000, 1<<20); !errors.Is(err, ErrTranscriptSeekIndexing) {
		t.Fatalf("first seek = %v, want ErrTranscriptSeekIndexing", err)
	}
	waitForTranscriptTSIndexComplete(t, db, healthyID)
	// Repeated seeks now answer from the completed index.
	for i := 0; i < 3; i++ {
		page, err := db.TranscriptSeekToTimePage(ownerA, healthyID, 1000+30000, 1<<20)
		if err != nil {
			t.Fatalf("recovered seek %d = %v", i, err)
		}
		if len(page.Chunks) == 0 || page.Chunks[0].Seq != 30000 {
			t.Fatalf("recovered seek %d = %+v, want it to start at seq 30000", i, page.Chunks)
		}
	}
}

// A corrupt index row fails the seek explicitly: a high index value sends
// the candidate to a block whose content predates the target, and the
// mismatch is reported as corruption, never a silent tail page.
func TestTranscriptSeekHighCorruptionFails(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendPagedFixture(t, db, id, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")},
	})
	appendPagedFixture(t, db, id, []TranscriptChunkRow{
		{Seq: 1, TabID: "tab-a", TS: 1001, Data: []byte("b")},
	})
	// Corrupt the index: block 0's max_ts claims the block reaches far
	// beyond its content.
	if err := db.db.WithContext(context.Background()).Model(&model.TranscriptChunkTS{}).
		Where("transcript_id = ? AND block_seq = 0", id).
		Update("max_ts", 2000).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.transcriptTSIndexStateComplete(context.Background(), db.db, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.TranscriptSeekToTimePage(context.Background(), id, 1001, 1<<20); err == nil {
		t.Fatal("seek over a high-corrupted index = nil error, want explicit corruption")
	}
}

// A lowered index value on the elimination boundary is caught: the block
// before the candidate is verified, so a real match hidden by a lowered
// max_ts is reported as corruption, never a later page.
func TestTranscriptSeekLowCorruptionFails(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendPagedFixture(t, db, id, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")},
	})
	appendPagedFixture(t, db, id, []TranscriptChunkRow{
		{Seq: 1, TabID: "tab-a", TS: 1001, Data: []byte("b")},
	})
	// Corrupt the index: block 0's max_ts is lowered below its true max,
	// hiding the real match from the candidate query.
	if err := db.db.WithContext(context.Background()).Model(&model.TranscriptChunkTS{}).
		Where("transcript_id = ? AND block_seq = 0", id).
		Update("max_ts", 500).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.transcriptTSIndexStateComplete(context.Background(), db.db, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.TranscriptSeekToTimePage(context.Background(), id, 1000, 1<<20); err == nil {
		t.Fatal("seek over a low-corrupted index = nil error, want explicit corruption")
	}
}

// A candidate the index claims reaches the target but whose content has no
// qualifying chunk is corruption by construction: candidate-no-match is
// never a silent tail page.
func TestTranscriptSeekCandidateNoMatchFails(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendPagedFixture(t, db, id, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")},
	})
	// Corrupt the index: block 0's max_ts claims a chunk at the target,
	// but the block's only chunk predates it.
	if err := db.db.WithContext(context.Background()).Model(&model.TranscriptChunkTS{}).
		Where("transcript_id = ? AND block_seq = 0", id).
		Update("max_ts", 1500).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.transcriptTSIndexStateComplete(context.Background(), db.db, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.TranscriptSeekToTimePage(context.Background(), id, 1200, 1<<20); err == nil {
		t.Fatal("candidate-no-match = nil error, want explicit corruption")
	}
}

// Concurrent replace/sync rewriting a block mid-seek never yields a wrong
// page: every answer is either the pre-replace or the post-replace
// content, and a healthy replace never surfaces as corruption.
func TestTranscriptSeekConcurrentReplaceStaysConsistent(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	id := ids.New()
	row := TranscriptRow{ID: id, SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}
	if err := db.TranscriptStart(ctx, row); err != nil {
		t.Fatal(err)
	}
	ended := int64(2000)
	row.EndedAt = &ended
	row.ContentOmitted = true
	if err := db.TranscriptInsertSynced(ctx, row, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.transcriptTSIndexStateComplete(ctx, db.db, id); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			replacement := []TranscriptChunkRow{
				{Seq: 0, TabID: "tab-a", TS: int64(1000 + i), Data: []byte("r")},
			}
			if err := db.TranscriptReplaceContent(ctx, id, 1, 1, false, replacement); err != nil {
				t.Errorf("replace: %v", err)
				return
			}
			// ReplaceContent is one-shot per record: re-mark it as
			// metadata-only so the next round can rewrite it again.
			if err := db.db.WithContext(ctx).Model(&TranscriptRow{}).
				Where("id = ?", id).
				Update("content_omitted", true).Error; err != nil {
				t.Errorf("re-omit: %v", err)
				return
			}
		}
	}()
	for i := 0; i < 20; i++ {
		page, err := db.TranscriptSeekToTimePage(ctx, id, 1000, 1<<20)
		if err != nil {
			t.Fatalf("seek %d: %v", i, err)
		}
		if len(page.Chunks) == 0 {
			t.Fatalf("seek %d returned an empty page", i)
		}
	}
	<-done
}

// A persistently corrupt block never stalls the backfill: other
// transcripts complete on schedule, and the corrupt transcript's seek
// fails explicitly on the corrupt block instead of hanging or answering
// from a wrong page.
func TestTranscriptBackfillSkipsCorruptBlock(t *testing.T) {
	db := testStore(t)
	corruptID := startTranscript(t, db, "asset-1", 1000)
	insertRawTranscriptBlocks(t, db, corruptID, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("healthy")},
	})
	if err := db.db.WithContext(context.Background()).Create(&TranscriptChunkRow{
		TranscriptID: corruptID, Seq: 1, TabID: "tab-a", TS: 1001, Data: []byte("corrupt"),
	}).Error; err != nil {
		t.Fatal(err)
	}
	healthyID := startTranscript(t, db, "asset-1", 1000)
	appendPagedFixture(t, db, healthyID, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("fine")},
	})
	// The healthy transcript's append already indexed it; the corrupt one
	// is legacy and incomplete.
	db.kickTranscriptTSIndexBackfill("")
	waitForTranscriptTSIndexComplete(t, db, healthyID)
	// The corrupt transcript's seek fails explicitly on the corrupt block.
	if _, err := db.TranscriptSeekToTimePage(context.Background(), corruptID, 1001, 1<<20); err == nil {
		t.Fatal("seek over a corrupt block = nil error, want explicit failure")
	}
	// And the healthy transcript still answers.
	page, err := db.TranscriptSeekToTimePage(context.Background(), healthyID, 1000, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) == 0 || page.Chunks[0].Seq != 0 {
		t.Fatalf("healthy seek = %+v, want it to start at seq 0", page.Chunks)
	}
}

// The backfill prioritizes the seeking owner's transcripts: another
// owner's data is never scanned by the seek, and the owner's own
// transcript completes first.
func TestTranscriptBackfillOwnerScheduling(t *testing.T) {
	db := testStore(t)
	ownerA := ipc.WithUserID(context.Background(), "user-a")
	ownerB := ipc.WithUserID(context.Background(), "user-b")
	idA := ids.New()
	if err := db.TranscriptStart(ownerA, TranscriptRow{ID: idA, SessionID: "sess-a", AssetID: "asset-a", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	insertRawTranscriptBlocks(t, db, idA, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a"), OwnerID: "user-a"},
	})
	idB := ids.New()
	if err := db.TranscriptStart(ownerB, TranscriptRow{ID: idB, SessionID: "sess-b", AssetID: "asset-b", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	insertRawTranscriptBlocks(t, db, idB, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-b", TS: 1000, Data: []byte("b"), OwnerID: "user-b"},
	})
	// Owner A's seek kicks the backfill: A's transcript completes first,
	// and the seek never touches B's data.
	db.kickTranscriptTSIndexBackfill("user-a")
	waitForTranscriptTSIndexComplete(t, db, idA)
	page, err := db.TranscriptSeekToTimePage(ownerA, idA, 1000, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) == 0 || page.Chunks[0].Seq != 0 {
		t.Fatalf("owner A seek = %+v, want it to start at seq 0", page.Chunks)
	}
	// Owner B's transcript completes too, and B's seek is owner-isolated.
	waitForTranscriptTSIndexComplete(t, db, idB)
	if _, err := db.TranscriptSeekToTimePage(ownerA, idB, 1000, 1<<20); err == nil {
		t.Fatal("cross-owner seek = nil error, want not-found")
	} else {
		requireCode(t, err, ipc.CodeNotFound)
	}
}

// Close cancels and joins the backfill loop: the store closes promptly
// and leaves no goroutine behind.
func TestStoreCloseJoinsBackfill(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	insertRawTranscriptBlocks(t, db, id, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")},
	})
	db.kickTranscriptTSIndexBackfill("")
	closed := make(chan error, 1)
	go func() { closed <- db.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close never returned; the backfill loop was not joined")
	}
	goleak.VerifyNone(t)
}

// A corrupt stored block fails the seek explicitly instead of answering
// from partial data.
func TestTranscriptSeekCorruptBlockFailsExplicitly(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	insertRawTranscriptBlocks(t, db, id, []TranscriptChunkRow{{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")}})
	if err := db.db.WithContext(context.Background()).Create(&TranscriptChunkRow{
		TranscriptID: id, Seq: 1, TabID: "tab-a", TS: 1001, Data: []byte("not-a-block"),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := db.TranscriptSeekToTimePage(context.Background(), id, 1001, 1<<20); err == nil {
		t.Fatal("seek over a corrupt block = nil error, want an explicit failure")
	}
}

// The straddling-block cap handles blocks whose chunk seqs span far more
// than one block span: a block holding sparse chunks with huge seq gaps
// still yields the chunks just below the cursor.
func TestTranscriptPageBeforeWithSparseSeqStraddle(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	// One stored block holding chunks with 5000-wide seq gaps.
	appendPagedFixture(t, db, id, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")},
		{Seq: 5000, TabID: "tab-a", TS: 1001, Data: []byte("b")},
		{Seq: 10000, TabID: "tab-a", TS: 1002, Data: []byte("c")},
	})
	all := naiveTranscriptChunks(t, db, id)

	// The page before seq 6000 must include seq 5000 — it lives in the
	// block whose stored seq (10000) is far above the cursor.
	page, err := db.TranscriptPageBefore(context.Background(), id, 6000, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) != 2 || page.Chunks[0].Seq != 0 || page.Chunks[1].Seq != 5000 {
		t.Fatalf("page = %+v, want seqs 0 and 5000", page.Chunks)
	}
	if !page.HasAfter {
		t.Fatal("page must report the seq-10000 chunk as later history")
	}
	requirePageParity(t, page, naiveFirstScreen(all[:2], 1<<20), all)
	// The page before seq 5001 ends at seq 5000 with the straddler excluded.
	upper, err := db.TranscriptPageBefore(context.Background(), id, 5001, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(upper.Chunks) != 2 || upper.Chunks[1].Seq != 5000 {
		t.Fatalf("upper page = %+v, want seqs 0 and 5000", upper.Chunks)
	}
	// The first-screen tail read still reaches the newest chunk.
	tail, err := db.TranscriptFirstScreenPage(context.Background(), id, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	requirePageParity(t, tail, naiveFirstScreen(all, 1<<20), all)
}

// The first-screen tail read is inclusive: a chunk stored at the maximum
// seq is part of the tail page, never dropped by the exclusive cursor.
func TestTranscriptFirstScreenIncludesMaxSeqChunk(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendPagedFixture(t, db, id, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")},
		{Seq: 1, TabID: "tab-a", TS: 1001, Data: []byte("b")},
	})
	appendPagedFixture(t, db, id, []TranscriptChunkRow{
		{Seq: math.MaxInt64 - 1, TabID: "tab-a", TS: 1002, Data: []byte("c")},
		{Seq: math.MaxInt64, TabID: "tab-a", TS: 1003, Data: []byte("d")},
	})
	all := naiveTranscriptChunks(t, db, id)
	if len(all) != 4 {
		t.Fatalf("reference read %d chunks, want 4", len(all))
	}

	// The tail page includes the max-seq chunk with its earlier history.
	tail, err := db.TranscriptFirstScreenPage(context.Background(), id, 3)
	if err != nil {
		t.Fatal(err)
	}
	requirePageParity(t, tail, naiveFirstScreen(all, 3), all)
	if tail.LastSeq != math.MaxInt64 {
		t.Fatalf("tail page last seq = %d, want %d", tail.LastSeq, int64(math.MaxInt64))
	}
	if !tail.HasBefore {
		t.Fatal("tail page must report the earlier chunks")
	}
	// An exclusive page before MaxInt64 excludes the max-seq chunk but keeps
	// everything below it.
	exclusive, err := db.TranscriptPageBefore(context.Background(), id, math.MaxInt64, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(exclusive.Chunks) != 3 || exclusive.Chunks[0].Seq != 0 || exclusive.Chunks[1].Seq != 1 || exclusive.Chunks[2].Seq != math.MaxInt64-1 {
		t.Fatalf("exclusive page = %+v, want seqs 0, 1, and %d", exclusive.Chunks, int64(math.MaxInt64-1))
	}
	if !exclusive.HasAfter {
		t.Fatal("exclusive page must report the max-seq chunk as later history")
	}
}
