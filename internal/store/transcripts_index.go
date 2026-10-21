package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gen/field"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The timestamp-index models are registered here rather than in
// internal/store/model/models.go so the store package owns its index schema
// without touching the shared model registry; the registration is consumed
// by the same startup AutoMigrate.
func init() {
	model.AllModels = append(model.AllModels, model.TranscriptChunkTS{}, model.TranscriptTSIndex{})
}

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
		TranscriptID: transcriptID,
		Complete:     wasEmpty,
		UpdatedAt:    ids.NowMS(),
		OwnerID:      ipc.OwnerID(ctx),
	}
	if wasEmpty {
		return tx.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "transcript_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"complete", "updated_at", "owner_id"}),
		}).Create(state).Error
	}
	return tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "transcript_id"}},
		DoNothing: true,
	}).Create(state).Error
}

// transcriptTSIndexStateComplete marks the transcript's timestamp index
// complete after its content was fully (re)indexed.
func (s *Store) transcriptTSIndexStateComplete(ctx context.Context, tx *gorm.DB, transcriptID string) error {
	state := &model.TranscriptTSIndex{
		TranscriptID: transcriptID,
		Complete:     true,
		UpdatedAt:    ids.NowMS(),
		OwnerID:      ipc.OwnerID(ctx),
	}
	return tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "transcript_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"complete", "updated_at", "owner_id"}),
	}).Create(state).Error
}

// transcriptTSIndexComplete reports whether every stored block of the
// transcript has a timestamp-index row.
func (s *Store) transcriptTSIndexComplete(ctx context.Context, transcriptID string) (bool, error) {
	query := s.db.WithContext(ctx).Model(&model.TranscriptTSIndex{}).
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
// order, so this eliminates every earlier block from the seek.
func (s *Store) transcriptIndexedBlockAtLeast(ctx context.Context, transcriptID string, atMS int64) (int64, bool, error) {
	query := s.db.WithContext(ctx).Model(&model.TranscriptChunkTS{}).
		Select("block_seq").
		Where("transcript_id = ? AND max_ts >= ?", transcriptID, atMS).
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

// transcriptTSIndexBackfillBatch is the block population indexed per
// backfill batch.
const transcriptTSIndexBackfillBatch = 64

// transcriptTSIndexBackfillPauses bound the backfill loop's pacing.
const (
	transcriptTSIndexBackfillBatchPause  = 20 * time.Millisecond
	transcriptTSIndexBackfillErrorPause  = 200 * time.Millisecond
	transcriptTSIndexBackfillMaxFailures = 8
)

// transcriptTSIndexBackfills tracks the running backfill loop per store so
// a later seek can restart it after an early exit; the entry is removed
// when the loop finishes.
var transcriptTSIndexBackfills sync.Map // *Store -> *atomic.Bool

// kickTranscriptTSIndexBackfill starts the backfill loop when it is not
// already running.
func (s *Store) kickTranscriptTSIndexBackfill() {
	flag, _ := transcriptTSIndexBackfills.LoadOrStore(s, &atomic.Bool{})
	if flag.(*atomic.Bool).CompareAndSwap(false, true) {
		go s.backfillTranscriptTSIndexLoop(flag.(*atomic.Bool))
	}
}

// backfillTranscriptTSIndexLoop indexes unindexed blocks in bounded batches
// until none remain, the store closes, or failures persist. It runs in the
// background so a legacy transcript's distant seek only pays the index
// query while the index is complete and only scans the unindexed remainder
// until the backfill catches up — never an unbounded scan per seek.
func (s *Store) backfillTranscriptTSIndexLoop(running *atomic.Bool) {
	defer transcriptTSIndexBackfills.Delete(s)
	defer running.Store(false)
	failures := 0
	for {
		progressed, err := s.backfillTranscriptTSIndexBatch(context.Background())
		if err != nil {
			if isDBClosedError(err) {
				return
			}
			failures++
			if failures >= transcriptTSIndexBackfillMaxFailures {
				return
			}
			time.Sleep(transcriptTSIndexBackfillErrorPause)
			continue
		}
		failures = 0
		if !progressed {
			return
		}
		time.Sleep(transcriptTSIndexBackfillBatchPause)
	}
}

// isDBClosedError reports whether the error means the store's database
// handle is closed, so the backfill loop can stop instead of retrying.
func isDBClosedError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "database is closed")
}

// backfillTranscriptTSIndexBatch indexes up to
// transcriptTSIndexBackfillBatch unindexed blocks across transcripts in
// (transcript, seq) order and marks fully covered transcripts complete. It
// is resumable by construction: already-indexed blocks are skipped, so an
// interruption resumes where it left off.
func (s *Store) backfillTranscriptTSIndexBatch(ctx context.Context) (bool, error) {
	type blockRef struct {
		TranscriptID string `gorm:"column:transcript_id"`
		Seq          int64  `gorm:"column:seq"`
	}
	var refs []blockRef
	err := s.db.WithContext(ctx).Model(&model.TranscriptChunkRow{}).
		Select("transcript_chunk.transcript_id AS transcript_id, transcript_chunk.seq AS seq").
		Where("NOT EXISTS (SELECT 1 FROM transcript_chunk_ts idx WHERE idx.transcript_id = transcript_chunk.transcript_id AND idx.block_seq = transcript_chunk.seq)").
		Order("transcript_chunk.transcript_id, transcript_chunk.seq").
		Limit(transcriptTSIndexBackfillBatch).
		Scan(&refs).Error
	if err != nil {
		return false, dbError(err)
	}
	if len(refs) == 0 {
		return false, nil
	}
	transcripts := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		block, err := s.transcriptBlockBySeq(context.WithoutCancel(ctx), ref.TranscriptID, ref.Seq)
		if err != nil {
			return false, err
		}
		chunks, err := decodeTranscriptBlockVerified(block)
		if err != nil {
			return false, err
		}
		maxTS := chunks[0].TS
		for _, chunk := range chunks[1:] {
			if chunk.TS > maxTS {
				maxTS = chunk.TS
			}
		}
		row := &model.TranscriptChunkTS{TranscriptID: ref.TranscriptID, BlockSeq: ref.Seq, MaxTS: maxTS, OwnerID: block.OwnerID}
		if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "transcript_id"}, {Name: "block_seq"}},
			DoNothing: true,
		}).Create(row).Error; err != nil {
			return false, dbError(err)
		}
		transcripts[ref.TranscriptID] = struct{}{}
	}
	for transcriptID := range transcripts {
		complete, err := s.transcriptTSIndexFullyIndexed(ctx, transcriptID)
		if err != nil {
			return false, err
		}
		if complete {
			if err := s.transcriptTSIndexMarkComplete(ctx, transcriptID); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

// transcriptTSIndexFullyIndexed reports whether the transcript has no
// unindexed blocks left.
func (s *Store) transcriptTSIndexFullyIndexed(ctx context.Context, transcriptID string) (bool, error) {
	var remaining int64
	err := s.db.WithContext(ctx).Model(&model.TranscriptChunkRow{}).
		Where("transcript_chunk.transcript_id = ? AND NOT EXISTS (SELECT 1 FROM transcript_chunk_ts idx WHERE idx.transcript_id = transcript_chunk.transcript_id AND idx.block_seq = transcript_chunk.seq)", transcriptID).
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
		TranscriptID: transcriptID,
		Complete:     true,
		UpdatedAt:    ids.NowMS(),
		OwnerID:      ownerID,
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "transcript_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"complete", "updated_at", "owner_id"}),
	}).Create(state).Error
}

// scanUnindexedChunksAtLeast returns the first chunk with TS >= atMS among
// blocks that have no timestamp-index row yet, in seq order — the sound
// remainder scan while the backfill is catching up.
func (s *Store) scanUnindexedChunksAtLeast(ctx context.Context, transcriptID string, atMS int64) (int64, bool, error) {
	chunk := query.Use(s.db).TranscriptChunkRow
	blocks := chunk.WithContext(ctx).
		Where(chunk.TranscriptID.Eq(transcriptID)).
		Where(ownerConditions(ctx, chunk.TableName())...).
		Where(field.NewUnsafeFieldRaw("NOT EXISTS (SELECT 1 FROM transcript_chunk_ts idx WHERE idx.transcript_id = transcript_chunk.transcript_id AND idx.block_seq = transcript_chunk.seq)"))
	rows, err := blocks.Order(chunk.Seq).Rows()
	if err != nil {
		return 0, false, dbError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var stored TranscriptChunkRow
		if err := s.db.ScanRows(rows, &stored); err != nil {
			return 0, false, dbError(err)
		}
		chunks, err := decodeTranscriptBlockVerified(stored)
		if err != nil {
			return 0, false, err
		}
		for _, row := range chunks {
			if row.TS >= atMS {
				return row.Seq, true, nil
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, false, dbError(err)
	}
	return 0, false, nil
}
