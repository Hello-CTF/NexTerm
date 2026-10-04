package store

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

type TranscriptRow struct {
	ID        string
	SessionID string
	AssetID   string
	AssetName string
	AssetKind string
	StartedAt int64
	EndedAt   *int64
	Bytes     int64
	Chunks    int64
	Truncated bool
}

type TranscriptChunkRow struct {
	Seq   int64
	TabID string
	TS    int64
	Data  []byte
}

type TranscriptMatch struct {
	Seq     int64
	TS      int64
	Preview []byte
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO transcript
(id, session_id, asset_id, asset_name, asset_kind, started_at, ended_at, bytes, chunks, truncated)
VALUES(?,?,?,?,?,?,NULL,0,0,0)`,
		row.ID, row.SessionID, row.AssetID, row.AssetName, row.AssetKind, row.StartedAt)
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) TranscriptAppendChunks(ctx context.Context, transcriptID string, chunks []TranscriptChunkRow) error {
	if len(chunks) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var totalBytes, totalChunks int64
	for _, chunk := range chunks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO transcript_chunk
(transcript_id, seq, tab_id, ts, data) VALUES(?,?,?,?,?)`,
			transcriptID, chunk.Seq, chunk.TabID, chunk.TS, chunk.Data); err != nil {
			return dbError(err)
		}
		totalBytes += int64(len(chunk.Data))
		totalChunks++
	}
	if _, err := tx.ExecContext(ctx, `UPDATE transcript SET bytes=bytes+?, chunks=chunks+? WHERE id=?`,
		totalBytes, totalChunks, transcriptID); err != nil {
		return dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) TranscriptEnd(ctx context.Context, transcriptID string, endedAt int64, truncated bool) error {
	flag := 0
	if truncated {
		flag = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE transcript SET ended_at=?, truncated=MAX(truncated,?) WHERE id=? AND ended_at IS NULL`,
		endedAt, flag, transcriptID)
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) TranscriptGet(ctx context.Context, id string) (TranscriptRow, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, session_id, asset_id, asset_name, asset_kind,
started_at, ended_at, bytes, chunks, truncated FROM transcript WHERE id=?`, id)
	return scanTranscriptRow(row.Scan)
}

func scanTranscriptRow(scan func(...any) error) (TranscriptRow, error) {
	var row TranscriptRow
	var truncated int
	if err := scan(&row.ID, &row.SessionID, &row.AssetID, &row.AssetName, &row.AssetKind,
		&row.StartedAt, &row.EndedAt, &row.Bytes, &row.Chunks, &truncated); err != nil {
		if isNoRows(err) {
			return TranscriptRow{}, notFound("transcript")
		}
		return TranscriptRow{}, dbError(err)
	}
	row.Truncated = truncated != 0
	return row, nil
}

func (s *Store) TranscriptListByAsset(ctx context.Context, assetID string, limit int64) ([]TranscriptRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_id, asset_id, asset_name, asset_kind,
started_at, ended_at, bytes, chunks, truncated FROM transcript WHERE asset_id=?
ORDER BY started_at DESC, id DESC LIMIT ?`, assetID, limit)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []TranscriptRow{}
	for rows.Next() {
		row, err := scanTranscriptRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) TranscriptChunks(ctx context.Context, transcriptID string, afterSeq int64, maxBytes int) ([]TranscriptChunkRow, error) {
	if maxBytes <= 0 || maxBytes > 8<<20 {
		maxBytes = 1 << 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT seq, tab_id, ts, data FROM transcript_chunk
WHERE transcript_id=? AND seq>=? ORDER BY seq LIMIT 4096`, transcriptID, afterSeq)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []TranscriptChunkRow{}
	budget := maxBytes
	for rows.Next() {
		var chunk TranscriptChunkRow
		if err := rows.Scan(&chunk.Seq, &chunk.TabID, &chunk.TS, &chunk.Data); err != nil {
			return nil, dbError(err)
		}
		result = append(result, chunk)
		budget -= len(chunk.Data)
		if budget <= 0 {
			break
		}
	}
	return result, rows.Err()
}

func (s *Store) TranscriptSearch(ctx context.Context, transcriptID string, query []byte, maxMatches, maxScanBytes int) ([]TranscriptMatch, error) {
	if len(query) == 0 {
		return nil, badParam(fmt.Errorf("transcript search query is empty"))
	}
	if maxMatches <= 0 || maxMatches > 500 {
		maxMatches = 100
	}
	if maxScanBytes <= 0 || maxScanBytes > 64<<20 {
		maxScanBytes = 8 << 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT seq, ts, data FROM transcript_chunk
WHERE transcript_id=? ORDER BY seq LIMIT 8192`, transcriptID)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	matches := []TranscriptMatch{}
	scanned := 0
	for rows.Next() && len(matches) < maxMatches && scanned < maxScanBytes {
		var seq, ts int64
		var data []byte
		if err := rows.Scan(&seq, &ts, &data); err != nil {
			return nil, dbError(err)
		}
		scanned += len(data)
		for offset := 0; len(matches) < maxMatches; {
			index := bytes.Index(data[offset:], query)
			if index < 0 {
				break
			}
			start := offset + index
			matches = append(matches, TranscriptMatch{
				Seq:     seq,
				TS:      ts,
				Preview: transcriptPreview(data, start, len(query)),
			})
			offset = start + len(query)
		}
	}
	return matches, rows.Err()
}

func transcriptPreview(data []byte, matchStart, matchLen int) []byte {
	const previewRadius = 80
	start := matchStart - previewRadius
	if start < 0 {
		start = 0
	}
	end := matchStart + matchLen + previewRadius
	if end > len(data) {
		end = len(data)
	}
	return bytes.Clone(data[start:end])
}

func (s *Store) TranscriptDelete(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM transcript WHERE id=?", id)
	if err != nil {
		return dbError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return dbError(err)
	}
	if affected == 0 {
		return notFound("transcript")
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if policy.MaxAge > 0 {
		cutoff := time.Now().Add(-policy.MaxAge).UnixMilli()
		deleted, err := retentionDelete(ctx, tx,
			"DELETE FROM transcript WHERE ended_at IS NOT NULL AND ended_at < ?", cutoff)
		if err != nil {
			return result, dbError(err)
		}
		result.Deleted += deleted
	}
	if policy.MaxCount > 0 {
		deleted, err := retentionDelete(ctx, tx, `DELETE FROM transcript
WHERE ended_at IS NOT NULL AND id IN (
	SELECT id FROM transcript WHERE ended_at IS NOT NULL
	ORDER BY ended_at DESC, id DESC LIMIT -1 OFFSET ?
)`, policy.MaxCount)
		if err != nil {
			return result, dbError(err)
		}
		result.Deleted += deleted
	}
	if err := tx.Commit(); err != nil {
		return result, dbError(err)
	}
	return result, nil
}
