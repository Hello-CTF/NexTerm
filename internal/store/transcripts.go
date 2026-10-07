package store

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

type TranscriptRow struct {
	ID             string
	SessionID      string
	AssetID        string
	AssetName      string
	AssetKind      string
	StartedAt      int64
	EndedAt        *int64
	Bytes          int64
	Chunks         int64
	Truncated      bool
	SyncOptIn      bool
	ContentOmitted bool
}

const transcriptColumns = `id, session_id, asset_id, asset_name, asset_kind,
started_at, ended_at, bytes, chunks, truncated, sync_opt_in, content_omitted`

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
	_, err := s.db.ExecContext(ctx, `INSERT INTO transcript
(id, session_id, asset_id, asset_name, asset_kind, started_at, ended_at, bytes, chunks, truncated)
VALUES(?,?,?,?,?,?,NULL,0,0,0)`,
		row.ID, row.SessionID, row.AssetID, row.AssetName, row.AssetKind, row.StartedAt)
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) TranscriptAppendChunks(ctx context.Context, transcriptID string, chunks []TranscriptChunkRow, durableBytes ...map[string]int64) error {
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
	if len(durableBytes) > 0 {
		now := ids.NowMS()
		for durableID, count := range durableBytes[0] {
			if count <= 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO durable_transcript_offset(durable_id, `+s.dialect.offsetColumn()+`, updated_at)
VALUES(?,?,?) ON CONFLICT(durable_id) DO UPDATE SET `+s.dialect.offsetColumn()+`=`+s.dialect.offsetColumn()+`+?, updated_at=?`,
				durableID, count, now, count, now); err != nil {
				return dbError(err)
			}
		}
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
	_, err := s.db.ExecContext(ctx, `UPDATE transcript SET ended_at=?, truncated=`+s.dialect.ScalarMax()+`(truncated,?) WHERE id=? AND ended_at IS NULL`,
		endedAt, flag, transcriptID)
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) TranscriptGet(ctx context.Context, id string) (TranscriptRow, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+transcriptColumns+` FROM transcript WHERE id=?`, id)
	return scanTranscriptRow(row.Scan)
}

func scanTranscriptRow(scan func(...any) error) (TranscriptRow, error) {
	var row TranscriptRow
	var truncated, syncOptIn, contentOmitted int
	if err := scan(&row.ID, &row.SessionID, &row.AssetID, &row.AssetName, &row.AssetKind,
		&row.StartedAt, &row.EndedAt, &row.Bytes, &row.Chunks, &truncated, &syncOptIn, &contentOmitted); err != nil {
		if isNoRows(err) {
			return TranscriptRow{}, notFound("transcript")
		}
		return TranscriptRow{}, dbError(err)
	}
	row.Truncated = truncated != 0
	row.SyncOptIn = syncOptIn != 0
	row.ContentOmitted = contentOmitted != 0
	return row, nil
}

func (s *Store) TranscriptListByAsset(ctx context.Context, assetID string, limit int64) ([]TranscriptRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+transcriptColumns+` FROM transcript WHERE asset_id=?
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

func (s *Store) TranscriptHosts(ctx context.Context) ([]TranscriptHostRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.asset_id, MAX(t.asset_name), MAX(t.asset_kind),
COUNT(*), MAX(t.started_at),
CASE WHEN a.id IS NULL OR a.deleted_at IS NOT NULL THEN 1 ELSE 0 END
FROM transcript t LEFT JOIN asset a ON a.id = t.asset_id
GROUP BY t.asset_id ORDER BY MAX(t.started_at) DESC LIMIT 500`)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []TranscriptHostRow{}
	for rows.Next() {
		var row TranscriptHostRow
		var deleted int
		if err := rows.Scan(&row.AssetID, &row.AssetName, &row.AssetKind,
			&row.Transcripts, &row.LastStartedAt, &deleted); err != nil {
			return nil, dbError(err)
		}
		row.AssetDeleted = deleted != 0
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
	if len(query) > 256 {
		return nil, badParam(fmt.Errorf("transcript search query is too long"))
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
	matcher := newVisibleMatcher(query, maxMatches)
	scanned := 0
	for rows.Next() && !matcher.full() && scanned < maxScanBytes {
		var seq, ts int64
		var data []byte
		if err := rows.Scan(&seq, &ts, &data); err != nil {
			return nil, dbError(err)
		}
		scanned += len(data)
		matcher.consume(seq, ts, data)
	}
	return matcher.matches, rows.Err()
}

const (
	visibleWindowBytes   = 16 << 10
	visiblePreviewRadius = 80
)

type visibleSeqMark struct {
	offset int
	seq    int64
	ts     int64
}

type visibleMatcher struct {
	query      []byte
	window     []byte
	marks      []visibleSeqMark
	pending    []byte
	matches    []TranscriptMatch
	maxMatches int
	reportFrom int
}

func newVisibleMatcher(query []byte, maxMatches int) *visibleMatcher {
	return &visibleMatcher{
		query:      query,
		maxMatches: maxMatches,
	}
}

func (m *visibleMatcher) full() bool {
	return len(m.matches) >= m.maxMatches
}

func (m *visibleMatcher) consume(seq, ts int64, data []byte) {
	raw := m.pending
	m.pending = nil
	raw = append(raw, data...)
	visible, tail := visibleBytes(raw)
	m.pending = tail
	if len(visible) == 0 {
		return
	}
	m.marks = append(m.marks, visibleSeqMark{offset: len(m.window), seq: seq, ts: ts})
	m.window = append(m.window, visible...)
	m.searchNew()
	m.trim()
}

func (m *visibleMatcher) searchNew() {
	from := m.reportFrom
	window := m.window
	query := m.query
	for from <= len(window)-len(query) && !m.full() {
		index := bytes.Index(window[from:], query)
		if index < 0 {
			break
		}
		start := from + index
		m.matches = append(m.matches, TranscriptMatch{
			Seq:     m.seqAt(start),
			TS:      m.tsAt(start),
			Preview: transcriptPreview(window, start, len(query)),
		})
		from = start + len(query)
	}
	m.reportFrom = len(window) - len(query) + 1
	if m.reportFrom < 0 {
		m.reportFrom = 0
	}
}

func (m *visibleMatcher) seqAt(offset int) int64 {
	return m.markAt(offset).seq
}

func (m *visibleMatcher) tsAt(offset int) int64 {
	return m.markAt(offset).ts
}

func (m *visibleMatcher) markAt(offset int) visibleSeqMark {
	index := sort.Search(len(m.marks), func(i int) bool { return m.marks[i].offset > offset }) - 1
	if index < 0 {
		return visibleSeqMark{}
	}
	return m.marks[index]
}

func (m *visibleMatcher) trim() {
	if len(m.window) <= visibleWindowBytes {
		return
	}
	drop := len(m.window) - visibleWindowBytes
	m.window = append([]byte(nil), m.window[drop:]...)
	var carry visibleSeqMark
	kept := make([]visibleSeqMark, 0, len(m.marks))
	for _, mark := range m.marks {
		if mark.offset < drop {
			carry = mark
			continue
		}
		kept = append(kept, visibleSeqMark{offset: mark.offset - drop, seq: mark.seq, ts: mark.ts})
	}
	if len(kept) == 0 || kept[0].offset > 0 {
		kept = append([]visibleSeqMark{{offset: 0, seq: carry.seq, ts: carry.ts}}, kept...)
	}
	m.marks = kept
	if m.reportFrom > drop {
		m.reportFrom -= drop
	} else {
		m.reportFrom = 0
	}
}

func transcriptPreview(data []byte, matchStart, matchLen int) []byte {
	start := matchStart - visiblePreviewRadius
	if start < 0 {
		start = 0
	}
	end := matchStart + matchLen + visiblePreviewRadius
	if end > len(data) {
		end = len(data)
	}
	return bytes.Clone(data[start:end])
}

func visibleBytes(raw []byte) (visible []byte, tail []byte) {
	index := 0
	for index < len(raw) {
		current := raw[index]
		switch {
		case current == 0x1b:
			consumed, complete := escapeSequence(raw[index:])
			if !complete {
				return visible, raw[index:]
			}
			index += consumed
		case current == 0x9b:
			consumed, complete := csiSequence(raw[index+1:])
			if !complete {
				return visible, raw[index:]
			}
			index += 1 + consumed
		case current == 0x90 || current == 0x98 || current == 0x9e || current == 0x9f:
			consumed, complete := controlString(raw[index+1:])
			if !complete {
				return visible, raw[index:]
			}
			index += 1 + consumed
		default:
			r, size := utf8.DecodeRune(raw[index:])
			if r == utf8.RuneError && size <= 1 {
				if !utf8.FullRune(raw[index:]) {
					return visible, raw[index:]
				}
				visible = append(visible, current)
				index++
				continue
			}
			if r >= 0x80 && r <= 0x9f {
				switch r {
				case 0x9b:
					consumed, complete := csiSequence(raw[index+size:])
					if !complete {
						return visible, raw[index:]
					}
					index += size + consumed
				case 0x90, 0x98, 0x9e, 0x9f:
					consumed, complete := controlString(raw[index+size:])
					if !complete {
						return visible, raw[index:]
					}
					index += size + consumed
				default:
					index += size
				}
				continue
			}
			visible = append(visible, raw[index:index+size]...)
			index += size
		}
	}
	return visible, nil
}

func escapeSequence(raw []byte) (consumed int, complete bool) {
	if len(raw) < 2 {
		return 0, false
	}
	switch raw[1] {
	case '[':
		consumed, complete := csiSequence(raw[2:])
		return 2 + consumed, complete
	case ']':
		consumed, complete := oscSequence(raw[2:])
		return 2 + consumed, complete
	case 'P', 'X', '^', '_':
		consumed, complete := controlString(raw[2:])
		return 2 + consumed, complete
	default:
		index := 1
		for index < len(raw) && raw[index] >= 0x20 && raw[index] <= 0x2f {
			index++
		}
		if index >= len(raw) {
			return 0, false
		}
		if raw[index] >= 0x30 && raw[index] <= 0x7e {
			return index + 1, true
		}
		return index, true
	}
}

func controlString(raw []byte) (consumed int, complete bool) {
	for index := 0; index < len(raw); index++ {
		if raw[index] == 0x1b && index+1 < len(raw) && raw[index+1] == '\\' {
			return index + 2, true
		}
		if raw[index] == 0x9c {
			return index + 1, true
		}
	}
	return 0, false
}

func csiSequence(raw []byte) (consumed int, complete bool) {
	for index, current := range raw {
		if current >= 0x40 && current <= 0x7e {
			return index + 1, true
		}
		if current < 0x20 || current > 0x3f {
			return index, true
		}
	}
	return 0, false
}

func oscSequence(raw []byte) (consumed int, complete bool) {
	for index := 0; index < len(raw); index++ {
		if raw[index] == 0x07 {
			return index + 1, true
		}
		if raw[index] == 0x1b && index+1 < len(raw) && raw[index+1] == '\\' {
			return index + 2, true
		}
	}
	return 0, false
}

func (s *Store) TranscriptDelete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var syncOptIn int
	err = tx.QueryRowContext(ctx, "SELECT sync_opt_in FROM transcript WHERE id=?", id).Scan(&syncOptIn)
	if isNoRows(err) {
		return notFound("transcript")
	}
	if err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM transcript WHERE id=?", id); err != nil {
		return dbError(err)
	}
	if syncOptIn != 0 {
		if err := transcriptTombstoneTx(ctx, tx, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func transcriptTombstoneTx(ctx context.Context, tx *sql.Tx, id string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO sync_tombstone(id, kind, deleted_at) VALUES (?,'transcript',?)
ON CONFLICT(id) DO NOTHING`, id, ids.NowMS()); err != nil {
		return dbError(err)
	}
	return nil
}

// TranscriptListOptedIn 返回全部已 opt-in 且已结束的会话记录, 供同步推送枚举。
func (s *Store) TranscriptListOptedIn(ctx context.Context) ([]TranscriptRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+transcriptColumns+` FROM transcript
WHERE sync_opt_in=1 AND ended_at IS NOT NULL ORDER BY started_at, id`)
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

// TranscriptReplaceContent 用拉取到的完整内容替换仅元数据副本(或重写内容), 并清除 content_omitted 标记。
func (s *Store) TranscriptReplaceContent(ctx context.Context, transcriptID string, bytes, chunks int64, truncated bool, content []TranscriptChunkRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "DELETE FROM transcript_chunk WHERE transcript_id=?", transcriptID); err != nil {
		return dbError(err)
	}
	for _, chunk := range content {
		if _, err := tx.ExecContext(ctx, `INSERT INTO transcript_chunk
(transcript_id, seq, tab_id, ts, data) VALUES (?,?,?,?,?)`,
			transcriptID, chunk.Seq, chunk.TabID, chunk.TS, chunk.Data); err != nil {
			return dbError(err)
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE transcript SET bytes=?, chunks=?, truncated=?, content_omitted=0
WHERE id=? AND content_omitted=1`, bytes, chunks, boolInt(truncated), transcriptID)
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
	return tx.Commit()
}

// TranscriptSetSyncOptIn 切换单条会话记录的同步 opt-in; 进行中的会话不允许 opt-in。
// opt-out 会同时写入删除墓碑: 服务端副本与其他设备副本随下一轮同步清除, 本地记录保留。
// 已写入墓碑的记录不允许重新 opt-in: 墓碑的语义是"从同步集合中彻底删除",
// 允许恢复会让任意设备上的旧副本借 re-opt-in 复活, 与防复活语义冲突。
func (s *Store) TranscriptSetSyncOptIn(ctx context.Context, id string, optIn bool) error {
	flag := 0
	if optIn {
		flag = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var current int
	err = tx.QueryRowContext(ctx, "SELECT sync_opt_in FROM transcript WHERE id=?", id).Scan(&current)
	if isNoRows(err) {
		return notFound("transcript")
	}
	if err != nil {
		return dbError(err)
	}
	if flag == 1 {
		var tombstones int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sync_tombstone WHERE id=?", id).Scan(&tombstones); err != nil {
			return dbError(err)
		}
		if tombstones > 0 {
			return badParam(fmt.Errorf("该记录已关闭同步并清除远端副本, 不能重新开启"))
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE transcript SET sync_opt_in=?
WHERE id=? AND (? = 0 OR ended_at IS NOT NULL)`, flag, id, flag)
	if err != nil {
		return dbError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return dbError(err)
	}
	if affected == 0 {
		return badParam(fmt.Errorf("进行中的会话不能开启同步"))
	}
	if current != 0 && flag == 0 {
		if err := transcriptTombstoneTx(ctx, tx, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TranscriptInsertSynced 写入一条拉取到的已结束会话记录(含分块), 直接落为已 opt-in。
func (s *Store) TranscriptInsertSynced(ctx context.Context, row TranscriptRow, chunks []TranscriptChunkRow) error {
	if row.EndedAt == nil {
		return badParam(fmt.Errorf("synced transcript must be ended"))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	contentOmitted := 0
	if row.ContentOmitted {
		contentOmitted = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO transcript
(id, session_id, asset_id, asset_name, asset_kind, started_at, ended_at, bytes, chunks, truncated, sync_opt_in, content_omitted)
VALUES (?,?,?,?,?,?,?,?,?,?,1,?)
ON CONFLICT(id) DO UPDATE SET ended_at=excluded.ended_at, bytes=excluded.bytes, chunks=excluded.chunks,
truncated=excluded.truncated, content_omitted=excluded.content_omitted`,
		row.ID, row.SessionID, row.AssetID, row.AssetName, row.AssetKind, row.StartedAt, *row.EndedAt,
		row.Bytes, row.Chunks, boolInt(row.Truncated), contentOmitted); err != nil {
		return dbError(err)
	}
	for _, chunk := range chunks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO transcript_chunk
(transcript_id, seq, tab_id, ts, data) VALUES (?,?,?,?,?)`,
			row.ID, chunk.Seq, chunk.TabID, chunk.TS, chunk.Data); err != nil {
			return dbError(err)
		}
	}
	return tx.Commit()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Store) DurableTranscriptOffsetGet(ctx context.Context, durableID string) (int64, error) {
	var offset int64
	err := s.db.QueryRowContext(ctx,
		"SELECT "+s.dialect.offsetColumn()+" FROM durable_transcript_offset WHERE durable_id=?", durableID).Scan(&offset)
	if isNoRows(err) {
		return 0, nil
	}
	if err != nil {
		return 0, dbError(err)
	}
	return offset, nil
}

func (s *Store) DurableTranscriptOffsetSet(ctx context.Context, durableID string, offset int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO durable_transcript_offset(durable_id, `+s.dialect.offsetColumn()+`, updated_at)
VALUES(?,?,?) ON CONFLICT(durable_id) DO UPDATE SET `+s.dialect.offsetColumn()+`=excluded.`+s.dialect.offsetColumn()+`, updated_at=excluded.updated_at`,
		durableID, offset, ids.NowMS())
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) DurableTranscriptOffsetDelete(ctx context.Context, durableID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM durable_transcript_offset WHERE durable_id=?", durableID)
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if policy.MaxAge > 0 {
		cutoff := time.Now().Add(-policy.MaxAge).UnixMilli()
		deleted, err := s.transcriptRetentionDelete(ctx, tx,
			"ended_at IS NOT NULL AND ended_at < ?", cutoff)
		if err != nil {
			return result, dbError(err)
		}
		result.Deleted += deleted
	}
	if policy.MaxCount > 0 {
		deleted, err := s.transcriptRetentionDelete(ctx, tx, `ended_at IS NOT NULL AND id IN (
	SELECT id FROM transcript WHERE ended_at IS NOT NULL
	ORDER BY ended_at DESC, id DESC `+s.dialect.limitOffset()+`
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

// transcriptRetentionDelete 按保留策略删除已结束会话; 被删记录若已 opt-in 同步则同步写墓碑, 防止对端副本复活。
func (s *Store) transcriptRetentionDelete(ctx context.Context, tx *sql.Tx, predicate string, args ...any) (int64, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM transcript WHERE "+predicate+" AND sync_opt_in=1", args...)
	if err != nil {
		return 0, err
	}
	optedIn := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		optedIn = append(optedIn, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	deleted, err := retentionDelete(ctx, tx, "DELETE FROM transcript WHERE "+predicate, args...)
	if err != nil {
		return 0, err
	}
	for _, id := range optedIn {
		if err := transcriptTombstoneTx(ctx, tx, id); err != nil {
			return 0, err
		}
	}
	return deleted, nil
}
