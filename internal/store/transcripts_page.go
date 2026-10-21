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
	// beforeSeq can carry chunks just below it — and a block's chunk seqs
	// can span far more than one block span when chunks are sparse. The
	// only block at or above beforeSeq that can hold older chunks is the
	// FIRST one; every later block starts above beforeSeq. Cap the scan at
	// that straddling block instead of a fixed seq window.
	straddler, _, straddles, err := s.transcriptBlockAtOrAfter(ctx, transcriptID, beforeSeq)
	if err != nil {
		return TranscriptPage{}, err
	}
	if straddles {
		blocks = blocks.Where(chunk.Seq.Lte(straddler))
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
// semantics as the reference model's SeekToTime. The distant seek is
// bounded and correct under any timestamp order: the per-block timestamp
// index eliminates every block whose maximum timestamp is below the target
// (such a block provably holds no qualifying chunk), and the first block
// reaching the target is decoded for the exact chunk. While a legacy
// transcript's index is still backfilling, the unindexed remainder is
// scanned soundly in seq order; the backfill advances in the background so
// the remainder disappears instead of rescanning on every seek.
func (s *Store) TranscriptSeekToTimePage(ctx context.Context, transcriptID string, atMS int64, maxBytes int) (TranscriptPage, error) {
	if _, err := s.TranscriptGet(ctx, transcriptID); err != nil {
		return TranscriptPage{}, err
	}
	_, _, found, err := s.transcriptBlockBoundaries(ctx, transcriptID)
	if err != nil {
		return TranscriptPage{}, err
	}
	if !found {
		return TranscriptPage{Chunks: []TranscriptChunkRow{}}, nil
	}
	complete, err := s.transcriptTSIndexComplete(ctx, transcriptID)
	if err != nil {
		return TranscriptPage{}, err
	}
	if !complete {
		s.kickTranscriptTSIndexBackfill()
	}
	answerSeq, answerFound, err := s.transcriptIndexedBlockAtLeast(ctx, transcriptID, atMS)
	if err != nil {
		return TranscriptPage{}, err
	}
	if answerFound {
		answerSeq, answerFound, err = s.firstChunkAtLeastInBlock(ctx, transcriptID, answerSeq, atMS)
		if err != nil {
			return TranscriptPage{}, err
		}
	}
	if !complete {
		suffixSeq, suffixFound, err := s.scanUnindexedChunksAtLeast(ctx, transcriptID, atMS)
		if err != nil {
			return TranscriptPage{}, err
		}
		if suffixFound && (!answerFound || suffixSeq < answerSeq) {
			answerSeq, answerFound = suffixSeq, true
		}
	}
	if !answerFound {
		return s.TranscriptFirstScreenPage(ctx, transcriptID, maxBytes)
	}
	return s.TranscriptPageAfter(ctx, transcriptID, answerSeq, maxBytes)
}

// firstChunkAtLeastInBlock decodes the candidate block and returns the seq
// of its first chunk with TS >= atMS.
func (s *Store) firstChunkAtLeastInBlock(ctx context.Context, transcriptID string, blockSeq, atMS int64) (int64, bool, error) {
	block, err := s.transcriptBlockBySeq(ctx, transcriptID, blockSeq)
	if err != nil {
		return 0, false, err
	}
	chunks, err := decodeTranscriptBlockVerified(block)
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
