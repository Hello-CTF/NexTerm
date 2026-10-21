package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// transcriptBlockTS is one stored block's seq with its true maximum chunk
// timestamp.
type transcriptBlockTS struct {
	blockSeq int64
	maxTS    int64
}

// transcriptBlockMaxTimestamps computes the true maximum chunk timestamp
// per stored block for a batch of chunks, mirroring the encoder's block
// grouping (up to transcriptBlockMaxItems chunks or transcriptBlockMaxBytes
// raw bytes per block, in seq order).
func transcriptBlockMaxTimestamps(chunks []TranscriptChunkRow) []transcriptBlockTS {
	ordered := append([]TranscriptChunkRow(nil), chunks...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Seq < ordered[j].Seq })
	blocks := make([]transcriptBlockTS, 0, (len(ordered)+transcriptBlockMaxItems-1)/transcriptBlockMaxItems)
	for start := 0; start < len(ordered); {
		end := start + 1
		maxTS := ordered[start].TS
		rawBytes := len(ordered[start].Data) + len(ordered[start].TabID)
		for end < len(ordered) && end-start < transcriptBlockMaxItems && rawBytes+len(ordered[end].Data)+len(ordered[end].TabID) <= transcriptBlockMaxBytes {
			rawBytes += len(ordered[end].Data) + len(ordered[end].TabID)
			if ordered[end].TS > maxTS {
				maxTS = ordered[end].TS
			}
			end++
		}
		blocks = append(blocks, transcriptBlockTS{blockSeq: ordered[end-1].Seq, maxTS: maxTS})
		start = end
	}
	return blocks
}

// decodeTranscriptBlockVerified decodes a stored block and verifies its
// stored timestamp matches its last chunk's timestamp; a mismatch means the
// row's timestamp metadata is corrupt and the block must not be trusted for
// time-indexed reads.
func decodeTranscriptBlockVerified(stored TranscriptChunkRow) ([]TranscriptChunkRow, error) {
	chunks, err := decodeTranscriptBlock(stored)
	if err != nil {
		return nil, err
	}
	if chunks[len(chunks)-1].TS != stored.TS {
		return nil, transcriptFormatError(fmt.Errorf("transcript block timestamp does not match its last chunk"))
	}
	return chunks, nil
}

// transcriptTSIndexStateUpsert records the index state after an append: a
// transcript's first append indexes every stored block, so it is complete
// from the start; a transcript with prior blocks keeps its state (an
// incomplete legacy transcript stays incomplete until the backfill catches
// up), and an absent state row is created incomplete.
func (s *Store) transcriptTSIndexStateUpsert(ctx context.Context, tx *gorm.DB, transcriptID string, wasEmpty bool) error {
	state := &model.TranscriptTSIndex{
		TranscriptID:    transcriptID,
		VerifiedThrough: -1,
		Complete:        wasEmpty,
		UpdatedAt:       ids.NowMS(),
		OwnerID:         ipc.OwnerID(ctx),
	}
	if wasEmpty {
		return tx.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "transcript_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"complete", "updated_at", "owner_id", "verified_through"}),
		}).Create(state).Error
	}
	return tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "transcript_id"}},
		DoNothing: true,
	}).Create(state).Error
}

// transcriptTSIndexStateComplete marks the transcript's timestamp index
// complete after its content was fully (re)indexed; the verification
// watermark resets with the content generation and the rollback epoch
// increments, so a stale advance from before the replace is discarded
// instead of trusting rows no seek has checked.
func (s *Store) transcriptTSIndexStateComplete(ctx context.Context, tx *gorm.DB, transcriptID string) error {
	state := &model.TranscriptTSIndex{
		TranscriptID:    transcriptID,
		VerifiedThrough: -1,
		Revision:        0,
		Complete:        true,
		UpdatedAt:       ids.NowMS(),
		OwnerID:         ipc.OwnerID(ctx),
	}
	return tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "transcript_id"}},
		DoUpdates: clause.Set{
			{Column: clause.Column{Name: "complete"}, Value: true},
			{Column: clause.Column{Name: "updated_at"}, Value: ids.NowMS()},
			{Column: clause.Column{Name: "owner_id"}, Value: ipc.OwnerID(ctx)},
			{Column: clause.Column{Name: "verified_through"}, Value: -1},
			{Column: clause.Column{Name: "revision"}, Value: gorm.Expr("transcript_ts_index.revision + 1")},
		},
	}).Create(state).Error
}

// transcriptTSIndexState reads the transcript's index state in one
// snapshot-consistent round trip: the completeness flag, the verification
// watermark, and the rollback epoch the advance must compare against.
// found is false when no state row exists.
func (s *Store) transcriptTSIndexState(ctx context.Context, tx *gorm.DB, transcriptID string) (complete bool, verified, revision int64, found bool, err error) {
	query := tx.WithContext(ctx).Model(&model.TranscriptTSIndex{}).
		Select("complete", "verified_through", "revision").
		Where("transcript_id = ?", transcriptID)
	if ownerID := ipc.OwnerID(ctx); ownerID != "" {
		query = query.Where("owner_id = ?", ownerID)
	}
	var state struct {
		Complete        bool
		VerifiedThrough int64
		Revision        int64
	}
	result := query.Scan(&state)
	if result.Error != nil {
		return false, 0, 0, false, dbError(result.Error)
	}
	if result.RowsAffected == 0 {
		return false, 0, 0, false, nil
	}
	return state.Complete, state.VerifiedThrough, state.Revision, true, nil
}

// transcriptTSIndexVerifiedThrough reads the seek-side verification
// watermark: blocks with seq <= watermark have had their decoded content
// verified against their index rows for the current content.
func (s *Store) transcriptTSIndexVerifiedThrough(ctx context.Context, tx *gorm.DB, transcriptID string) (int64, error) {
	_, verified, _, _, err := s.transcriptTSIndexState(ctx, tx, transcriptID)
	return verified, err
}

// transcriptTSIndexAdvanceVerifiedGen moves the verification watermark
// forward monotonically, guarded by the content generation and the
// rollback epoch the seek read in its snapshot: the advance runs in its
// own READ COMMITTED transaction so it sees the latest committed state,
// and it applies only while the transcript's current generation still
// matches and the epoch is unchanged — a replace that landed after the
// snapshot resets the watermark, and an early-seq append rolls it back
// (bumping the epoch); either way the stale advance is discarded (the
// caller reports the explicit retry state) instead of re-covering blocks
// the snapshot never verified. Another seek's legitimate forward advance
// leaves the epoch untouched and is honored, and an advance the watermark
// already covers is a no-op. A serialization or lock failure on the way
// is likewise an explicit retry, never an error page.
func (s *Store) transcriptTSIndexAdvanceVerifiedGen(ctx context.Context, transcriptID string, verified, gen, revision int64) (bool, error) {
	var currentGen, currentRevision int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Model(&model.TranscriptRow{}).
			Select("content_gen").
			Where("id = ?", transcriptID).
			Scan(&currentGen).Error; err != nil {
			return dbError(err)
		}
		if currentGen != gen {
			return nil
		}
		if err := tx.WithContext(ctx).Model(&model.TranscriptTSIndex{}).
			Select("revision").
			Where("transcript_id = ?", transcriptID).
			Scan(&currentRevision).Error; err != nil {
			return dbError(err)
		}
		if currentRevision != revision {
			return nil
		}
		result := tx.WithContext(ctx).Model(&model.TranscriptTSIndex{}).
			Where("transcript_id = ? AND (SELECT content_gen FROM transcript WHERE id = ?) = ? AND revision = ?", transcriptID, transcriptID, gen, revision).
			Updates(map[string]any{
				"verified_through": gorm.Expr("CASE WHEN verified_through < ? THEN ? ELSE verified_through END", verified, verified),
			})
		if result.Error != nil {
			return dbError(result.Error)
		}
		if result.RowsAffected == 0 {
			// MySQL and MariaDB count changed rows: the advance may have
			// been a no-op because the watermark already covered it. Re-read
			// the generation and the epoch to tell that apart from a
			// concurrent replace or rollback.
			if err := tx.WithContext(ctx).Model(&model.TranscriptRow{}).
				Select("content_gen").
				Where("id = ?", transcriptID).
				Scan(&currentGen).Error; err != nil {
				return dbError(err)
			}
			if currentGen != gen {
				return nil
			}
			if err := tx.WithContext(ctx).Model(&model.TranscriptTSIndex{}).
				Select("revision").
				Where("transcript_id = ?", transcriptID).
				Scan(&currentRevision).Error; err != nil {
				return dbError(err)
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		if isBusyErr(err) {
			return false, nil
		}
		return false, err
	}
	return currentGen == gen && currentRevision == revision, nil
}

// transcriptContentGen reads the transcript's content generation, the
// stamp every timestamp-index row must carry to be trusted for the current
// content.
func (s *Store) transcriptContentGen(ctx context.Context, tx *gorm.DB, transcriptID string) (int64, error) {
	var gen int64
	result := tx.WithContext(ctx).Model(&model.TranscriptRow{}).
		Select("content_gen").
		Where("id = ?", transcriptID).
		Scan(&gen)
	if result.Error != nil {
		return 0, dbError(result.Error)
	}
	if result.RowsAffected == 0 {
		return 0, notFound("会话记录")
	}
	return gen, nil
}

// transcriptTSIndexComplete reports whether every stored block of the
// transcript has a timestamp-index row.
func (s *Store) transcriptTSIndexComplete(ctx context.Context, tx *gorm.DB, transcriptID string) (bool, error) {
	query := tx.WithContext(ctx).Model(&model.TranscriptTSIndex{}).
		Select("complete").
		Where("transcript_id = ?", transcriptID)
	if ownerID := ipc.OwnerID(ctx); ownerID != "" {
		query = query.Where("owner_id = ?", ownerID)
	}
	var complete bool
	result := query.Scan(&complete)
	if result.Error != nil {
		return false, dbError(result.Error)
	}
	return result.RowsAffected > 0 && complete, nil
}

// transcriptIndexedBlockAtLeast returns the seq of the first indexed block
// whose maximum chunk timestamp reaches atMS; a block whose maximum is
// below the target provably holds no qualifying chunk under any timestamp
// order, so this eliminates every earlier block from the seek. Only rows
// stamped with the transcript's current content generation are trusted: a
// row written for an older generation is stale (a replace rebuilt the
// content) and must never eliminate current content.
func (s *Store) transcriptIndexedBlockAtLeast(ctx context.Context, tx *gorm.DB, transcriptID string, atMS, gen int64) (int64, bool, error) {
	query := tx.WithContext(ctx).Model(&model.TranscriptChunkTS{}).
		Select("block_seq").
		Where("transcript_id = ? AND max_ts >= ? AND gen = ?", transcriptID, atMS, gen).
		Order("block_seq").
		Limit(1)
	if ownerID := ipc.OwnerID(ctx); ownerID != "" {
		query = query.Where("owner_id = ?", ownerID)
	}
	var blockSeq int64
	result := query.Scan(&blockSeq)
	if result.Error != nil {
		return 0, false, dbError(result.Error)
	}
	if result.RowsAffected == 0 {
		return 0, false, nil
	}
	return blockSeq, true, nil
}

// transcriptTSIndexUpsertCurrent writes one block's timestamp-index row
// stamped with the given content generation. The guarded repair only
// overwrites an existing row when the incoming generation is newer: an
// older backfill's late row can never overwrite the current generation's
// row, and a stale row left by a replace is repaired by the next
// current-generation write instead of blocking it forever.
func (s *Store) transcriptTSIndexUpsertCurrent(ctx context.Context, tx *gorm.DB, transcriptID string, blockSeq, maxTS, gen int64, ownerID string) error {
	row := &model.TranscriptChunkTS{TranscriptID: transcriptID, BlockSeq: blockSeq, MaxTS: maxTS, Gen: gen, OwnerID: ownerID}
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "transcript_id"}, {Name: "block_seq"}},
		DoNothing: true,
	}).Create(row).Error; err != nil {
		return dbError(err)
	}
	return tx.WithContext(ctx).Model(&model.TranscriptChunkTS{}).
		Where("transcript_id = ? AND block_seq = ? AND gen < ?", transcriptID, blockSeq, gen).
		Updates(map[string]any{"max_ts": maxTS, "gen": gen, "owner_id": ownerID}).Error
}

// transcriptTSIndexBackfillBatch is the block population indexed per
// backfill batch; it is a variable so tests can exercise multi-batch
// scheduling without a multi-million-chunk fixture.
var transcriptTSIndexBackfillBatch = 64

// transcriptTSIndexBackfillPauses bound the backfill loop's pacing.
const (
	transcriptTSIndexBackfillBatchPause  = 20 * time.Millisecond
	transcriptTSIndexBackfillErrorPause  = 200 * time.Millisecond
	transcriptTSIndexBackfillMaxFailures = 8
)

// transcriptTSIndexBackfillState tracks a store's backfill loop, its
// per-block failures, and its resumable cursors. Failed blocks are corrupt
// data that can never be indexed; they are skipped with a log so they
// cannot block other transcripts' progress, and the transcript they belong
// to stays incomplete so its seeks keep failing explicitly on the corrupt
// block. The cursors mark the last-processed positions so a batch always
// moves forward: corrupt blocks can never fill the selection window and
// stall the loop. The priority cursor serves the kicking owner's
// transcripts first; the global cursor then serves every owner in (owner,
// transcript, seq) order — fair across owners and explainable per batch.
type transcriptTSIndexBackfillState struct {
	running atomic.Bool
	failed  sync.Map // transcriptID/blockSeq -> struct{}{}
	cursor  struct {
		ownerID      string
		transcriptID string
		seq          int64
	}
	priorityOwner  string
	priorityCursor struct {
		transcriptID string
		seq          int64
	}
}

// transcriptTSIndexBackfills tracks the running backfill loop per store so
// a later seek can restart it after an early exit; the entry is removed
// when the loop finishes.
var transcriptTSIndexBackfills sync.Map // *Store -> *transcriptTSIndexBackfillState

// kickTranscriptTSIndexBackfill starts the backfill loop when it is not
// already running, prioritizing the given owner's transcripts. The closed
// flag is checked under the lifecycle lock so a kick racing Close cannot
// start work that Close would not join.
func (s *Store) kickTranscriptTSIndexBackfill(ownerID string) {
	s.backfillMu.Lock()
	defer s.backfillMu.Unlock()
	if s.backfillClosed {
		return
	}
	state, _ := transcriptTSIndexBackfills.LoadOrStore(s, &transcriptTSIndexBackfillState{})
	if state.(*transcriptTSIndexBackfillState).running.CompareAndSwap(false, true) {
		s.backfillWG.Add(1)
		go s.backfillTranscriptTSIndexLoop(state.(*transcriptTSIndexBackfillState), ownerID)
	}
}

// backfillTranscriptTSIndexLoop indexes unindexed blocks in bounded batches
// until none remain, the store closes, or failures persist. It runs in the
// background so a legacy transcript's distant seek only pays the index
// query while the index is complete and only scans a bounded pass while
// the backfill catches up — never an unbounded scan per seek.
func (s *Store) backfillTranscriptTSIndexLoop(state *transcriptTSIndexBackfillState, ownerID string) {
	// The defers run LIFO: first the map registration is deleted (a new
	// kick creates a fresh registration and flag instead of reusing this
	// loop's), then the flag clears, and Done runs last so Close's Wait
	// unblocks only after the loop is fully cleaned up — no duplicate loop
	// can escape the join.
	defer s.backfillWG.Done()
	defer state.running.Store(false)
	defer transcriptTSIndexBackfills.Delete(s)
	if ownerID != "" {
		// The scheduling decision is observable, not just documented: the
		// kicked owner's transcripts are served first so the seek that
		// started the loop gets its answer without waiting behind every
		// other owner's backlog.
		state.priorityOwner = ownerID
		s.logger.Info("transcript timestamp index backfill prioritizing the kicked owner", "owner", ownerID)
	}
	failures := 0
	for {
		progressed, err := s.backfillTranscriptTSIndexBatch(state)
		if err != nil {
			if isDBClosedError(err) || s.backfillCtx.Err() != nil {
				return
			}
			failures++
			if failures >= transcriptTSIndexBackfillMaxFailures {
				return
			}
			if !s.backfillSleep(transcriptTSIndexBackfillErrorPause) {
				return
			}
			continue
		}
		failures = 0
		if !progressed {
			return
		}
		if !s.backfillSleep(transcriptTSIndexBackfillBatchPause) {
			return
		}
	}
}

// backfillSleep waits for the pause and reports whether the store is still
// open; a closed store ends the loop instead of parking it.
func (s *Store) backfillSleep(pause time.Duration) bool {
	timer := time.NewTimer(pause)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-s.backfillCtx.Done():
		return false
	}
}

// isDBClosedError reports whether the error means the store's database
// handle is closed, so the backfill loop can stop instead of retrying.
func isDBClosedError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "database is closed")
}

// transcriptBackfillBlockRef identifies one unindexed block to backfill.
type transcriptBackfillBlockRef struct {
	OwnerID      string `gorm:"column:owner_id"`
	TranscriptID string `gorm:"column:transcript_id"`
	Seq          int64  `gorm:"column:seq"`
}

// backfillTranscriptTSIndexBatch indexes up to
// transcriptTSIndexBackfillBatch unindexed blocks across transcripts and
// marks fully covered transcripts complete. The kicking owner's transcripts
// are served first through the resumable priority cursor; once the owner is
// fully indexed the batch falls through to the global (owner, transcript,
// seq) cursor, which serves every owner in turn — fair across owners, and
// every batch logs the owner it served so the scheduling is explainable.
// It is resumable by construction: already-indexed blocks are skipped, so
// an interruption resumes where it left off.
func (s *Store) backfillTranscriptTSIndexBatch(state *transcriptTSIndexBackfillState) (bool, error) {
	if state.priorityOwner != "" {
		refs, err := s.unindexedTranscriptBlockRefs(state.priorityOwner, "", state.priorityCursor.transcriptID, state.priorityCursor.seq)
		if err != nil {
			return false, err
		}
		if len(refs) > 0 {
			return s.indexTranscriptBlockRefs(state, refs, func(last transcriptBackfillBlockRef) {
				state.priorityCursor.transcriptID = last.TranscriptID
				state.priorityCursor.seq = last.Seq
			})
		}
		// The priority owner is fully indexed: the global order takes over
		// in the same batch so the loop keeps making progress.
		state.priorityOwner = ""
	}
	refs, err := s.unindexedTranscriptBlockRefs("", state.cursor.ownerID, state.cursor.transcriptID, state.cursor.seq)
	if err != nil {
		return false, err
	}
	if len(refs) == 0 {
		return false, nil
	}
	return s.indexTranscriptBlockRefs(state, refs, func(last transcriptBackfillBlockRef) {
		state.cursor.ownerID = last.OwnerID
		state.cursor.transcriptID = last.TranscriptID
		state.cursor.seq = last.Seq
	})
}

// unindexedTranscriptBlockRefs selects up to transcriptTSIndexBackfillBatch
// unindexed block refs: within one owner when filterOwner is set (priority
// pass), across owners in (owner, transcript, seq) order otherwise (global
// pass). The cursor makes the pass resumable.
func (s *Store) unindexedTranscriptBlockRefs(filterOwner, cursorOwner, cursorTranscriptID string, cursorSeq int64) ([]transcriptBackfillBlockRef, error) {
	query := s.db.WithContext(s.backfillCtx).Model(&model.TranscriptChunkRow{}).
		Select("transcript_chunk.owner_id AS owner_id, transcript_chunk.transcript_id AS transcript_id, transcript_chunk.seq AS seq").
		Where(`NOT EXISTS (
			SELECT 1 FROM transcript_chunk_ts idx
			JOIN transcript ON transcript.id = idx.transcript_id
			WHERE idx.transcript_id = transcript_chunk.transcript_id
			  AND idx.block_seq = transcript_chunk.seq
			  AND idx.gen = transcript.content_gen
		)`).
		Order("transcript_chunk.owner_id, transcript_chunk.transcript_id, transcript_chunk.seq").
		Limit(transcriptTSIndexBackfillBatch)
	if filterOwner != "" {
		query = query.
			Where("transcript_chunk.owner_id = ?", filterOwner).
			Where("(transcript_chunk.transcript_id, transcript_chunk.seq) > (?, ?)", cursorTranscriptID, cursorSeq)
	} else {
		query = query.
			Where("(transcript_chunk.owner_id, transcript_chunk.transcript_id, transcript_chunk.seq) > (?, ?, ?)", cursorOwner, cursorTranscriptID, cursorSeq)
	}
	var refs []transcriptBackfillBlockRef
	if err := query.Scan(&refs).Error; err != nil {
		return nil, dbError(err)
	}
	return refs, nil
}

// indexTranscriptBlockRefs indexes the selected blocks — stamping each row
// with the transcript's current content generation so a row written for a
// replaced content generation is recognizable as stale — and marks fully
// covered transcripts complete. The cursor advances to the last ref:
// corrupt blocks are recorded and skipped, but the window always moves
// forward so they can never fill it and stall the loop.
func (s *Store) indexTranscriptBlockRefs(state *transcriptTSIndexBackfillState, refs []transcriptBackfillBlockRef, advance func(transcriptBackfillBlockRef)) (bool, error) {
	gens := make(map[string]int64, len(refs))
	transcripts := make(map[string]struct{}, len(refs))
	indexed := 0
	for _, ref := range refs {
		key := fmt.Sprintf("%s/%d", ref.TranscriptID, ref.Seq)
		if _, failed := state.failed.Load(key); failed {
			continue
		}
		block, err := s.transcriptBlockBySeq(s.backfillCtx, s.db, ref.TranscriptID, ref.Seq)
		if err != nil {
			if isNotFoundError(err) {
				// The block was replaced or deleted after selection: the
				// replacement rebuilds the index itself, so the ref is
				// simply stale — skip it instead of failing the batch.
				continue
			}
			return false, err
		}
		chunks, err := decodeTranscriptBlockVerified(block)
		if err != nil {
			// A persistently corrupt block must not stall the loop: record
			// it and move on; its transcript stays incomplete so its seeks
			// keep failing explicitly on the corrupt block.
			s.logger.Warn("transcript timestamp index backfill skipped a corrupt block", "transcript", ref.TranscriptID, "block", ref.Seq, "error", err)
			state.failed.Store(key, struct{}{})
			continue
		}
		gen, ok := gens[ref.TranscriptID]
		if !ok {
			gen, err = s.transcriptContentGen(s.backfillCtx, s.db, ref.TranscriptID)
			if err != nil {
				if isNotFoundError(err) {
					continue
				}
				return false, err
			}
			gens[ref.TranscriptID] = gen
		}
		maxTS := chunks[0].TS
		for _, chunk := range chunks[1:] {
			if chunk.TS > maxTS {
				maxTS = chunk.TS
			}
		}
		if err := s.transcriptTSIndexUpsertCurrent(s.backfillCtx, s.db, ref.TranscriptID, ref.Seq, maxTS, gen, block.OwnerID); err != nil {
			return false, err
		}
		indexed++
		transcripts[ref.TranscriptID] = struct{}{}
	}
	advance(refs[len(refs)-1])
	s.logger.Info("transcript timestamp index backfill batch", "owners", strings.Join(transcriptBackfillRefOwners(refs), ","), "selected", len(refs), "indexed", indexed)
	for transcriptID := range transcripts {
		complete, err := s.transcriptTSIndexFullyIndexed(s.backfillCtx, transcriptID)
		if err != nil {
			return false, err
		}
		if complete {
			if err := s.transcriptTSIndexMarkComplete(s.backfillCtx, transcriptID); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

// isNotFoundError reports whether the error is the store's not-found
// verdict, so the backfill can skip a block or transcript that was
// replaced or deleted after selection instead of failing the batch.
func isNotFoundError(err error) bool {
	var appErr *ipc.Error
	return errors.As(err, &appErr) && appErr.Code == ipc.CodeNotFound
}

// transcriptBackfillRefOwners lists the distinct owners of a batch's refs
// in first-seen order, so each batch log names exactly the owners the
// scheduling decision served.
func transcriptBackfillRefOwners(refs []transcriptBackfillBlockRef) []string {
	owners := make([]string, 0, 1)
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if _, ok := seen[ref.OwnerID]; ok {
			continue
		}
		seen[ref.OwnerID] = struct{}{}
		owners = append(owners, ref.OwnerID)
	}
	return owners
}

// transcriptTSIndexFullyIndexed reports whether the transcript has no
// unindexed blocks left: every block carries an index row stamped with the
// transcript's current content generation — a stale row from an older
// generation does not count.
func (s *Store) transcriptTSIndexFullyIndexed(ctx context.Context, transcriptID string) (bool, error) {
	var remaining int64
	err := s.db.WithContext(ctx).Model(&model.TranscriptChunkRow{}).
		Where(`transcript_chunk.transcript_id = ? AND NOT EXISTS (
			SELECT 1 FROM transcript_chunk_ts idx
			JOIN transcript ON transcript.id = idx.transcript_id
			WHERE idx.transcript_id = transcript_chunk.transcript_id
			  AND idx.block_seq = transcript_chunk.seq
			  AND idx.gen = transcript.content_gen
		)`, transcriptID).
		Count(&remaining).Error
	if err != nil {
		return false, dbError(err)
	}
	return remaining == 0, nil
}

// transcriptTSIndexMarkComplete marks the transcript's timestamp index
// complete under the transcript's owner.
func (s *Store) transcriptTSIndexMarkComplete(ctx context.Context, transcriptID string) error {
	var ownerID string
	if err := s.db.WithContext(ctx).Model(&model.TranscriptRow{}).
		Select("owner_id").
		Where("id = ?", transcriptID).
		Limit(1).
		Scan(&ownerID).Error; err != nil {
		return dbError(err)
	}
	state := &model.TranscriptTSIndex{
		TranscriptID:    transcriptID,
		VerifiedThrough: -1,
		Complete:        true,
		UpdatedAt:       ids.NowMS(),
		OwnerID:         ownerID,
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "transcript_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"complete", "updated_at", "owner_id"}),
	}).Create(state).Error
}
