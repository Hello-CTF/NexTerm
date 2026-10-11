package store

import (
	"context"
	"fmt"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type TranscriptRow = model.TranscriptRow
type TranscriptChunkRow = model.TranscriptChunkRow
type DurableTranscriptOffset = model.DurableTranscriptOffset

const (
	TranscriptChunkKindOutput = 0
	TranscriptChunkKindInput  = 1
	TranscriptChunkKindResize = 2
)

type TranscriptMatch struct {
	Seq     int64
	TS      int64
	Preview []byte
}

type TranscriptHostRow struct {
	AssetID       string
	AssetName     string
	AssetKind     string
	AssetDeleted  bool
	Transcripts   int64
	LastStartedAt int64
}

type TranscriptRetentionPolicy struct {
	MaxAge   time.Duration
	MaxCount int64
}

type TranscriptRetentionResult struct {
	Deleted int64
}

func (p TranscriptRetentionPolicy) validate() error {
	if p.MaxAge < 0 {
		return badParam(fmt.Errorf("transcript max age must not be negative"))
	}
	if p.MaxCount < 0 {
		return badParam(fmt.Errorf("transcript max count must not be negative"))
	}
	return nil
}

func (s *Store) TranscriptStart(ctx context.Context, row TranscriptRow) error {
	if row.ID == "" {
		row.ID = ids.New()
	}
	row.EndedAt = nil
	row.Bytes = 0
	row.Chunks = 0
	row.Truncated = false
	row.SyncOptIn = false
	row.ContentOmitted = false
	row.OwnerID = ipc.OwnerID(ctx)
	if err := query.Use(s.db).TranscriptRow.WithContext(ctx).Create(&row); err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) TranscriptAppendChunks(ctx context.Context, transcriptID string, chunks []TranscriptChunkRow, durableBytes ...map[string]int64) error {
	if len(chunks) == 0 {
		return nil
	}
	if _, err := s.TranscriptGet(ctx, transcriptID); err != nil {
		return err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := query.Use(tx)
		var totalBytes, totalChunks int64
		for _, chunk := range chunks {
			chunk.TranscriptID = transcriptID
			chunk.OwnerID = ipc.OwnerID(ctx)
			if err := queries.TranscriptChunkRow.WithContext(ctx).Create(&chunk); err != nil {
				return err
			}
			totalBytes += int64(len(chunk.Data))
			totalChunks++
		}
		transcript := queries.TranscriptRow
		if _, err := transcript.WithContext(ctx).
			Where(transcript.ID.Eq(transcriptID)).
			Where(ownerConditions(ctx, transcript.TableName())...).
			UpdateColumns(map[string]any{
				"bytes":  gorm.Expr("bytes + ?", totalBytes),
				"chunks": gorm.Expr("chunks + ?", totalChunks),
			}); err != nil {
			return err
		}
		if len(durableBytes) > 0 {
			now := ids.NowMS()
			for durableID, count := range durableBytes[0] {
				if count <= 0 {
					continue
				}
				if err := tx.Clauses(clause.OnConflict{
					Columns: []clause.Column{{Name: "durable_id"}},
					DoUpdates: clause.Assignments(map[string]any{
						s.dialect.offsetColumn(): gorm.Expr("durable_transcript_offset."+s.dialect.offsetColumn()+" + ?", count),
						"updated_at":             now,
					}),
					Where: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: s.dialect.conflictTargetColumn("durable_transcript_offset", "owner_id"), Value: ipc.OwnerID(ctx)}}},
				}).Create(&DurableTranscriptOffset{DurableID: durableID, Offset: count, UpdatedAt: now, OwnerID: ipc.OwnerID(ctx)}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) TranscriptEnd(ctx context.Context, transcriptID string, endedAt int64, truncated bool) error {
	transcript := query.Use(s.db).TranscriptRow
	_, err := transcript.WithContext(ctx).
		Where(transcript.ID.Eq(transcriptID), transcript.EndedAt.IsNull()).
		Where(ownerConditions(ctx, transcript.TableName())...).
		UpdateColumns(map[string]any{
			"ended_at":  endedAt,
			"truncated": gorm.Expr(s.dialect.ScalarMax()+"(truncated, ?)", truncated),
		})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) TranscriptGet(ctx context.Context, id string) (TranscriptRow, error) {
	transcript := query.Use(s.db).TranscriptRow
	row, err := transcript.WithContext(ctx).
		Where(transcript.ID.Eq(id)).
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

func (s *Store) TranscriptListByAsset(ctx context.Context, assetID string, limit int64) ([]TranscriptRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	transcript := query.Use(s.db).TranscriptRow
	result := []TranscriptRow{}
	err := transcript.WithContext(ctx).
		Where(transcript.AssetID.Eq(assetID)).
		Where(ownerConditions(ctx, transcript.TableName())...).
		Order(transcript.StartedAt.Desc(), transcript.ID.Desc()).
		Limit(int(limit)).
		Scan(&result)
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

func (s *Store) TranscriptHosts(ctx context.Context) ([]TranscriptHostRow, error) {
	transcript := query.Use(s.db).TranscriptRow.As("t")
	asset := query.Use(s.db).AssetRow.As("a")
	type hostRow struct {
		AssetID       string
		AssetName     string
		AssetKind     string
		AssetDeleted  int
		Transcripts   int64
		LastStartedAt int64
	}
	rows := []hostRow{}
	err := transcript.WithContext(ctx).
		LeftJoin(asset, transcript.AssetID.EqCol(asset.ID)).
		Where(ownerConditions(ctx, "t")...).
		UnderlyingDB().
		Select(`t.asset_id, MAX(t.asset_name) AS asset_name, MAX(t.asset_kind) AS asset_kind, COUNT(*) AS transcripts, MAX(t.started_at) AS last_started_at, CASE WHEN a.id IS NULL OR a.deleted_at IS NOT NULL THEN 1 ELSE 0 END AS asset_deleted`).
		Group("t.asset_id").
		Order("MAX(t.started_at) DESC").
		Limit(500).
		Scan(&rows).Error
	if err != nil {
		return nil, dbError(err)
	}
	result := []TranscriptHostRow{}
	for _, row := range rows {
		result = append(result, TranscriptHostRow{
			AssetID: row.AssetID, AssetName: row.AssetName, AssetKind: row.AssetKind,
			AssetDeleted: row.AssetDeleted != 0, Transcripts: row.Transcripts, LastStartedAt: row.LastStartedAt,
		})
	}
	return result, nil
}

func (s *Store) TranscriptChunks(ctx context.Context, transcriptID string, afterSeq int64, maxBytes int) ([]TranscriptChunkRow, error) {
	if maxBytes <= 0 || maxBytes > 8<<20 {
		maxBytes = 1 << 20
	}
	chunk := query.Use(s.db).TranscriptChunkRow
	rows := []TranscriptChunkRow{}
	err := chunk.WithContext(ctx).
		Where(chunk.TranscriptID.Eq(transcriptID), chunk.Seq.Gte(afterSeq)).
		Where(ownerConditions(ctx, chunk.TableName())...).
		Order(chunk.Seq).
		Limit(4096).
		Scan(&rows)
	if err != nil {
		return nil, dbError(err)
	}
	result := []TranscriptChunkRow{}
	budget := maxBytes
	for _, row := range rows {
		result = append(result, row)
		budget -= len(row.Data)
		if budget <= 0 {
			break
		}
	}
	return result, nil
}

func (s *Store) TranscriptSearch(ctx context.Context, transcriptID string, text []byte, maxMatches, maxScanBytes int) ([]TranscriptMatch, error) {
	if len(text) == 0 {
		return nil, badParam(fmt.Errorf("transcript search query is empty"))
	}
	if len(text) > 256 {
		return nil, badParam(fmt.Errorf("transcript search query is too long"))
	}
	if maxMatches <= 0 || maxMatches > 500 {
		maxMatches = 100
	}
	if maxScanBytes <= 0 || maxScanBytes > 64<<20 {
		maxScanBytes = 8 << 20
	}
	chunk := query.Use(s.db).TranscriptChunkRow
	rows := []TranscriptChunkRow{}
	err := chunk.WithContext(ctx).
		Where(chunk.TranscriptID.Eq(transcriptID), chunk.Kind.Eq(TranscriptChunkKindOutput)).
		Where(ownerConditions(ctx, chunk.TableName())...).
		Order(chunk.Seq).
		Limit(8192).
		Scan(&rows)
	if err != nil {
		return nil, dbError(err)
	}
	matcher := newVisibleMatcher(text, maxMatches)
	scanned := 0
	for _, row := range rows {
		if matcher.full() || scanned >= maxScanBytes {
			break
		}
		scanned += len(row.Data)
		matcher.consume(row.Seq, row.TS, row.Data)
	}
	return matcher.matches, nil
}

func (s *Store) TranscriptDelete(ctx context.Context, id string) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := query.Use(tx)
		transcript := queries.TranscriptRow
		row, err := transcript.WithContext(ctx).
			Where(transcript.ID.Eq(id)).
			Where(ownerConditions(ctx, transcript.TableName())...).
			First()
		if isNoRows(err) {
			return notFound("会话记录")
		}
		if err != nil {
			return err
		}
		if _, err := transcript.WithContext(ctx).
			Where(transcript.ID.Eq(id)).
			Where(ownerConditions(ctx, transcript.TableName())...).
			Delete(); err != nil {
			return err
		}
		if row.SyncOptIn {
			return transcriptTombstoneTx(ctx, tx, id)
		}
		return nil
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func transcriptTombstoneTx(ctx context.Context, tx *gorm.DB, id string) error {
	tombstone := query.Use(tx).SyncTombstone
	err := tombstone.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "owner_id"}, {Name: "id"}}, DoNothing: true}).
		Create(&SyncTombstone{ID: id, Kind: "transcript", DeletedAt: ids.NowMS(), OwnerID: ipc.OwnerID(ctx)})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) TranscriptListOptedIn(ctx context.Context) ([]TranscriptRow, error) {
	transcript := query.Use(s.db).TranscriptRow
	result := []TranscriptRow{}
	err := transcript.WithContext(ctx).
		Where(transcript.SyncOptIn.Is(true), transcript.EndedAt.IsNotNull()).
		Where(ownerConditions(ctx, transcript.TableName())...).
		Order(transcript.StartedAt, transcript.ID).
		Scan(&result)
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

func (s *Store) TranscriptReplaceContent(ctx context.Context, transcriptID string, bytes, chunks int64, truncated bool, content []TranscriptChunkRow) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := query.Use(tx)
		transcript := queries.TranscriptRow
		info, err := transcript.WithContext(ctx).
			Where(transcript.ID.Eq(transcriptID), transcript.ContentOmitted.Is(true)).
			Where(ownerConditions(ctx, transcript.TableName())...).
			UpdateColumns(map[string]any{"bytes": bytes, "chunks": chunks, "truncated": truncated, "content_omitted": false})
		if err != nil {
			return err
		}
		if info.RowsAffected == 0 {
			return notFound("会话记录")
		}
		chunk := queries.TranscriptChunkRow
		if _, err := chunk.WithContext(ctx).
			Where(chunk.TranscriptID.Eq(transcriptID)).
			Where(ownerConditions(ctx, chunk.TableName())...).
			Delete(); err != nil {
			return err
		}
		for _, row := range content {
			row.TranscriptID = transcriptID
			row.OwnerID = ipc.OwnerID(ctx)
			if err := chunk.WithContext(ctx).Create(&row); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) TranscriptSetSyncOptIn(ctx context.Context, id string, optIn bool) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := query.Use(tx)
		transcript := queries.TranscriptRow
		row, err := transcript.WithContext(ctx).
			Where(transcript.ID.Eq(id)).
			Where(ownerConditions(ctx, transcript.TableName())...).
			First()
		if isNoRows(err) {
			return notFound("会话记录")
		}
		if err != nil {
			return err
		}
		if optIn {
			tombstone := queries.SyncTombstone
			count, err := tombstone.WithContext(ctx).
				Where(tombstone.ID.Eq(id)).
				Where(ownerConditions(ctx, tombstone.TableName())...).
				Count()
			if err != nil {
				return err
			}
			if count > 0 {
				return badParam(fmt.Errorf("该记录已关闭同步并清除远端副本, 不能重新开启"))
			}
		}
		q := transcript.WithContext(ctx).Where(transcript.ID.Eq(id))
		if optIn {
			q = q.Where(transcript.EndedAt.IsNotNull())
		}
		info, err := q.Where(ownerConditions(ctx, transcript.TableName())...).
			UpdateColumns(map[string]any{"sync_opt_in": optIn})
		if err != nil {
			return err
		}
		if info.RowsAffected == 0 {
			return badParam(fmt.Errorf("进行中的会话不能开启同步"))
		}
		if row.SyncOptIn && !optIn {
			return transcriptTombstoneTx(ctx, tx, id)
		}
		return nil
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) TranscriptInsertSynced(ctx context.Context, row TranscriptRow, chunks []TranscriptChunkRow) error {
	if row.EndedAt == nil {
		return badParam(fmt.Errorf("synced transcript must be ended"))
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row.SyncOptIn = true
		row.OwnerID = ipc.OwnerID(ctx)
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"ended_at", "bytes", "chunks", "truncated", "content_omitted"}),
			Where:     clause.Where{Exprs: []clause.Expression{clause.Eq{Column: s.dialect.conflictTargetColumn("durable_transcript_offset", "owner_id"), Value: ipc.OwnerID(ctx)}}},
		}).Create(&row).Error; err != nil {
			return err
		}
		chunk := query.Use(tx).TranscriptChunkRow
		for _, item := range chunks {
			item.TranscriptID = row.ID
			item.OwnerID = ipc.OwnerID(ctx)
			if err := chunk.WithContext(ctx).Create(&item); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) DurableTranscriptOffsetGet(ctx context.Context, durableID string) (int64, error) {
	offset := query.Use(s.db).DurableTranscriptOffset
	row, err := offset.WithContext(ctx).
		Where(offset.DurableID.Eq(durableID)).
		Where(ownerConditions(ctx, offset.TableName())...).
		First()
	if isNoRows(err) {
		return 0, nil
	}
	if err != nil {
		return 0, dbError(err)
	}
	return row.Offset, nil
}

func (s *Store) DurableTranscriptOffsetSet(ctx context.Context, durableID string, value int64) error {
	now := ids.NowMS()
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "durable_id"}},
		DoUpdates: clause.AssignmentColumns([]string{s.dialect.offsetColumn(), "updated_at"}),
		Where:     clause.Where{Exprs: []clause.Expression{clause.Eq{Column: s.dialect.conflictTargetColumn("durable_transcript_offset", "owner_id"), Value: ipc.OwnerID(ctx)}}},
	}).Create(&DurableTranscriptOffset{DurableID: durableID, Offset: value, UpdatedAt: now, OwnerID: ipc.OwnerID(ctx)}).Error
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) DurableTranscriptOffsetDelete(ctx context.Context, durableID string) error {
	offset := query.Use(s.db).DurableTranscriptOffset
	_, err := offset.WithContext(ctx).
		Where(offset.DurableID.Eq(durableID)).
		Where(ownerConditions(ctx, offset.TableName())...).
		Delete()
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) EnforceTranscriptRetention(ctx context.Context, policy TranscriptRetentionPolicy) (TranscriptRetentionResult, error) {
	var result TranscriptRetentionResult
	if err := policy.validate(); err != nil {
		return result, err
	}
	if policy.MaxAge == 0 && policy.MaxCount == 0 {
		return result, nil
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		transcript := query.Use(tx).TranscriptRow
		if policy.MaxAge > 0 {
			rows := []TranscriptRow{}
			if err := transcript.WithContext(ctx).
				Where(transcript.EndedAt.IsNotNull(), transcript.EndedAt.Lt(time.Now().Add(-policy.MaxAge).UnixMilli())).
				Where(ownerConditions(ctx, transcript.TableName())...).
				Scan(&rows); err != nil {
				return err
			}
			deleted, err := s.transcriptRetentionDeleteRows(ctx, tx, rows)
			if err != nil {
				return err
			}
			result.Deleted += deleted
		}
		if policy.MaxCount > 0 {
			rows := []TranscriptRow{}
			if err := transcript.WithContext(ctx).
				Where(transcript.EndedAt.IsNotNull()).
				Where(ownerConditions(ctx, transcript.TableName())...).
				Order(transcript.EndedAt.Desc(), transcript.ID.Desc()).
				Limit(-1).
				Offset(int(policy.MaxCount)).
				Scan(&rows); err != nil {
				return err
			}
			deleted, err := s.transcriptRetentionDeleteRows(ctx, tx, rows)
			if err != nil {
				return err
			}
			result.Deleted += deleted
		}
		return nil
	})
	if err != nil {
		return result, dbError(err)
	}
	return result, nil
}

func (s *Store) transcriptRetentionDeleteRows(ctx context.Context, tx *gorm.DB, rows []TranscriptRow) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	ids := make([]string, 0, len(rows))
	optedIn := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		if row.SyncOptIn {
			optedIn = append(optedIn, row.ID)
		}
	}
	transcript := query.Use(tx).TranscriptRow
	info, err := transcript.WithContext(ctx).
		Where(transcript.ID.In(ids...)).
		Where(ownerConditions(ctx, transcript.TableName())...).
		Delete()
	if err != nil {
		return 0, err
	}
	for _, id := range optedIn {
		if err := transcriptTombstoneTx(ctx, tx, id); err != nil {
			return 0, err
		}
	}
	return info.RowsAffected, nil
}
