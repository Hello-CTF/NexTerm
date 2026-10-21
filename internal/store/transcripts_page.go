package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"slices"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
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

// transcriptGet resolves the transcript row inside a transaction, with the
// same owner-scoped not-found semantics as TranscriptGet.
func (s *Store) transcriptGet(ctx context.Context, tx *gorm.DB, transcriptID string) (TranscriptRow, error) {
	transcript := query.Use(tx).TranscriptRow
	row, err := transcript.WithContext(ctx).
		Where(transcript.ID.Eq(transcriptID)).
		Where(ownerConditions(ctx, transcript.TableName())...).
		First()
	if isNoRows(err) {
		return TranscriptRow{}, notFound("会话记录")
	}
	if err != nil {
		return TranscriptRow{}, dbError(err)
	}
	return *row, nil
}

// TranscriptFirstScreenPage reads the transcript's tail page straight from
// the newest blocks: the cost is the page size, never a rescan of the full
// history, matching the reference model's FirstScreen tail read. The read
// is inclusive — a chunk stored at the maximum seq belongs to the tail.
func (s *Store) TranscriptFirstScreenPage(ctx context.Context, transcriptID string, maxBytes int) (TranscriptPage, error) {
	return s.transcriptPageBefore(ctx, s.db, transcriptID, math.MaxInt64, maxBytes, true)
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
	return s.transcriptPageBefore(ctx, s.db, transcriptID, beforeSeq, maxBytes, false)
}

// transcriptPageBefore implements the backward page read; inclusive makes
// the cursor bound include a chunk at exactly beforeSeq (the tail read).
func (s *Store) transcriptPageBefore(ctx context.Context, tx *gorm.DB, transcriptID string, beforeSeq int64, maxBytes int, inclusive bool) (TranscriptPage, error) {
	if _, err := s.transcriptGet(ctx, tx, transcriptID); err != nil {
		return TranscriptPage{}, err
	}
	budget := normalizeTranscriptPageBudget(maxBytes)
	chunk := query.Use(tx).TranscriptChunkRow
	blocks := chunk.WithContext(ctx).
		Where(chunk.TranscriptID.Eq(transcriptID)).
		Where(ownerConditions(ctx, chunk.TableName())...)
	// A block's seq is its last chunk's seq, so the block straddling
	// beforeSeq can carry chunks just below it — and a block's chunk seqs
	// can span far more than one block span when chunks are sparse. The
	// only block at or above beforeSeq that can hold older chunks is the
	// FIRST one; every later block starts above beforeSeq. Cap the scan at
	// that straddling block instead of a fixed seq window.
	straddler, _, straddles, err := s.transcriptBlockAtOrAfter(ctx, tx, transcriptID, beforeSeq)
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
		if err := tx.ScanRows(rows, &stored); err != nil {
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
	if err := s.transcriptPageBounds(ctx, tx, transcriptID, &page); err != nil {
		return TranscriptPage{}, err
	}
	return page, nil
}

// TranscriptPageAfter reads the page of chunks starting at afterSeq (the
// first chunk with seq >= afterSeq is always included), with the same
// budget semantics as the forward reader it shares the block scan with.
func (s *Store) TranscriptPageAfter(ctx context.Context, transcriptID string, afterSeq int64, maxBytes int) (TranscriptPage, error) {
	return s.transcriptPageAfter(ctx, s.db, transcriptID, afterSeq, maxBytes)
}

// transcriptPageAfter reads the page of chunks starting at afterSeq.
func (s *Store) transcriptPageAfter(ctx context.Context, tx *gorm.DB, transcriptID string, afterSeq int64, maxBytes int) (TranscriptPage, error) {
	if _, err := s.transcriptGet(ctx, tx, transcriptID); err != nil {
		return TranscriptPage{}, err
	}
	budget := normalizeTranscriptPageBudget(maxBytes)
	chunk := query.Use(tx).TranscriptChunkRow
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
		if err := tx.ScanRows(rows, &stored); err != nil {
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
	if err := s.transcriptPageBounds(ctx, tx, transcriptID, &page); err != nil {
		return TranscriptPage{}, err
	}
	return page, nil
}

// transcriptFirstScreenPageTx is the tail page read inside the seek's
// snapshot transaction.
func (s *Store) transcriptFirstScreenPageTx(ctx context.Context, tx *gorm.DB, transcriptID string, maxBytes int) (TranscriptPage, error) {
	return s.transcriptPageBefore(ctx, tx, transcriptID, math.MaxInt64, maxBytes, true)
}

// transcriptPageAfterTx is the forward page read inside the seek's snapshot
// transaction.
func (s *Store) transcriptPageAfterTx(ctx context.Context, tx *gorm.DB, transcriptID string, afterSeq int64, maxBytes int) (TranscriptPage, error) {
	return s.transcriptPageAfter(ctx, tx, transcriptID, afterSeq, maxBytes)
}

// TranscriptSeekToTimePage reads the page starting at the first chunk with
// TS >= atMS; a time past the last chunk yields the final (tail) page, and
// duplicate timestamps resolve to the first chunk of the group — the same
// semantics as the reference model's SeekToTime. The whole seek runs in one
// read-only REPEATABLE READ transaction, so the candidate query, the
// verification scan, and the final page all read one snapshot: a
// concurrent replace or an in-flight backfill can never mix old and new
// content or masquerade as corruption. With a complete index the seek
// verifies only the blocks beyond the persisted verification watermark and
// answers from the candidate block; when the bounded pass cannot reach the
// answer in this call it commits the watermark advance and reports the
// explicit pending state — the next call resumes where this one stopped,
// never an unbounded scan, never a wrong page, and never a silently
// unsupported answer.
func (s *Store) TranscriptSeekToTimePage(ctx context.Context, transcriptID string, atMS int64, maxBytes int) (TranscriptPage, error) {
	var page TranscriptPage
	var pending bool
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		page, pending, err = s.transcriptSeekToTimePageTx(ctx, tx, transcriptID, atMS, maxBytes)
		// A pending pass still commits: the watermark advance it made is
		// exactly what lets the next call resume instead of rescanning.
		if err != nil {
			return err
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return TranscriptPage{}, err
	}
	if pending {
		return TranscriptPage{}, ErrTranscriptSeekIndexing
	}
	return page, nil
}

// transcriptSeekToTimePageTx is the seek's implementation inside its
// consistent-snapshot transaction; pending reports that the bounded pass
// exhausted its budget before reaching the answer (the watermark advance
// is written either way).
func (s *Store) transcriptSeekToTimePageTx(ctx context.Context, tx *gorm.DB, transcriptID string, atMS int64, maxBytes int) (TranscriptPage, bool, error) {
	if _, err := s.transcriptGet(ctx, tx, transcriptID); err != nil {
		return TranscriptPage{}, false, err
	}
	_, _, found, err := s.transcriptBlockBoundaries(ctx, tx, transcriptID)
	if err != nil {
		return TranscriptPage{}, false, err
	}
	if !found {
		return TranscriptPage{Chunks: []TranscriptChunkRow{}}, false, nil
	}
	complete, err := s.transcriptTSIndexComplete(ctx, tx, transcriptID)
	if err != nil {
		return TranscriptPage{}, false, err
	}
	if complete {
		return s.seekIndexedAtLeastTx(ctx, tx, transcriptID, atMS, maxBytes)
	}
	s.kickTranscriptTSIndexBackfill(ipc.OwnerID(ctx))
	gen, err := s.transcriptContentGen(ctx, tx, transcriptID)
	if err != nil {
		return TranscriptPage{}, false, err
	}
	seq, found, pending, err := s.scanBlocksAtLeast(ctx, tx, transcriptID, atMS, gen, transcriptSeekScanBudget, 0, true)
	if err != nil {
		return TranscriptPage{}, false, err
	}
	if pending {
		return TranscriptPage{}, true, nil
	}
	if !found {
		page, err := s.transcriptFirstScreenPageTx(ctx, tx, transcriptID, maxBytes)
		return page, false, err
	}
	page, err := s.transcriptPageAfterTx(ctx, tx, transcriptID, seq, maxBytes)
	return page, false, err
}

// transcriptSeekScanBudget bounds the block rows one incomplete-index pass
// decodes before it reports the explicit pending state. It is a variable so
// tests can exercise the pending path without a multi-million-chunk
// fixture.
var transcriptSeekScanBudget = 8192

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

// seekIndexedAtLeastTx is the complete-index path inside the seek's
// snapshot. The candidate query picks the first block whose maximum
// timestamp reaches at; every block the watermark has not yet verified is
// decoded in a bounded pass — each block's true maximum timestamp must
// match its index row, so a lowered value cannot silently hide a match
// (corruption is explicit, never a later page) — and the watermark advance
// is committed with the seek so the next call resumes instead of
// rescanning. Once the candidate lies inside the verified prefix the seek
// answers from the candidate block alone, matching the reference model
// exactly.
func (s *Store) seekIndexedAtLeastTx(ctx context.Context, tx *gorm.DB, transcriptID string, atMS int64, maxBytes int) (TranscriptPage, bool, error) {
	gen, err := s.transcriptContentGen(ctx, tx, transcriptID)
	if err != nil {
		return TranscriptPage{}, false, err
	}
	verified, err := s.transcriptTSIndexVerifiedThrough(ctx, tx, transcriptID)
	if err != nil {
		return TranscriptPage{}, false, err
	}
	blockSeq, found, err := s.transcriptIndexedBlockAtLeast(ctx, tx, transcriptID, atMS, gen)
	if err != nil {
		return TranscriptPage{}, false, err
	}
	if !found {
		// No candidate: verify the unverified suffix before answering with
		// the tail page — a lowered index value must not silently hide a
		// match, and the bounded pass resumes across calls.
		advanced, pending, err := s.verifyIndexedPrefix(ctx, tx, transcriptID, gen, verified, 0, transcriptSeekScanBudget)
		if err != nil {
			return TranscriptPage{}, false, err
		}
		if err := s.transcriptTSIndexAdvanceVerified(ctx, tx, transcriptID, advanced); err != nil {
			return TranscriptPage{}, false, err
		}
		if pending {
			return TranscriptPage{}, true, nil
		}
		page, err := s.transcriptFirstScreenPageTx(ctx, tx, transcriptID, maxBytes)
		return page, false, err
	}
	if blockSeq > verified {
		// Verify the newly reached blocks up to the candidate: bounded per
		// call, resumable through the watermark, and a mismatch is explicit
		// corruption, never a silently eliminated match.
		advanced, pending, err := s.verifyIndexedPrefix(ctx, tx, transcriptID, gen, verified, blockSeq, transcriptSeekScanBudget)
		if err != nil {
			return TranscriptPage{}, false, err
		}
		if err := s.transcriptTSIndexAdvanceVerified(ctx, tx, transcriptID, advanced); err != nil {
			return TranscriptPage{}, false, err
		}
		if pending {
			return TranscriptPage{}, true, nil
		}
	}
	// The candidate is the first block with a qualifying chunk; its index
	// row is verified, so the first chunk with TS >= at inside it is the
	// answer — one block decode, never a scan.
	seq, err := s.transcriptFirstQualifyingChunkSeq(ctx, tx, transcriptID, blockSeq, atMS)
	if err != nil {
		return TranscriptPage{}, false, err
	}
	page, err := s.transcriptPageAfterTx(ctx, tx, transcriptID, seq, maxBytes)
	return page, false, err
}

// verifyIndexedPrefix decodes the transcript's blocks in (afterSeq,
// stopAtSeq] — or through the tail when stopAtSeq is 0 — checking each
// block's true maximum timestamp against its index row for the current
// content generation. It returns the last verified seq and reports pending
// when the budget ran out before the range was exhausted, so the caller
// advances the watermark and the next call resumes from there.
func (s *Store) verifyIndexedPrefix(ctx context.Context, tx *gorm.DB, transcriptID string, gen, afterSeq, stopAtSeq int64, budget int) (int64, bool, error) {
	query := tx.WithContext(ctx).Model(&model.TranscriptChunkRow{}).
		Select("transcript_chunk.*, idx.max_ts AS indexed_max_ts").
		Joins("LEFT JOIN transcript_chunk_ts idx ON idx.transcript_id = transcript_chunk.transcript_id AND idx.block_seq = transcript_chunk.seq AND idx.gen = ?", gen).
		Where("transcript_chunk.transcript_id = ?", transcriptID).
		Where("transcript_chunk.seq > ?", afterSeq).
		Order("transcript_chunk.seq")
	if stopAtSeq > 0 {
		query = query.Where("transcript_chunk.seq <= ?", stopAtSeq)
	}
	if ownerID := ipc.OwnerID(ctx); ownerID != "" {
		query = query.Where("transcript_chunk.owner_id = ?", ownerID)
	}
	rows, err := query.Rows()
	if err != nil {
		return 0, false, dbError(err)
	}
	defer rows.Close()
	advanced := afterSeq
	scanned := 0
	for rows.Next() {
		if scanned >= budget {
			return advanced, true, nil
		}
		scanned++
		var stored struct {
			model.TranscriptChunkRow
			IndexedMaxTS sql.NullInt64 `gorm:"column:indexed_max_ts"`
		}
		if err := tx.ScanRows(rows, &stored); err != nil {
			return 0, false, dbError(err)
		}
		chunks, err := decodeTranscriptBlockVerified(stored.TranscriptChunkRow)
		if err != nil {
			return 0, false, err
		}
		maxTS := chunks[0].TS
		for _, chunk := range chunks[1:] {
			if chunk.TS > maxTS {
				maxTS = chunk.TS
			}
		}
		if !stored.IndexedMaxTS.Valid || maxTS != stored.IndexedMaxTS.Int64 {
			return 0, false, transcriptIndexCorruptError(transcriptID, stored.Seq)
		}
		advanced = stored.Seq
	}
	if err := rows.Err(); err != nil {
		return 0, false, dbError(err)
	}
	return advanced, false, nil
}

// transcriptFirstQualifyingChunkSeq decodes one candidate block and returns
// its first chunk with TS >= at; a candidate whose index row claims a
// qualifying maximum but whose content holds none is explicit corruption.
func (s *Store) transcriptFirstQualifyingChunkSeq(ctx context.Context, tx *gorm.DB, transcriptID string, blockSeq, atMS int64) (int64, error) {
	block, err := s.transcriptBlockBySeq(ctx, tx, transcriptID, blockSeq)
	if err != nil {
		return 0, err
	}
	chunks, err := decodeTranscriptBlockVerified(block)
	if err != nil {
		return 0, err
	}
	for _, chunk := range chunks {
		if chunk.TS >= atMS {
			return chunk.Seq, nil
		}
	}
	return 0, transcriptIndexCorruptError(transcriptID, blockSeq)
}

// verifyEliminationBoundary decodes the block immediately before the
// candidate and checks its true maximum timestamp against its index row:
// transcriptIndexedBlockTS reads one index row's maximum timestamp for the
// corruption check, trusting only rows stamped with the transcript's
// current content generation.
func (s *Store) transcriptIndexedBlockTS(ctx context.Context, tx *gorm.DB, transcriptID string, blockSeq, gen int64) (int64, bool, error) {
	query := tx.WithContext(ctx).Model(&model.TranscriptChunkTS{}).
		Select("max_ts").
		Where("transcript_id = ? AND block_seq = ? AND gen = ?", transcriptID, blockSeq, gen)
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

// scanBlocksAtLeast streams the transcript's blocks in seq order from the
// start to stopAtSeq (or to the tail when stopAtSeq is 0), decoding every
// block and checking its true maximum timestamp against its index row for
// the current content generation: a mismatch is explicit corruption, never
// a silently eliminated match, and the first chunk with TS >= at is the
// answer, matching the reference model exactly. The pass is capped at
// budget block rows and reports pending on exhaustion, so an incomplete
// index never turns the seek into an unbounded scan.
func (s *Store) scanBlocksAtLeast(ctx context.Context, tx *gorm.DB, transcriptID string, atMS, gen int64, budget int, stopAtSeq int64, bounded bool) (int64, bool, bool, error) {
	query := tx.WithContext(ctx).Model(&model.TranscriptChunkRow{}).
		Select("transcript_chunk.*, idx.max_ts AS indexed_max_ts").
		Joins("LEFT JOIN transcript_chunk_ts idx ON idx.transcript_id = transcript_chunk.transcript_id AND idx.block_seq = transcript_chunk.seq AND idx.gen = ?", gen).
		Where("transcript_chunk.transcript_id = ?", transcriptID).
		Order("transcript_chunk.seq")
	if stopAtSeq > 0 {
		query = query.Where("transcript_chunk.seq <= ?", stopAtSeq)
	}
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
		if bounded && scanned > budget {
			return 0, false, true, nil
		}
		var stored struct {
			model.TranscriptChunkRow
			IndexedMaxTS sql.NullInt64 `gorm:"column:indexed_max_ts"`
		}
		if err := tx.ScanRows(rows, &stored); err != nil {
			return 0, false, false, dbError(err)
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
				return 0, false, false, transcriptIndexCorruptError(transcriptID, stored.Seq)
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
func (s *Store) transcriptBlockBoundaries(ctx context.Context, tx *gorm.DB, transcriptID string) (first, last int64, found bool, err error) {
	oldest, _, oldestFound, err := s.transcriptBlockBoundary(ctx, tx, transcriptID, false)
	if err != nil || !oldestFound {
		return 0, 0, false, err
	}
	newest, _, newestFound, err := s.transcriptBlockBoundary(ctx, tx, transcriptID, true)
	if err != nil || !newestFound {
		return 0, 0, false, err
	}
	return oldest, newest, true, nil
}

// transcriptBlockBoundary reads the seq and max TS of the oldest (or
// newest) block of the transcript; found is false for an empty transcript.
func (s *Store) transcriptBlockBoundary(ctx context.Context, tx *gorm.DB, transcriptID string, last bool) (seq, ts int64, found bool, err error) {
	chunk := query.Use(tx).TranscriptChunkRow
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
func (s *Store) transcriptBlockAtOrAfter(ctx context.Context, tx *gorm.DB, transcriptID string, seq int64) (blockSeq, blockTS int64, ok bool, err error) {
	chunk := query.Use(tx).TranscriptChunkRow
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
func (s *Store) transcriptBlockBefore(ctx context.Context, tx *gorm.DB, transcriptID string, seq int64) (blockSeq, blockTS int64, ok bool, err error) {
	chunk := query.Use(tx).TranscriptChunkRow
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
func (s *Store) transcriptBlockBySeq(ctx context.Context, tx *gorm.DB, transcriptID string, blockSeq int64) (TranscriptChunkRow, error) {
	chunk := query.Use(tx).TranscriptChunkRow
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
func (s *Store) transcriptChunkBeforeExists(ctx context.Context, tx *gorm.DB, transcriptID string, bound int64) (bool, error) {
	chunk := query.Use(tx).TranscriptChunkRow
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
func (s *Store) transcriptChunkAfterExists(ctx context.Context, tx *gorm.DB, transcriptID string, bound int64) (bool, error) {
	chunk := query.Use(tx).TranscriptChunkRow
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
func (s *Store) transcriptPageBounds(ctx context.Context, tx *gorm.DB, transcriptID string, page *TranscriptPage) error {
	if len(page.Chunks) == 0 {
		return nil
	}
	var err error
	page.HasBefore, err = s.transcriptChunkBeforeExists(ctx, tx, transcriptID, page.FirstSeq)
	if err != nil {
		return err
	}
	page.HasAfter, err = s.transcriptChunkAfterExists(ctx, tx, transcriptID, page.LastSeq)
	return err
}
