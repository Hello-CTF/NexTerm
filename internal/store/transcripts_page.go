package store

import (
	"context"
	"math"
	"slices"

	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

// TranscriptPage is one bounded page of transcript chunks in ascending seq
// order, together with the cursors a reader needs to page in both
// directions: FirstSeq/LastSeq delimit the page, and HasBefore/HasAfter
// report whether more history exists on either side.
type TranscriptPage struct {
	Chunks    []TranscriptChunkRow
	FirstSeq  int64
	LastSeq   int64
	HasBefore bool
	HasAfter  bool
}

const (
	transcriptPageMaxBytes     = 8 << 20
	transcriptPageDefaultBytes = 1 << 20
	transcriptPageMaxChunks    = 4096
)

func normalizeTranscriptPageBudget(maxBytes int) int {
	if maxBytes <= 0 || maxBytes > transcriptPageMaxBytes {
		return transcriptPageDefaultBytes
	}
	return maxBytes
}

// TranscriptFirstScreenPage reads the transcript's tail page straight from
// the newest blocks: the cost is the page size, never a rescan of the full
// history, matching the reference model's FirstScreen tail read. The read
// is inclusive — a chunk stored at the maximum seq belongs to the tail.
func (s *Store) TranscriptFirstScreenPage(ctx context.Context, transcriptID string, maxBytes int) (TranscriptPage, error) {
	return s.transcriptPageBefore(ctx, transcriptID, math.MaxInt64, maxBytes, true)
}

// TranscriptPageBefore reads the page of chunks ending just before beforeSeq:
// blocks stream newest-first and chunks accumulate backward until the byte
// budget is crossed (the crossing chunk is included, mirroring the forward
// reader) or the chunk cap is hit. The newest chunk before beforeSeq is
// always included. Direction flags answer real chunk existence through
// bounded probes: flush failures can leave holes in the seq space and
// imported transcripts can start at any seq, so adjacency arithmetic is
// never trusted.
func (s *Store) TranscriptPageBefore(ctx context.Context, transcriptID string, beforeSeq int64, maxBytes int) (TranscriptPage, error) {
	return s.transcriptPageBefore(ctx, transcriptID, beforeSeq, maxBytes, false)
}

// transcriptPageBefore implements the backward page read; inclusive makes
// the cursor bound include a chunk at exactly beforeSeq (the tail read).
func (s *Store) transcriptPageBefore(ctx context.Context, transcriptID string, beforeSeq int64, maxBytes int, inclusive bool) (TranscriptPage, error) {
	if _, err := s.TranscriptGet(ctx, transcriptID); err != nil {
		return TranscriptPage{}, err
	}
	budget := normalizeTranscriptPageBudget(maxBytes)
	chunk := query.Use(s.db).TranscriptChunkRow
	blocks := chunk.WithContext(ctx).
		Where(chunk.TranscriptID.Eq(transcriptID)).
		Where(ownerConditions(ctx, chunk.TableName())...)
	// A block's seq is its last chunk's seq, so the block straddling
	// beforeSeq can carry chunks just below it; widen the block predicate by
	// one maximum block span and filter precisely per chunk below. Guard the
	// addition so a MaxInt64 cursor (the first-screen read) never wraps.
	if beforeSeq <= math.MaxInt64-transcriptBlockMaxItems {
		blocks = blocks.Where(chunk.Seq.Lt(beforeSeq + transcriptBlockMaxItems))
	}
	rows, err := blocks.Order(chunk.Seq.Desc()).Rows()
	if err != nil {
		return TranscriptPage{}, dbError(err)
	}
	defer rows.Close()

	reversed := make([]TranscriptChunkRow, 0, min(budget, 64))
	collect := true
	for collect && rows.Next() {
		var stored TranscriptChunkRow
		if err := s.db.ScanRows(rows, &stored); err != nil {
			return TranscriptPage{}, dbError(err)
		}
		chunks, err := decodeTranscriptBlock(stored)
		if err != nil {
			return TranscriptPage{}, err
		}
		for index := len(chunks) - 1; index >= 0; index-- {
			if inclusive {
				if chunks[index].Seq > beforeSeq {
					continue
				}
			} else if chunks[index].Seq >= beforeSeq {
				continue
			}
			reversed = append(reversed, chunks[index])
			budget -= len(chunks[index].Data)
			if budget <= 0 || len(reversed) >= transcriptPageMaxChunks {
				collect = false
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return TranscriptPage{}, dbError(err)
	}
	if err := rows.Close(); err != nil {
		return TranscriptPage{}, dbError(err)
	}
	slices.Reverse(reversed)
	page := transcriptPageFromChunks(reversed)
	if err := s.transcriptPageBounds(ctx, transcriptID, &page); err != nil {
		return TranscriptPage{}, err
	}
	return page, nil
}

// TranscriptPageAfter reads the page of chunks starting at afterSeq (the
// first chunk with seq >= afterSeq is always included), with the same
// budget semantics as the forward reader it shares the block scan with.
func (s *Store) TranscriptPageAfter(ctx context.Context, transcriptID string, afterSeq int64, maxBytes int) (TranscriptPage, error) {
	if _, err := s.TranscriptGet(ctx, transcriptID); err != nil {
		return TranscriptPage{}, err
	}
	budget := normalizeTranscriptPageBudget(maxBytes)
	chunk := query.Use(s.db).TranscriptChunkRow
	rows, err := chunk.WithContext(ctx).
		Where(chunk.TranscriptID.Eq(transcriptID), chunk.Seq.Gte(afterSeq)).
		Where(ownerConditions(ctx, chunk.TableName())...).
		Order(chunk.Seq).
		Rows()
	if err != nil {
		return TranscriptPage{}, dbError(err)
	}
	defer rows.Close()

	collected := make([]TranscriptChunkRow, 0, min(budget, 64))
	collect := true
	for collect && rows.Next() {
		var stored TranscriptChunkRow
		if err := s.db.ScanRows(rows, &stored); err != nil {
			return TranscriptPage{}, dbError(err)
		}
		chunks, err := decodeTranscriptBlock(stored)
		if err != nil {
			return TranscriptPage{}, err
		}
		for _, row := range chunks {
			if row.Seq < afterSeq {
				continue
			}
			collected = append(collected, row)
			budget -= len(row.Data)
			if budget <= 0 || len(collected) >= transcriptPageMaxChunks {
				collect = false
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return TranscriptPage{}, dbError(err)
	}
	if err := rows.Close(); err != nil {
		return TranscriptPage{}, dbError(err)
	}
	page := transcriptPageFromChunks(collected)
	if err := s.transcriptPageBounds(ctx, transcriptID, &page); err != nil {
		return TranscriptPage{}, err
	}
	return page, nil
}

// TranscriptSeekToTimePage reads the page starting at the first chunk with
// TS >= atMS; a time past the last chunk yields the final (tail) page, and
// duplicate timestamps resolve to the first chunk of the group — the same
// semantics as the reference model's SeekToTime. For transcripts with
// non-decreasing chunk timestamps (every transcript written through this
// store, verified for legacy data) the seek is a bounded binary search
// over the block seq keyspace; legacy data with out-of-order timestamps
// falls back to the sound full-scan, so any timestamp order resolves
// correctly and no transcript size ever fails the seek.
func (s *Store) TranscriptSeekToTimePage(ctx context.Context, transcriptID string, atMS int64, maxBytes int) (TranscriptPage, error) {
	if _, err := s.TranscriptGet(ctx, transcriptID); err != nil {
		return TranscriptPage{}, err
	}
	first, last, found, err := s.transcriptBlockBoundaries(ctx, transcriptID)
	if err != nil {
		return TranscriptPage{}, err
	}
	if !found {
		return TranscriptPage{Chunks: []TranscriptChunkRow{}}, nil
	}
	status, ok := transcriptOrderStatusOf(transcriptID)
	if !ok {
		status, err = s.transcriptValidateOrder(ctx, transcriptID)
		if err != nil {
			return TranscriptPage{}, err
		}
		transcriptOrderMark(transcriptID, status)
	}
	var seq int64
	var foundMatch bool
	if status == transcriptOrderOrdered {
		seq, foundMatch, err = s.seekFirstChunkOrdered(ctx, transcriptID, first, last, atMS)
	} else {
		seq, foundMatch, err = s.seekFirstChunkAtLeast(ctx, transcriptID, atMS)
	}
	if err != nil {
		return TranscriptPage{}, err
	}
	if !foundMatch {
		return s.TranscriptFirstScreenPage(ctx, transcriptID, maxBytes)
	}
	return s.TranscriptPageAfter(ctx, transcriptID, seq, maxBytes)
}

// transcriptValidateOrder decodes every block of the transcript and reports
// whether chunk timestamps are non-decreasing along the seq order — the
// invariant the bounded index requires. A corrupt block fails explicitly.
func (s *Store) transcriptValidateOrder(ctx context.Context, transcriptID string) (transcriptOrderStatus, error) {
	chunk := query.Use(s.db).TranscriptChunkRow
	rows, err := chunk.WithContext(ctx).
		Where(chunk.TranscriptID.Eq(transcriptID)).
		Where(ownerConditions(ctx, chunk.TableName())...).
		Order(chunk.Seq).
		Rows()
	if err != nil {
		return transcriptOrderUnknown, dbError(err)
	}
	defer rows.Close()
	var lastTS int64
	started := false
	for rows.Next() {
		var stored TranscriptChunkRow
		if err := s.db.ScanRows(rows, &stored); err != nil {
			return transcriptOrderUnknown, dbError(err)
		}
		chunks, err := decodeTranscriptBlock(stored)
		if err != nil {
			return transcriptOrderUnknown, err
		}
		for _, row := range chunks {
			if started && row.TS < lastTS {
				return transcriptOrderDisordered, nil
			}
			lastTS = row.TS
			started = true
		}
	}
	if err := rows.Err(); err != nil {
		return transcriptOrderUnknown, dbError(err)
	}
	return transcriptOrderOrdered, nil
}

// transcriptBlockProbe resolves the first stored block whose seq is >= the
// given seq, reporting the block's seq and its stored timestamp (the last
// chunk's TS, which is the block's maximum under the order invariant).
type transcriptBlockProbe func(seq int64) (blockSeq, blockTS int64, ok bool)

// seekFirstBlockAtLeast returns the seq of the first block whose maximum
// chunk timestamp is >= at, or false when every block predates at. Under
// the order invariant block timestamps are non-decreasing along the block
// seq order, so the search binary-searches the seq keyspace through the
// probe: each round either halves the remaining keyspace or steps to a
// real block boundary, keeping the probe count logarithmic in the seq
// range instead of scanning the block population.
func seekFirstBlockAtLeast(probe transcriptBlockProbe, firstSeq, lastSeq, at int64) (int64, bool) {
	lo, hi := firstSeq, lastSeq
	var candidate int64
	found := false
	for lo <= hi {
		mid := lo + (hi-lo)/2
		blockSeq, blockTS, ok := probe(mid)
		if !ok || blockSeq > hi {
			// No block at or above mid inside the range: everything from
			// mid upward is empty keyspace.
			hi = mid - 1
			continue
		}
		if blockTS >= at {
			candidate, found = blockSeq, true
			if blockSeq <= lo {
				break
			}
			hi = blockSeq - 1
			continue
		}
		// Monotonicity: blocks at or below blockSeq predate at as well.
		if blockSeq >= hi {
			break
		}
		lo = blockSeq + 1
	}
	return candidate, found
}

// seekFirstChunkOrdered resolves the first chunk with TS >= at through the
// bounded index: a binary search over the block seq keyspace locates the
// first block whose maximum timestamp reaches at, and only that block is
// decoded. Every probe error fails the seek explicitly.
func (s *Store) seekFirstChunkOrdered(ctx context.Context, transcriptID string, first, last, atMS int64) (int64, bool, error) {
	var probeErr error
	probe := func(seq int64) (int64, int64, bool) {
		blockSeq, blockTS, ok, err := s.transcriptBlockAtOrAfter(ctx, transcriptID, seq)
		if err != nil {
			probeErr = err
			return 0, 0, false
		}
		if !ok {
			return 0, 0, false
		}
		return blockSeq, blockTS, true
	}
	blockSeq, found := seekFirstBlockAtLeast(probe, first, last, atMS)
	if probeErr != nil {
		return 0, false, probeErr
	}
	if !found {
		return 0, false, nil
	}
	block, err := s.transcriptBlockBySeq(ctx, transcriptID, blockSeq)
	if err != nil {
		return 0, false, err
	}
	chunks, err := decodeTranscriptBlock(block)
	if err != nil {
		return 0, false, err
	}
	for _, row := range chunks {
		if row.TS >= atMS {
			return row.Seq, true, nil
		}
	}
	return 0, false, nil
}

// transcriptBlockBoundaries reads the seq of the oldest and newest blocks
// of the transcript; found is false for an empty transcript.
func (s *Store) transcriptBlockBoundaries(ctx context.Context, transcriptID string) (first, last int64, found bool, err error) {
	oldest, _, oldestFound, err := s.transcriptBlockBoundary(ctx, transcriptID, false)
	if err != nil || !oldestFound {
		return 0, 0, false, err
	}
	newest, _, newestFound, err := s.transcriptBlockBoundary(ctx, transcriptID, true)
	if err != nil || !newestFound {
		return 0, 0, false, err
	}
	return oldest, newest, true, nil
}

// transcriptBlockBoundary reads the seq and max TS of the oldest (or
// newest) block of the transcript; found is false for an empty transcript.
func (s *Store) transcriptBlockBoundary(ctx context.Context, transcriptID string, last bool) (seq, ts int64, found bool, err error) {
	chunk := query.Use(s.db).TranscriptChunkRow
	blocks := chunk.WithContext(ctx).
		Select(chunk.Seq, chunk.TS).
		Where(chunk.TranscriptID.Eq(transcriptID)).
		Where(ownerConditions(ctx, chunk.TableName())...)
	if last {
		blocks = blocks.Order(chunk.Seq.Desc())
	} else {
		blocks = blocks.Order(chunk.Seq)
	}
	row, err := blocks.Take()
	if isNoRows(err) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, dbError(err)
	}
	return row.Seq, row.TS, true, nil
}

// transcriptBlockAtOrAfter resolves the first block with seq >= the given
// seq, reporting the block's seq and its stored timestamp.
func (s *Store) transcriptBlockAtOrAfter(ctx context.Context, transcriptID string, seq int64) (blockSeq, blockTS int64, ok bool, err error) {
	chunk := query.Use(s.db).TranscriptChunkRow
	row, err := chunk.WithContext(ctx).
		Select(chunk.Seq, chunk.TS).
		Where(chunk.TranscriptID.Eq(transcriptID), chunk.Seq.Gte(seq)).
		Where(ownerConditions(ctx, chunk.TableName())...).
		Order(chunk.Seq).
		Take()
	if isNoRows(err) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, dbError(err)
	}
	return row.Seq, row.TS, true, nil
}

// transcriptBlockBySeq fetches one stored block row for decoding.
func (s *Store) transcriptBlockBySeq(ctx context.Context, transcriptID string, blockSeq int64) (TranscriptChunkRow, error) {
	chunk := query.Use(s.db).TranscriptChunkRow
	row, err := chunk.WithContext(ctx).
		Where(chunk.TranscriptID.Eq(transcriptID), chunk.Seq.Eq(blockSeq)).
		Where(ownerConditions(ctx, chunk.TableName())...).
		Take()
	if isNoRows(err) {
		return TranscriptChunkRow{}, notFound("会话记录")
	}
	if err != nil {
		return TranscriptChunkRow{}, dbError(err)
	}
	return *row, nil
}

// transcriptDecodedBlock is one decoded stored block (ascending chunks).
type transcriptDecodedBlock []TranscriptChunkRow

// seekFirstChunkAtLeast returns the seq of the first chunk with TS >= at by
// decoding blocks in seq order through next (nil block = exhausted); found
// is false when every chunk predates at. The scan is sound under any
// timestamp order.
func seekFirstChunkAtLeast(next func() (transcriptDecodedBlock, error), at int64) (int64, bool, error) {
	for {
		block, err := next()
		if err != nil {
			return 0, false, err
		}
		if block == nil {
			return 0, false, nil
		}
		for _, row := range block {
			if row.TS >= at {
				return row.Seq, true, nil
			}
		}
	}
}

// seekFirstChunkAtLeast scans the transcript's blocks in seq order and
// returns the first chunk with TS >= atMS — the sound full-scan fallback
// for out-of-order legacy data. The rows cursor is closed before returning
// so follow-up page reads never race it.
func (s *Store) seekFirstChunkAtLeast(ctx context.Context, transcriptID string, atMS int64) (int64, bool, error) {
	chunk := query.Use(s.db).TranscriptChunkRow
	rows, err := chunk.WithContext(ctx).
		Where(chunk.TranscriptID.Eq(transcriptID)).
		Where(ownerConditions(ctx, chunk.TableName())...).
		Order(chunk.Seq).
		Rows()
	if err != nil {
		return 0, false, dbError(err)
	}
	next := func() (transcriptDecodedBlock, error) {
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return nil, dbError(err)
			}
			return nil, nil
		}
		var stored TranscriptChunkRow
		if err := s.db.ScanRows(rows, &stored); err != nil {
			return nil, dbError(err)
		}
		chunks, err := decodeTranscriptBlock(stored)
		if err != nil {
			return nil, err
		}
		return transcriptDecodedBlock(chunks), nil
	}
	seq, found, err := seekFirstChunkAtLeast(next, atMS)
	if closeErr := rows.Close(); err == nil && closeErr != nil {
		return 0, false, dbError(closeErr)
	}
	return seq, found, err
}

// transcriptChunkBeforeExists reports whether any chunk with seq < bound
// exists, using at most two block probes and one block decode: a block
// whose stored seq is below the bound holds only older chunks, and the
// first block at or above the bound may straddle it and carry older ones.
func (s *Store) transcriptChunkBeforeExists(ctx context.Context, transcriptID string, bound int64) (bool, error) {
	chunk := query.Use(s.db).TranscriptChunkRow
	_, err := chunk.WithContext(ctx).
		Select(chunk.Seq).
		Where(chunk.TranscriptID.Eq(transcriptID), chunk.Seq.Lt(bound)).
		Where(ownerConditions(ctx, chunk.TableName())...).
		Order(chunk.Seq.Desc()).
		Take()
	if err != nil && !isNoRows(err) {
		return false, dbError(err)
	}
	if err == nil {
		return true, nil
	}
	straddler, err := chunk.WithContext(ctx).
		Where(chunk.TranscriptID.Eq(transcriptID), chunk.Seq.Gte(bound)).
		Where(ownerConditions(ctx, chunk.TableName())...).
		Order(chunk.Seq).
		Take()
	if isNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, dbError(err)
	}
	chunks, err := decodeTranscriptBlock(*straddler)
	if err != nil {
		return false, err
	}
	for _, row := range chunks {
		if row.Seq < bound {
			return true, nil
		}
	}
	return false, nil
}

// transcriptChunkAfterExists reports whether any chunk with seq > bound
// exists, using at most two block probes and one block decode: a block
// whose stored seq is above the bound ends in a newer chunk, and the block
// holding the bound may carry newer chunks behind it.
func (s *Store) transcriptChunkAfterExists(ctx context.Context, transcriptID string, bound int64) (bool, error) {
	chunk := query.Use(s.db).TranscriptChunkRow
	_, err := chunk.WithContext(ctx).
		Select(chunk.Seq).
		Where(chunk.TranscriptID.Eq(transcriptID), chunk.Seq.Gt(bound)).
		Where(ownerConditions(ctx, chunk.TableName())...).
		Order(chunk.Seq).
		Take()
	if err != nil && !isNoRows(err) {
		return false, dbError(err)
	}
	if err == nil {
		return true, nil
	}
	straddler, err := chunk.WithContext(ctx).
		Where(chunk.TranscriptID.Eq(transcriptID), chunk.Seq.Gte(bound)).
		Where(ownerConditions(ctx, chunk.TableName())...).
		Order(chunk.Seq).
		Take()
	if isNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, dbError(err)
	}
	chunks, err := decodeTranscriptBlock(*straddler)
	if err != nil {
		return false, err
	}
	for _, row := range chunks {
		if row.Seq > bound {
			return true, nil
		}
	}
	return false, nil
}

// transcriptPageFromChunks assembles the page bounds from the page's own
// chunks.
func transcriptPageFromChunks(chunks []TranscriptChunkRow) TranscriptPage {
	page := TranscriptPage{Chunks: chunks}
	if len(chunks) == 0 {
		return page
	}
	page.FirstSeq = chunks[0].Seq
	page.LastSeq = chunks[len(chunks)-1].Seq
	return page
}

// transcriptPageBounds fills the direction flags through bounded existence
// probes, so flush-failure holes and non-zero seq starts are reported
// exactly.
func (s *Store) transcriptPageBounds(ctx context.Context, transcriptID string, page *TranscriptPage) error {
	if len(page.Chunks) == 0 {
		return nil
	}
	var err error
	page.HasBefore, err = s.transcriptChunkBeforeExists(ctx, transcriptID, page.FirstSeq)
	if err != nil {
		return err
	}
	page.HasAfter, err = s.transcriptChunkAfterExists(ctx, transcriptID, page.LastSeq)
	return err
}
