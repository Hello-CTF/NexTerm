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
// history, matching the reference model's FirstScreen tail read.
func (s *Store) TranscriptFirstScreenPage(ctx context.Context, transcriptID string, maxBytes int) (TranscriptPage, error) {
	return s.TranscriptPageBefore(ctx, transcriptID, math.MaxInt64, maxBytes)
}

// TranscriptPageBefore reads the page of chunks ending just before beforeSeq:
// blocks stream newest-first and chunks accumulate backward until the byte
// budget is crossed (the crossing chunk is included, mirroring the forward
// reader) or the chunk cap is hit. The newest chunk before beforeSeq is
// always included. Chunk seqs are contiguous per transcript (the same
// contract the forward reader's Done computation relies on), so the page
// bounds answer HasBefore/HasAfter exactly.
func (s *Store) TranscriptPageBefore(ctx context.Context, transcriptID string, beforeSeq int64, maxBytes int) (TranscriptPage, error) {
	row, err := s.TranscriptGet(ctx, transcriptID)
	if err != nil {
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
			if chunks[index].Seq >= beforeSeq {
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
	slices.Reverse(reversed)
	return transcriptPageFromChunks(row.Chunks, reversed), nil
}

// TranscriptPageAfter reads the page of chunks starting at afterSeq (the
// first chunk with seq >= afterSeq is always included), with the same
// budget semantics as the forward reader it shares the block scan with.
func (s *Store) TranscriptPageAfter(ctx context.Context, transcriptID string, afterSeq int64, maxBytes int) (TranscriptPage, error) {
	row, err := s.TranscriptGet(ctx, transcriptID)
	if err != nil {
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

	page := make([]TranscriptChunkRow, 0, min(budget, 64))
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
			page = append(page, row)
			budget -= len(row.Data)
			if budget <= 0 || len(page) >= transcriptPageMaxChunks {
				collect = false
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return TranscriptPage{}, dbError(err)
	}
	return transcriptPageFromChunks(row.Chunks, page), nil
}

// TranscriptSeekToTimePage reads the page starting at the first chunk with
// TS >= atMS; a time past the last chunk yields the final (tail) page, and
// duplicate timestamps resolve to the first chunk of the group — the same
// semantics as the reference model's SeekToTime. The distant seek is
// bounded: block timestamps are non-decreasing along the block seq order
// (the writer contract the reference model enforces), so the target block
// is found by binary-searching the seq keyspace instead of scanning the
// history, and only that one block is decoded to locate the exact chunk.
func (s *Store) TranscriptSeekToTimePage(ctx context.Context, transcriptID string, atMS int64, maxBytes int) (TranscriptPage, error) {
	if _, err := s.TranscriptGet(ctx, transcriptID); err != nil {
		return TranscriptPage{}, err
	}
	first, _, found, err := s.transcriptBlockBoundary(ctx, transcriptID, false)
	if err != nil {
		return TranscriptPage{}, err
	}
	if !found {
		return TranscriptPage{Chunks: []TranscriptChunkRow{}}, nil
	}
	last, _, _, err := s.transcriptBlockBoundary(ctx, transcriptID, true)
	if err != nil {
		return TranscriptPage{}, err
	}
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
		return TranscriptPage{}, probeErr
	}
	if !found {
		return s.TranscriptFirstScreenPage(ctx, transcriptID, maxBytes)
	}
	block, err := s.transcriptBlockBySeq(ctx, transcriptID, blockSeq)
	if err != nil {
		return TranscriptPage{}, err
	}
	chunks, err := decodeTranscriptBlock(block)
	if err != nil {
		return TranscriptPage{}, err
	}
	for _, row := range chunks {
		if row.TS >= atMS {
			return s.TranscriptPageAfter(ctx, transcriptID, row.Seq, maxBytes)
		}
	}
	return s.TranscriptFirstScreenPage(ctx, transcriptID, maxBytes)
}

// transcriptBlockProbe resolves the first stored block whose seq is >= the
// given seq, reporting the block's seq and its maximum chunk timestamp
// (stored on the block row as its last chunk's TS).
type transcriptBlockProbe func(seq int64) (blockSeq, blockTS int64, ok bool)

// seekFirstBlockAtLeast returns the seq of the first block whose maximum
// chunk timestamp is >= at, or false when every block predates at. Block
// timestamps are non-decreasing along the block seq order, so the search
// binary-searches the seq keyspace through the probe: each round either
// halves the remaining keyspace or steps to a real block boundary, keeping
// the probe count logarithmic in the seq range instead of scanning the
// block population.
func seekFirstBlockAtLeast(probe transcriptBlockProbe, firstSeq, lastSeq, at int64) (int64, bool) {
	lo, hi := firstSeq, lastSeq
	var candidate int64
	found := false
	for lo <= hi {
		mid := lo + (hi-lo)/2
		blockSeq, blockTS, ok := probe(mid)
		if !ok || blockSeq > hi {
			// No block at or above mid inside the range: everything from mid
			// upward is empty keyspace.
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
// seq, reporting the block's seq and its stored max TS.
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

// transcriptPageFromChunks assembles the page and derives the direction
// flags from the transcript's total chunk count (chunk seqs are contiguous
// per transcript).
func transcriptPageFromChunks(total int64, chunks []TranscriptChunkRow) TranscriptPage {
	page := TranscriptPage{Chunks: chunks}
	if len(chunks) == 0 {
		return page
	}
	page.FirstSeq = chunks[0].Seq
	page.LastSeq = chunks[len(chunks)-1].Seq
	page.HasBefore = page.FirstSeq > 0
	page.HasAfter = page.LastSeq+1 < total
	return page
}
