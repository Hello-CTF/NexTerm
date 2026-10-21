package store

import (
	"context"
	"fmt"
	"math/rand"
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
		after = page[len(page)-1].Seq + 1
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

func requirePageParity(t *testing.T, page TranscriptPage, want []TranscriptChunkRow) {
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
		return
	}
	if page.FirstSeq != want[0].Seq || page.LastSeq != want[len(want)-1].Seq {
		t.Fatalf("page bounds = [%d,%d], want [%d,%d]", page.FirstSeq, page.LastSeq, want[0].Seq, want[len(want)-1].Seq)
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
	requirePageParity(t, page, naiveFirstScreen(all, 1000))
	if !page.HasBefore || page.HasAfter {
		t.Fatalf("first screen bounds = before %v after %v, want true/false", page.HasBefore, page.HasAfter)
	}

	whole, err := db.TranscriptFirstScreenPage(context.Background(), id, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	requirePageParity(t, whole, naiveFirstScreen(all, 8<<20))
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
		requirePageParity(t, page, want)
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
		requirePageParity(t, page, naiveSeekToTime(all, at, 4096))
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
		requirePageParity(t, page, naiveFirstScreen(all, maxBytes))
	}
	for _, at := range []int64{999, 1000, 1001, 1002, 1003, 1005, 1010, 1050, 1100} {
		page, err := db.TranscriptSeekToTimePage(context.Background(), id, at, 2048)
		if err != nil {
			t.Fatal(err)
		}
		requirePageParity(t, page, naiveSeekToTime(all, at, 2048))
	}
	cursor := all[len(all)-1].Seq + 1
	remaining := append([]TranscriptChunkRow(nil), all...)
	for len(remaining) > 0 {
		page, err := db.TranscriptPageBefore(context.Background(), id, cursor, 3000)
		if err != nil {
			t.Fatal(err)
		}
		want := naiveFirstScreen(remaining, 3000)
		requirePageParity(t, page, want)
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

// The sparse-window seek is bounded: probe count stays logarithmic in the
// block population on the adversarial plateau shape, and the answer always
// matches the naive linear scan.
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
				index := int(seq / 256)
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
			budget := 4*bits(testCase.count) + 16
			if probes > budget {
				t.Fatalf("seek used %d probes, want <= %d", probes, budget)
			}
		})
	}
}

func bits(count int) int {
	bits := 0
	for count > 0 {
		bits++
		count >>= 1
	}
	return bits
}
