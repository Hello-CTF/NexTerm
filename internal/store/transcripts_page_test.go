package store

import (
	"context"
	"fmt"
	"math"
	"math/bits"
	"math/rand"
	"sort"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
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

// The full-scan fallback is disorder-tolerant: it finds the first chunk
// with TS >= at under ANY timestamp order and resolves duplicate groups
// spanning block boundaries to the first matching chunk by seq.
func TestSeekFirstChunkAtLeastBounded(t *testing.T) {
	cases := []struct {
		name  string
		count int
		tsAt  func(index int) int64
		at    int64
	}{
		{"uniform plateau", 4096, func(int) int64 { return 100 }, 100},
		{"strictly increasing", 4096, func(index int) int64 { return int64(index) }, 2048},
		{"late step", 4096, func(index int) int64 { return int64(index / 4095) }, 1},
		{"all before", 1024, func(index int) int64 { return int64(index) }, 5000},
		{"single chunk hit", 1, func(int) int64 { return 10 }, 10},
		{"out of order early spike", 512, func(index int) int64 {
			if index == 3 {
				return 9999
			}
			return int64(index)
		}, 5000},
		{"out of order late dip", 512, func(index int) int64 {
			if index >= 256 {
				return 1
			}
			return int64(index)
		}, 100},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			blocks := make([]transcriptDecodedBlock, 0, (testCase.count+255)/256)
			for start := 0; start < testCase.count; start += 256 {
				end := min(start+256, testCase.count)
				block := make(transcriptDecodedBlock, 0, end-start)
				for index := start; index < end; index++ {
					block = append(block, TranscriptChunkRow{Seq: int64(index), TS: testCase.tsAt(index)})
				}
				blocks = append(blocks, block)
			}
			decoded := 0
			next := func() (transcriptDecodedBlock, error) {
				if decoded >= len(blocks) {
					return nil, nil
				}
				block := blocks[decoded]
				decoded++
				return block, nil
			}
			got, found, err := seekFirstChunkAtLeast(next, testCase.at)
			if err != nil {
				t.Fatalf("seek: %v", err)
			}
			want, wantFound := int64(0), false
			for index := 0; index < testCase.count; index++ {
				if testCase.tsAt(index) >= testCase.at {
					want, wantFound = int64(index), true
					break
				}
			}
			if found != wantFound || got != want {
				t.Fatalf("seek = (%d,%v), want (%d,%v)", got, found, want, wantFound)
			}
			if decoded > len(blocks) {
				t.Fatalf("seek decoded %d blocks, want <= %d", decoded, len(blocks))
			}
		})
	}
}

// The bounded index is sound under the order invariant: the binary search
// over the block seq keyspace finds the first block whose maximum timestamp
// reaches the target with a logarithmic probe count, matching the naive
// linear scan on every shape.
func TestSeekFirstBlockAtLeastBounded(t *testing.T) {
	cases := []struct {
		name  string
		count int
		tsAt  func(index int) int64
		at    int64
	}{
		{"uniform plateau", 4096, func(int) int64 { return 100 }, 100},
		{"strictly increasing", 4096, func(index int) int64 { return int64(index) }, 2048},
		{"late step", 4096, func(index int) int64 { return int64(index / 4095) }, 1},
		{"all before", 1024, func(index int) int64 { return int64(index) }, 5000},
		{"single block hit", 1, func(int) int64 { return 10 }, 10},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			blockSeqs := make([]int64, testCase.count)
			blockTS := make([]int64, testCase.count)
			for index := 0; index < testCase.count; index++ {
				blockSeqs[index] = int64(index*256 + 255)
				blockTS[index] = testCase.tsAt(index)
			}
			probes := 0
			probe := func(seq int64) (int64, int64, bool) {
				probes++
				index := sort.Search(len(blockSeqs), func(index int) bool { return blockSeqs[index] >= seq })
				if index >= testCase.count {
					return 0, 0, false
				}
				return blockSeqs[index], blockTS[index], true
			}
			got, found := seekFirstBlockAtLeast(probe, blockSeqs[0], blockSeqs[len(blockSeqs)-1], testCase.at)
			want, wantFound := int64(0), false
			for index, ts := range blockTS {
				if ts >= testCase.at {
					want, wantFound = blockSeqs[index], true
					break
				}
			}
			if found != wantFound || got != want {
				t.Fatalf("seek = (%d,%v), want (%d,%v)", got, found, want, wantFound)
			}
			if budget := 2*bits.Len64(uint64(testCase.count)) + 16; probes > budget {
				t.Fatalf("seek used %d probes, want <= %d", probes, budget)
			}
		})
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
// legacy data written before the timestamp-order invariant existed.
func insertRawTranscriptBlocks(t *testing.T, db *Store, transcriptID string, chunks []TranscriptChunkRow) {
	t.Helper()
	blocks, err := encodeTranscriptChunks(chunks)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range blocks {
		block.TranscriptID = transcriptID
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

// The write invariant: appends reject out-of-order timestamps with badParam
// and no state change, so every transcript written through this store has
// non-decreasing chunk timestamps and the bounded binary-search seek is
// sound for it.
func TestTranscriptAppendRejectsOutOfOrderTimestamps(t *testing.T) {
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendPagedFixture(t, db, id, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")},
		{Seq: 1, TabID: "tab-a", TS: 1002, Data: []byte("b")},
	})
	// Within-batch disorder: seq 3's TS steps below seq 2's.
	err := db.TranscriptAppendChunks(context.Background(), id, []TranscriptChunkRow{
		{Seq: 2, TabID: "tab-a", TS: 1003, Data: []byte("c")},
		{Seq: 3, TabID: "tab-a", TS: 1001, Data: []byte("d")},
	})
	requireCode(t, err, ipc.CodeBadParam)
	// Against the newest block: seq 4's TS steps below seq 1's.
	err = db.TranscriptAppendChunks(context.Background(), id, []TranscriptChunkRow{
		{Seq: 4, TabID: "tab-a", TS: 1001, Data: []byte("e")},
	})
	requireCode(t, err, ipc.CodeBadParam)

	row, err := db.TranscriptGet(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Chunks != 2 {
		t.Fatalf("rejected appends changed the chunk count to %d, want 2", row.Chunks)
	}
	all := naiveTranscriptChunks(t, db, id)
	if len(all) != 2 || all[0].Seq != 0 || all[1].Seq != 1 {
		t.Fatalf("stored chunks = %+v, want the original two", all)
	}
	// The ordered seek still answers after rejected appends.
	page, err := db.TranscriptSeekToTimePage(context.Background(), id, 1002, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	requirePageParity(t, page, naiveSeekToTime(all, 1002, 1<<20), all)
}

// The sync and replace write paths enforce the same invariant.
func TestTranscriptSyncedAndReplaceRejectOutOfOrderTimestamps(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	row := TranscriptRow{ID: ids.New(), SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}
	if err := db.TranscriptStart(ctx, row); err != nil {
		t.Fatal(err)
	}
	ended := int64(2000)
	row.EndedAt = &ended
	err := db.TranscriptInsertSynced(ctx, row, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1001, Data: []byte("a")},
		{Seq: 1, TabID: "tab-a", TS: 1000, Data: []byte("b")},
	})
	requireCode(t, err, ipc.CodeBadParam)
	if err := db.TranscriptInsertSynced(ctx, row, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1000, Data: []byte("a")},
		{Seq: 1, TabID: "tab-a", TS: 1001, Data: []byte("b")},
	}); err != nil {
		t.Fatal(err)
	}
	err = db.TranscriptReplaceContent(ctx, row.ID, 2, 2, false, []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-a", TS: 1002, Data: []byte("c")},
		{Seq: 1, TabID: "tab-a", TS: 1001, Data: []byte("d")},
	})
	requireCode(t, err, ipc.CodeBadParam)
	all := naiveTranscriptChunks(t, db, row.ID)
	if len(all) != 2 || all[0].TS != 1000 || all[1].TS != 1001 {
		t.Fatalf("stored chunks = %+v, want the ordered synced pair", all)
	}
}

// Legacy ordered data (written before the invariant) validates on first
// seek and then takes the bounded binary-search path, with parity against
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
