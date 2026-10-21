package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
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
// semantics as the reference model's SeekToTime. With a complete index the
// seek is a bounded candidate query plus one verified decode. While a
// legacy transcript's index is still being built, the seek runs one
// snapshot-consistent bounded pass over the blocks in seq order, decoding
// only blocks the index cannot eliminate, and reports an explicit pending
// state when the pass cannot reach the answer — never an unbounded scan
// and never a wrong page.
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
	if complete {
		seq, found, err := s.seekIndexedAtLeast(ctx, transcriptID, atMS)
		if err != nil {
			return TranscriptPage{}, err
		}
		if !found {
			return s.TranscriptFirstScreenPage(ctx, transcriptID, maxBytes)
		}
		return s.TranscriptPageAfter(ctx, transcriptID, seq, maxBytes)
	}
	s.kickTranscriptTSIndexBackfill(ipc.OwnerID(ctx))
	seq, found, pending, err := s.scanBlocksAtLeast(ctx, transcriptID, atMS, transcriptSeekScanBudget)
	if err != nil {
		return TranscriptPage{}, err
	}
	if pending {
		return TranscriptPage{}, ErrTranscriptSeekIndexing
	}
	if !found {
		return s.TranscriptFirstScreenPage(ctx, transcriptID, maxBytes)
	}
	return s.TranscriptPageAfter(ctx, transcriptID, seq, maxBytes)
}

// transcriptSeekScanBudget bounds the block rows one incomplete-index pass
// decodes before it reports the explicit pending state.
const transcriptSeekScanBudget = 8192

// ErrTranscriptSeekIndexing reports that the transcript's timestamp index
// is still being built and the bounded pass could not reach the answer in
// this pass; the caller retries once the backfill advances. It is an
// explicit, visible state — never an error page and never a silent
// unsupported answer.
var ErrTranscriptSeekIndexing = ipc.NewError(ipc.Code("transcript_indexing"), "transcript timestamp index is still building; retry shortly")

// transcriptIndexCorruptError reports that a stored block's content does
// not match its timestamp-index row: the index cannot be trusted for this
// transcript and the seek fails explicitly instead of answering from a
// wrong page.
func transcriptIndexCorruptError(transcriptID string, blockSeq int64) error {
	return ipc.NewError(ipc.Code("transcript_corrupt"), fmt.Sprintf("transcript timestamp index does not match the stored content at block %d", blockSeq))
}

// seekIndexedAtLeast is the complete-index fast path: the candidate query
// picks the first block whose maximum timestamp reaches at, and the
// verified decode returns its first qualifying chunk. The elimination
// boundary — the block immediately before the candidate — is verified too:
// a lowered index value there would silently hide a real match, so a
// stable mismatch is reported as corruption, never a later page. A
// concurrent replace/sync in flight keeps the index row changing, so the
// seek retries until the row stabilizes or the deadline passes; a stable
// but wrong index row is corruption by construction and fails explicitly,
// never a silent tail page.
func (s *Store) seekIndexedAtLeast(ctx context.Context, transcriptID string, atMS int64) (int64, bool, error) {
	deadline := time.Now().Add(transcriptSeekReplaceDeadline)
	for attempt := 0; ; attempt++ {
		blockSeq, found, err := s.transcriptIndexedBlockAtLeast(ctx, transcriptID, atMS)
		if err != nil {
			return 0, false, err
		}
		if !found {
			return 0, false, nil
		}
		if err := s.verifyEliminationBoundary(ctx, transcriptID, blockSeq, atMS); err != nil {
			return 0, false, err
		}
		seq, match, retry, err := s.verifiedFirstChunkAtLeast(ctx, transcriptID, blockSeq, atMS)
		if err != nil {
			return 0, false, err
		}
		if match {
			return seq, true, nil
		}
		if !retry || time.Now().After(deadline) {
			return 0, false, transcriptIndexCorruptError(transcriptID, blockSeq)
		}
	}
}

// transcriptSeekReplaceDeadline bounds how long a seek retries while a
// concurrent replace keeps the index row changing.
const transcriptSeekReplaceDeadline = 2 * time.Second

// verifyEliminationBoundary decodes the block immediately before the
// candidate and checks its true maximum timestamp against its index row:
// a lowered index value there would silently hide a real match, so a
// stable mismatch — or a true maximum reaching the target — is reported
// as corruption instead of a later page. A changing index row means a
// concurrent replace just landed: the candidate's own versioned check
// decides, so the boundary check stands down.
func (s *Store) verifyEliminationBoundary(ctx context.Context, transcriptID string, candidateSeq, atMS int64) error {
	boundary, _, found, err := s.transcriptBlockBefore(ctx, transcriptID, candidateSeq)
	if err != nil || !found {
		return err
	}
	indexedTS1, indexed1, err := s.transcriptIndexedBlockTS(ctx, transcriptID, boundary)
	if err != nil {
		return err
	}
	chunks, err := s.verifiedBlockChunks(ctx, transcriptID, boundary)
	if err != nil {
		return err
	}
	maxTS := chunks[0].TS
	for _, chunk := range chunks[1:] {
		if chunk.TS > maxTS {
			maxTS = chunk.TS
		}
	}
	indexedTS2, indexed2, err := s.transcriptIndexedBlockTS(ctx, transcriptID, boundary)
	if err != nil {
		return err
	}
	if indexed1 != indexed2 || indexedTS1 != indexedTS2 {
		return nil
	}
	if indexed1 && indexedTS1 != maxTS {
		return transcriptIndexCorruptError(transcriptID, boundary)
	}
	if maxTS >= atMS {
		return transcriptIndexCorruptError(transcriptID, boundary)
	}
	return nil
}

// verifiedBlockChunks decodes one stored block with verification.
func (s *Store) verifiedBlockChunks(ctx context.Context, transcriptID string, blockSeq int64) ([]TranscriptChunkRow, error) {
	block, err := s.transcriptBlockBySeq(ctx, transcriptID, blockSeq)
	if err != nil {
		return nil, err
	}
	return decodeTranscriptBlockVerified(block)
}

// verifiedFirstChunkAtLeast decodes the candidate block and returns its
// first qualifying chunk. The block's true maximum timestamp is checked
// against the index row read before and after the decode: a stable but
// wrong row is corruption by construction and fails explicitly; a
// changing row means a concurrent replace just landed and the caller
// retries. A vanished block is likewise a retry, never a wrong page.
func (s *Store) verifiedFirstChunkAtLeast(ctx context.Context, transcriptID string, blockSeq, atMS int64) (seq int64, match, retry bool, err error) {
	indexedTS1, indexed1, err := s.transcriptIndexedBlockTS(ctx, transcriptID, blockSeq)
	if err != nil {
		return 0, false, false, err
	}
	block, err := s.transcriptBlockBySeq(ctx, transcriptID, blockSeq)
	if err != nil {
		return 0, false, true, nil
	}
	chunks, err := decodeTranscriptBlockVerified(block)
	if err != nil {
		return 0, false, false, err
	}
	maxTS := chunks[0].TS
	for _, chunk := range chunks[1:] {
		if chunk.TS > maxTS {
			maxTS = chunk.TS
		}
	}
	indexedTS2, indexed2, err := s.transcriptIndexedBlockTS(ctx, transcriptID, blockSeq)
	if err != nil {
		return 0, false, false, err
	}
	if indexed1 != indexed2 || indexedTS1 != indexedTS2 {
		return 0, false, true, nil
	}
	if indexed1 && indexedTS1 != maxTS {
		return 0, false, false, transcriptIndexCorruptError(transcriptID, blockSeq)
	}
	for _, row := range chunks {
		if row.TS >= atMS {
			return row.Seq, true, false, nil
		}
	}
	return 0, false, false, nil
}

// transcriptIndexedBlockTS reads one index row's maximum timestamp for the
// corruption check.
func (s *Store) transcriptIndexedBlockTS(ctx context.Context, transcriptID string, blockSeq int64) (int64, bool, error) {
	query := s.db.WithContext(ctx).Model(&model.TranscriptChunkTS{}).
		Select("max_ts").
		Where("transcript_id = ? AND block_seq = ?", transcriptID, blockSeq)
	if ownerID := ipc.OwnerID(ctx); ownerID != "" {
		query = query.Where("owner_id = ?", ownerID)
	}
	var maxTS int64
	result := query.Scan(&maxTS)
	if result.Error != nil {
		return 0, false, dbError(result.Error)
	}
	if result.RowsAffected == 0 {
		return 0, false, nil
	}
	return maxTS, true, nil
}

// scanBlocksAtLeast runs one snapshot-consistent bounded pass over the
// transcript's blocks in seq order: every block is either eliminated by a
// valid index row (its maximum timestamp is below the target, which no
// content change can outgrow) or decoded and checked. The pass is bounded
// by a block-row budget; exhausting it with the index still incomplete
// reports pending so the caller retries once the backfill advances.
func (s *Store) scanBlocksAtLeast(ctx context.Context, transcriptID string, atMS int64, budget int) (int64, bool, bool, error) {
	query := s.db.WithContext(ctx).Model(&model.TranscriptChunkRow{}).
		Select("transcript_chunk.*, idx.max_ts AS indexed_max_ts").
		Joins("LEFT JOIN transcript_chunk_ts idx ON idx.transcript_id = transcript_chunk.transcript_id AND idx.block_seq = transcript_chunk.seq").
		Where("transcript_chunk.transcript_id = ?", transcriptID).
		Order("transcript_chunk.seq")
	if ownerID := ipc.OwnerID(ctx); ownerID != "" {
		query = query.Where("transcript_chunk.owner_id = ?", ownerID)
	}
	rows, err := query.Rows()
	if err != nil {
		return 0, false, false, dbError(err)
	}
	defer rows.Close()
	scanned := 0
	for rows.Next() {
		scanned++
		if scanned > budget {
			return 0, false, true, nil
		}
		var stored struct {
			model.TranscriptChunkRow
			IndexedMaxTS sql.NullInt64 `gorm:"column:indexed_max_ts"`
		}
		if err := s.db.ScanRows(rows, &stored); err != nil {
			return 0, false, false, dbError(err)
		}
		if stored.IndexedMaxTS.Valid && stored.IndexedMaxTS.Int64 < atMS {
			continue
		}
		chunks, err := decodeTranscriptBlockVerified(stored.TranscriptChunkRow)
		if err != nil {
			return 0, false, false, err
		}
		if stored.IndexedMaxTS.Valid {
			maxTS := chunks[0].TS
			for _, chunk := range chunks[1:] {
				if chunk.TS > maxTS {
					maxTS = chunk.TS
				}
			}
			if maxTS != stored.IndexedMaxTS.Int64 {
				// A concurrent replace may have rewritten the block after
				// the cursor read its index row: re-read the index once and
				// only call it corruption when the fresh row still
				// disagrees with the fresh block.
				fresh, ok, err := s.transcriptIndexedBlockTS(ctx, transcriptID, stored.Seq)
				if err != nil {
					return 0, false, false, err
				}
				if !ok || fresh != maxTS {
					return 0, false, false, transcriptIndexCorruptError(transcriptID, stored.Seq)
				}
			}
		}
		for _, row := range chunks {
			if row.TS >= atMS {
				return row.Seq, true, false, nil
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, false, false, dbError(err)
	}
	return 0, false, false, nil
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

// transcriptBlockBefore resolves the newest block with seq < the given
// seq, reporting the block's seq and its stored timestamp.
func (s *Store) transcriptBlockBefore(ctx context.Context, transcriptID string, seq int64) (blockSeq, blockTS int64, ok bool, err error) {
	chunk := query.Use(s.db).TranscriptChunkRow
	row, err := chunk.WithContext(ctx).
		Select(chunk.Seq, chunk.TS).
		Where(chunk.TranscriptID.Eq(transcriptID), chunk.Seq.Lt(seq)).
		Where(ownerConditions(ctx, chunk.TableName())...).
		Order(chunk.Seq.Desc()).
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
