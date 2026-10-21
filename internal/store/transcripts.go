package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gen/field"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type TranscriptRow = model.TranscriptRow
type TranscriptChunkRow = model.TranscriptChunkRow
type DurableTranscriptOffset = model.DurableTranscriptOffset
type DurableTabTitle = model.DurableTabTitle

const (
	TranscriptChunkKindOutput = 0
	TranscriptChunkKindInput  = 1
	TranscriptChunkKindResize = 2

	transcriptTombstoneKind = "transcript"
)

type TranscriptMatch struct {
	Seq     int64
	TS      int64
	Preview []byte
}

// transcriptChunksOrdered reports whether chunk timestamps are
// non-decreasing along the seq order — the write invariant behind the
// seek's bounded block-max-TS index.
func transcriptChunksOrdered(chunks []TranscriptChunkRow) bool {
	ordered := append([]TranscriptChunkRow(nil), chunks...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Seq < ordered[j].Seq })
	for index := 1; index < len(ordered); index++ {
		if ordered[index].TS < ordered[index-1].TS {
			return false
		}
	}
	return true
}

// transcriptFirstTimestamp returns the timestamp of the batch's smallest-seq
// chunk.
func transcriptFirstTimestamp(chunks []TranscriptChunkRow) int64 {
	first := chunks[0]
	for _, chunk := range chunks[1:] {
		if chunk.Seq < first.Seq {
			first = chunk
		}
	}
	return first.TS
}

// transcriptNewestBlockTS reads the newest stored block's timestamp for the
// append-order guard. On the service backends the read locks the newest row
// so a concurrent append cannot interleave a timestamp disorder; SQLite
// serializes writers by itself.
func (s *Store) transcriptNewestBlockTS(ctx context.Context, tx *gorm.DB, transcriptID string) (int64, bool, error) {
	newest := tx.WithContext(ctx).Model(&TranscriptChunkRow{}).
		Select("ts").
		Where("transcript_id = ?", transcriptID).
		Order("seq DESC").
		Limit(1)
	if s.backend == BackendMySQL || s.backend == BackendMariaDB || s.backend == BackendPostgres {
		newest = newest.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var ts int64
	result := newest.Scan(&ts)
	if result.Error != nil {
		return 0, false, dbError(result.Error)
	}
	if result.RowsAffected == 0 {
		return 0, false, nil
	}
	return ts, true, nil
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
	row.StoredBytes = 0
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
	// The timestamp-order invariant: chunk timestamps are non-decreasing
	// along the seq order, so the seek's bounded block-max-TS index is sound
	// for every transcript written through this store. A violating append is
	// rejected before any state changes.
	if !transcriptChunksOrdered(chunks) {
		return badParam(fmt.Errorf("transcript chunk timestamps must be non-decreasing"))
	}
	firstTS := transcriptFirstTimestamp(chunks)
	blocks, err := encodeTranscriptChunks(chunks)
	if err != nil {
		return err
	}
	var totalBytes, totalStoredBytes int64
	for _, chunk := range chunks {
		totalBytes += int64(len(chunk.Data))
	}
	for _, block := range blocks {
		totalStoredBytes += int64(len(block.Data))
	}
	totalChunks := int64(len(chunks))
	wasEmpty := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := query.Use(tx)
		// The batch's first timestamp must not step below the newest stored
		// block's timestamp; the read locks the newest row on the service
		// backends so a concurrent append cannot interleave a disorder.
		newestTS, hasNewest, err := s.transcriptNewestBlockTS(ctx, tx, transcriptID)
		if err != nil {
			return err
		}
		if hasNewest && newestTS > firstTS {
			return badParam(fmt.Errorf("transcript chunk timestamps must be non-decreasing"))
		}
		wasEmpty = !hasNewest
		for _, chunk := range blocks {
			chunk.TranscriptID = transcriptID
			chunk.OwnerID = ipc.OwnerID(ctx)
			if err := queries.TranscriptChunkRow.WithContext(ctx).Create(&chunk); err != nil {
				return err
			}
		}
		transcript := queries.TranscriptRow
		if _, err := transcript.WithContext(ctx).
			Where(transcript.ID.Eq(transcriptID)).
			Where(ownerConditions(ctx, transcript.TableName())...).
			UpdateColumns(map[string]any{
				"bytes":        gorm.Expr("bytes + ?", totalBytes),
				"stored_bytes": gorm.Expr("stored_bytes + ?", totalStoredBytes),
				"chunks":       gorm.Expr("chunks + ?", totalChunks),
			}); err != nil {
			return err
		}
		if len(durableBytes) > 0 {
			now := ids.NowMS()
			ownerID := ipc.OwnerID(ctx)
			for durableID, count := range durableBytes[0] {
				if count <= 0 {
					continue
				}
				// The insert never touches an existing row and the guarded UPDATE
				// advances only this owner's row or a legacy ownerless one. The
				// guard lives in the UPDATE's WHERE because MySQL and MariaDB
				// ignore ON CONFLICT ... WHERE, so every backend isolates owners
				// identically.
				if err := tx.Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "durable_id"}},
					DoNothing: true,
				}).Create(&DurableTranscriptOffset{DurableID: durableID, Offset: 0, UpdatedAt: now, OwnerID: ownerID}).Error; err != nil {
					return err
				}
				advance := tx.Model(&DurableTranscriptOffset{}).
					Where("durable_id = ? AND (owner_id = ? OR owner_id = '')", durableID, ownerID).
					Updates(map[string]any{
						"offset":     gorm.Expr("? + ?", clause.Column{Name: "offset"}, count),
						"updated_at": now,
						"owner_id":   ownerID,
					})
				if advance.Error != nil {
					return advance.Error
				}
				if advance.RowsAffected == 0 {
					s.logger.Warn("durable transcript offset advance skipped: row belongs to another owner", "durableId", durableID, "ownerId", ownerID)
				}
			}
		}
		return nil
	})
	if err != nil {
		return dbError(err)
	}
	if wasEmpty {
		// The transcript's first chunks established the invariant from a
		// verified-ordered batch, so later seeks can use the bounded index
		// without a validation scan.
		transcriptOrderMark(transcriptID, transcriptOrderOrdered)
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
			"truncated": MaxExpression(s.db, "", "truncated", truncated),
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
		Select(`t.asset_id, MAX(t.asset_name) AS asset_name, MAX(t.asset_kind) AS asset_kind, COUNT(*) AS transcripts, MAX(t.started_at) AS last_started_at, MAX(CASE WHEN substr(t.asset_id, 1, 7) = 'device/' THEN 0 WHEN a.id IS NULL OR a.deleted_at IS NOT NULL THEN 1 ELSE 0 END) AS asset_deleted`).
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
	rows, err := chunk.WithContext(ctx).
		Where(chunk.TranscriptID.Eq(transcriptID), chunk.Seq.Gte(afterSeq)).
		Where(ownerConditions(ctx, chunk.TableName())...).
		Order(chunk.Seq).
		Rows()
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []TranscriptChunkRow{}
	budget := maxBytes
	for rows.Next() {
		var stored TranscriptChunkRow
		if err := s.db.ScanRows(rows, &stored); err != nil {
			return nil, dbError(err)
		}
		chunks, err := decodeTranscriptBlock(stored)
		if err != nil {
			return nil, err
		}
		for _, row := range chunks {
			if row.Seq < afterSeq {
				continue
			}
			result = append(result, row)
			budget -= len(row.Data)
			if budget <= 0 || len(result) >= 4096 {
				return result, nil
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
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
	rows, err := chunk.WithContext(ctx).
		Where(chunk.TranscriptID.Eq(transcriptID)).
		Where(ownerConditions(ctx, chunk.TableName())...).
		Order(chunk.Seq).
		Rows()
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	matcher := newVisibleMatcher(text, maxMatches)
	scanned := 0
	outputChunks := 0
scanBlocks:
	for rows.Next() {
		var stored TranscriptChunkRow
		if err := s.db.ScanRows(rows, &stored); err != nil {
			return nil, dbError(err)
		}
		chunks, err := decodeTranscriptBlock(stored)
		if err != nil {
			return nil, err
		}
		for _, row := range chunks {
			if row.Kind != TranscriptChunkKindOutput {
				continue
			}
			if matcher.full() || scanned >= maxScanBytes || outputChunks >= 8192 {
				break scanBlocks
			}
			scanned += len(row.Data)
			outputChunks++
			matcher.consume(row.Seq, row.TS, row.Data)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
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
			return transcriptTombstoneTx(ctx, tx, id, row.OwnerID)
		}
		return nil
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func transcriptTombstoneTx(ctx context.Context, tx *gorm.DB, id, ownerID string) error {
	tombstone := query.Use(tx).SyncTombstone
	err := tombstone.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "owner_id"}, {Name: "id"}}, DoNothing: true}).
		Create(&SyncTombstone{ID: id, Kind: transcriptTombstoneKind, DeletedAt: ids.NowMS(), OwnerID: ownerID})
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
	// The replacement becomes the transcript's whole content, so it must
	// itself satisfy the timestamp-order invariant.
	if !transcriptChunksOrdered(content) {
		return badParam(fmt.Errorf("transcript chunk timestamps must be non-decreasing"))
	}
	blocks, err := encodeTranscriptChunks(content)
	if err != nil {
		return err
	}
	var storedBytes int64
	for _, block := range blocks {
		storedBytes += int64(len(block.Data))
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := query.Use(tx)
		transcript := queries.TranscriptRow
		info, err := transcript.WithContext(ctx).
			Where(transcript.ID.Eq(transcriptID), transcript.ContentOmitted.Is(true)).
			Where(ownerConditions(ctx, transcript.TableName())...).
			UpdateColumns(map[string]any{"bytes": bytes, "stored_bytes": storedBytes, "chunks": chunks, "truncated": truncated, "content_omitted": false})
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
		for _, row := range blocks {
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
	transcriptOrderMark(transcriptID, transcriptOrderOrdered)
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
			return transcriptTombstoneTx(ctx, tx, id, row.OwnerID)
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
	// The synced content becomes the whole transcript, so it must itself
	// satisfy the timestamp-order invariant.
	if !transcriptChunksOrdered(chunks) {
		return badParam(fmt.Errorf("transcript chunk timestamps must be non-decreasing"))
	}
	blocks, err := encodeTranscriptChunks(chunks)
	if err != nil {
		return err
	}
	if !row.ContentOmitted {
		row.StoredBytes = 0
		for _, block := range blocks {
			row.StoredBytes += int64(len(block.Data))
		}
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row.SyncOptIn = true
		row.OwnerID = ipc.OwnerID(ctx)
		// Identify a foreign-owned ID before any write. The check is an
		// explicit read, not a RowsAffected probe: MySQL and MariaDB count
		// changed rows, so an identical re-apply would look like a conflict.
		var existingOwner string
		if err := tx.Model(&TranscriptRow{}).
			Select("owner_id").
			Where("id = ?", row.ID).
			Limit(1).
			Scan(&existingOwner).Error; err != nil {
			return err
		}
		if existingOwner != "" && existingOwner != ipc.OwnerID(ctx) {
			return ipc.NewError(ipc.CodeForbidden, "会话记录已属于其他用户, 拒绝覆盖")
		}
		// Insert-if-absent never touches an existing row; the guarded UPDATE
		// below then applies only to this owner's row or a legacy ownerless
		// one on every backend (MySQL and MariaDB ignore ON CONFLICT ... WHERE).
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoNothing: true,
		}).Create(&row).Error; err != nil {
			return err
		}
		transcript := query.Use(tx).TranscriptRow
		if _, err := transcript.WithContext(ctx).
			Where(transcript.ID.Eq(row.ID)).
			Where(field.NewUnsafeFieldRaw("(owner_id = ? OR owner_id = '')", ipc.OwnerID(ctx))).
			UpdateColumns(map[string]any{
				"ended_at":        row.EndedAt,
				"bytes":           row.Bytes,
				"stored_bytes":    row.StoredBytes,
				"chunks":          row.Chunks,
				"truncated":       row.Truncated,
				"content_omitted": row.ContentOmitted,
				"owner_id":        ipc.OwnerID(ctx),
			}); err != nil {
			return err
		}
		// Verify the claim outcome with a current read that cannot conflate an
		// absent row or a stale ownerless one with a successful ownerless
		// claim: MySQL and MariaDB keep one REPEATABLE-READ snapshot per
		// transaction, so their verify locks the row to read the latest
		// committed owner, while PostgreSQL and SQLite already read the latest
		// committed version. Existence and owner are read separately — the
		// zero value never decides — and the claim wins only when the row
		// exists and its owner is the caller; anything else rejects before any
		// chunk write.
		verify := tx.Model(&TranscriptRow{}).
			Select("owner_id").
			Where("id = ?", row.ID).
			Limit(1)
		if s.backend == BackendMySQL || s.backend == BackendMariaDB {
			verify = verify.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var claimed TranscriptRow
		if err := verify.Take(&claimed).Error; err != nil {
			if isNoRows(err) {
				return ipc.NewError(ipc.CodeForbidden, "会话记录已属于其他用户, 拒绝覆盖")
			}
			return err
		}
		if claimed.OwnerID != ipc.OwnerID(ctx) {
			return ipc.NewError(ipc.CodeForbidden, "会话记录已属于其他用户, 拒绝覆盖")
		}
		chunk := query.Use(tx).TranscriptChunkRow
		if _, err := chunk.WithContext(ctx).
			Where(chunk.TranscriptID.Eq(row.ID)).
			Where(field.NewUnsafeFieldRaw("(owner_id = ? OR owner_id = '')", ipc.OwnerID(ctx))).
			Delete(); err != nil {
			return err
		}
		for _, item := range blocks {
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
	transcriptOrderMark(row.ID, transcriptOrderOrdered)
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
		DoUpdates: clause.AssignmentColumns([]string{"offset", "updated_at"}),
		Where:     clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Name: "owner_id"}, Value: ipc.OwnerID(ctx)}}},
	}).Create(&DurableTranscriptOffset{DurableID: durableID, Offset: value, UpdatedAt: now, OwnerID: ipc.OwnerID(ctx)}).Error
	if err != nil {
		return dbError(err)
	}
	return nil
}

// DurableTranscriptOffsetSeed resets a durable offset under an atomic owner
// guard; it reports whether a row was reset. The guard passes for rows owned by
// ownerID and for legacy ownerless rows (explicit owner_id = ”), so a stale
// resolution never touches a row the transcript writer claimed in between.
// Ownership is established by the writer when session output flushes, so a
// seed for a tab with no recorded output is a no-op rather than an insert.
func (s *Store) DurableTranscriptOffsetSeed(ctx context.Context, durableID, ownerID string, value int64) (bool, error) {
	now := ids.NowMS()
	info := s.db.WithContext(ctx).Model(&DurableTranscriptOffset{}).
		Where("durable_id = ? AND (owner_id = ? OR owner_id = '')", durableID, ownerID).
		Updates(map[string]any{"offset": value, "updated_at": now})
	if info.Error != nil {
		return false, dbError(info.Error)
	}
	return info.RowsAffected > 0, nil
}

// DurableTranscriptOffsetOwner resolves the owner recorded on a durable
// offset row; an empty string means the row is legacy ownerless or absent.
func (s *Store) DurableTranscriptOffsetOwner(ctx context.Context, durableID string) (string, error) {
	var ownerID string
	err := s.db.WithContext(ctx).Model(&DurableTranscriptOffset{}).
		Select("owner_id").
		Where("durable_id = ?", durableID).
		Limit(1).
		Scan(&ownerID).Error
	if err != nil {
		return "", dbError(err)
	}
	return ownerID, nil
}

// DurableTranscriptOffsetDeleteIfOwned removes a durable offset only while the
// row is still owned by ownerID or still legacy ownerless, so a delete racing
// a claim never removes a freshly claimed row.
func (s *Store) DurableTranscriptOffsetDeleteIfOwned(ctx context.Context, durableID, ownerID string) error {
	info := s.db.WithContext(ctx).
		Where("durable_id = ? AND (owner_id = ? OR owner_id = '')", durableID, ownerID).
		Delete(&DurableTranscriptOffset{})
	if info.Error != nil {
		return dbError(info.Error)
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

func (s *Store) DurableTabTitleGet(ctx context.Context, durableID string) (string, error) {
	title := query.Use(s.db).DurableTabTitle
	row, err := title.WithContext(ctx).
		Where(title.DurableID.Eq(durableID)).
		Where(ownerConditions(ctx, title.TableName())...).
		First()
	if isNoRows(err) {
		return "", nil
	}
	if err != nil {
		return "", dbError(err)
	}
	return row.Title, nil
}

func (s *Store) DurableTabTitleSet(ctx context.Context, durableID, value string) error {
	now := ids.NowMS()
	ownerID := ipc.OwnerID(ctx)
	// Insert-if-absent never touches an existing row; the guarded UPDATE then
	// applies only to this owner's row or a legacy ownerless one on every
	// backend (MySQL and MariaDB ignore ON CONFLICT ... WHERE).
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "durable_id"}},
			DoNothing: true,
		}).Create(&DurableTabTitle{DurableID: durableID, Title: value, UpdatedAt: now, OwnerID: ownerID}).Error; err != nil {
			return err
		}
		return tx.Model(&DurableTabTitle{}).
			Where("durable_id = ? AND (owner_id = ? OR owner_id = '')", durableID, ownerID).
			Updates(map[string]any{"title": value, "updated_at": now, "owner_id": ownerID}).Error
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}

// DurableTabTitleOwner resolves the owner recorded on a durable tab title;
// an empty string means the row is legacy ownerless or absent.
func (s *Store) DurableTabTitleOwner(ctx context.Context, durableID string) (string, error) {
	var ownerID string
	err := s.db.WithContext(ctx).Model(&DurableTabTitle{}).
		Select("owner_id").
		Where("durable_id = ?", durableID).
		Limit(1).
		Scan(&ownerID).Error
	if err != nil {
		return "", dbError(err)
	}
	return ownerID, nil
}

// DurableTabTitleDeleteIfOwned removes a durable tab title only while the row
// is still owned by ownerID or still legacy ownerless, so a delete racing a
// claim never removes a freshly claimed row.
func (s *Store) DurableTabTitleDeleteIfOwned(ctx context.Context, durableID, ownerID string) error {
	info := s.db.WithContext(ctx).
		Where("durable_id = ? AND (owner_id = ? OR owner_id = '')", durableID, ownerID).
		Delete(&DurableTabTitle{})
	if info.Error != nil {
		return dbError(info.Error)
	}
	return nil
}

func (s *Store) DurableTabTitleDelete(ctx context.Context, durableID string) error {
	title := query.Use(s.db).DurableTabTitle
	_, err := title.WithContext(ctx).
		Where(title.DurableID.Eq(durableID)).
		Where(ownerConditions(ctx, title.TableName())...).
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
	if policy.MaxAge > 0 {
		cutoff := time.Now().Add(-policy.MaxAge).UnixMilli()
		deleted, err := retentionDeleteBatches(ctx, s, "transcript", "age",
			func(tx *gorm.DB) ([]TranscriptRow, error) {
				transcript := query.Use(tx).TranscriptRow
				rows := make([]TranscriptRow, 0, retentionBatchSize)
				err := transcript.WithContext(ctx).
					Where(transcript.EndedAt.IsNotNull(), transcript.EndedAt.Lt(cutoff)).
					Where(ownerConditions(ctx, transcript.TableName())...).
					Order(transcript.ID.Asc()).
					Limit(retentionBatchSize).
					Scan(&rows)
				return rows, err
			},
			func(tx *gorm.DB, rows []TranscriptRow) (int64, error) {
				return s.transcriptRetentionDeleteRows(ctx, tx, rows, func(query *gorm.DB, _ TranscriptRow) *gorm.DB {
					return query.Where("ended_at < ?", cutoff)
				})
			},
		)
		result.Deleted += deleted
		if err != nil {
			return result, dbError(err)
		}
	}
	if policy.MaxCount > 0 {
		deleted, err := retentionDeleteBatches(ctx, s, "transcript", "count",
			func(tx *gorm.DB) ([]TranscriptRow, error) {
				transcript := query.Use(tx).TranscriptRow
				rows := make([]TranscriptRow, 0, retentionBatchSize)
				err := transcript.WithContext(ctx).
					Where(transcript.EndedAt.IsNotNull()).
					Where(ownerConditions(ctx, transcript.TableName())...).
					Order(transcript.EndedAt.Desc(), transcript.ID.Desc()).
					Limit(retentionBatchSize).
					Offset(int(policy.MaxCount)).
					Scan(&rows)
				return rows, err
			},
			func(tx *gorm.DB, rows []TranscriptRow) (int64, error) {
				// The keep set is evaluated inside each DELETE statement, so
				// eligibility is judged against the current committed ordering
				// instead of a separately read boundary a concurrent refresh
				// can stale. The derived-table wrapper keeps MySQL and MariaDB
				// from rejecting the same-table LIMIT subquery.
				keepSet := tx.WithContext(ctx).Model(&TranscriptRow{}).
					Select("id").
					Where("ended_at IS NOT NULL").
					Order("ended_at DESC, id DESC").
					Limit(int(policy.MaxCount))
				for _, condition := range ownerConditions(ctx, "transcript") {
					keepSet = keepSet.Where(condition)
				}
				return s.transcriptRetentionDeleteRows(ctx, tx, rows, func(query *gorm.DB, _ TranscriptRow) *gorm.DB {
					return query.Where("id NOT IN (SELECT id FROM (?) AS keep_set)", keepSet)
				})
			},
		)
		result.Deleted += deleted
		if err != nil {
			return result, dbError(err)
		}
	}
	return result, nil
}

// transcriptRetentionRowGuard narrows a selected row's delete to rows that are
// still retention-eligible when the delete statement executes, re-checked
// against the latest committed row on every backend.
type transcriptRetentionRowGuard func(query *gorm.DB, row TranscriptRow) *gorm.DB

// transcriptRetentionDeleteRows deletes the selected rows one guarded delete
// at a time and tombstones only the rows that delete actually removed, under
// each row's own owner and in the same transaction, so a background run
// without an owner in the context still attributes deletions to the correct
// sync scope. The guard re-checks retention eligibility against the latest
// committed row on every backend, including the sync opt-in flag: a row whose
// flag flipped after selection is skipped this round — the next round deletes
// it under its new state, writing or omitting the tombstone accordingly — so
// an opted-in row is never deleted without its tombstone. Rows that stay
// eligible are deleted and tombstoned together, never one without the other.
func (s *Store) transcriptRetentionDeleteRows(ctx context.Context, tx *gorm.DB, rows []TranscriptRow, guard transcriptRetentionRowGuard) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	deletedAt := ids.NowMS()
	tombstones := make([]*SyncTombstone, 0, len(rows))
	var deleted int64
	for _, row := range rows {
		info := guard(tx.WithContext(ctx).
			Where("id = ? AND owner_id = ? AND ended_at IS NOT NULL AND sync_opt_in = ?", row.ID, row.OwnerID, row.SyncOptIn), row).
			Delete(&TranscriptRow{})
		if info.Error != nil {
			return 0, info.Error
		}
		if info.RowsAffected == 0 {
			continue
		}
		deleted += info.RowsAffected
		if row.SyncOptIn {
			tombstones = append(tombstones, &SyncTombstone{ID: row.ID, Kind: transcriptTombstoneKind, DeletedAt: deletedAt, OwnerID: row.OwnerID})
		}
	}
	if len(tombstones) > 0 {
		tombstone := query.Use(tx).SyncTombstone
		if err := tombstone.WithContext(ctx).Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "owner_id"}, {Name: "id"}}, DoNothing: true,
		}).CreateInBatches(tombstones, retentionBatchSize); err != nil {
			return 0, err
		}
	}
	return deleted, nil
}
