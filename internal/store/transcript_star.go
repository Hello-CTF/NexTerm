package store

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gen"
)

// TranscriptSetStarred marks or unmarks a transcript. A starred transcript is skipped by automatic and manual cleanup.
func (s *Store) TranscriptSetStarred(ctx context.Context, id string, starred bool) error {
	transcript := query.Use(s.db).TranscriptRow
	count, err := transcript.WithContext(ctx).
		Where(transcript.ID.Eq(id)).
		Where(ownerConditions(ctx, transcript.TableName())...).
		Count()
	if err != nil {
		return dbError(err)
	}
	if count == 0 {
		return notFound("会话记录")
	}
	if _, err := transcript.WithContext(ctx).
		Where(transcript.ID.Eq(id)).
		Where(ownerConditions(ctx, transcript.TableName())...).
		UpdateColumns(map[string]any{"starred": starred}); err != nil {
		return dbError(err)
	}
	return nil
}

// TranscriptKeepLocal turns off sync for a transcript a remote tombstone deleted elsewhere, without writing a tombstone
// of its own: the record stays on this device and no longer takes part in sync.
func (s *Store) TranscriptKeepLocal(ctx context.Context, id string) error {
	transcript := query.Use(s.db).TranscriptRow
	if _, err := transcript.WithContext(ctx).
		Where(transcript.ID.Eq(id)).
		Where(ownerConditions(ctx, transcript.TableName())...).
		UpdateColumns(map[string]any{"sync_opt_in": false}); err != nil {
		return dbError(err)
	}
	return nil
}

// TranscriptListStarredByAsset lists an asset's starred transcripts, newest first, without the recent-list cap.
func (s *Store) TranscriptListStarredByAsset(ctx context.Context, assetID string) ([]TranscriptRow, error) {
	transcript := query.Use(s.db).TranscriptRow
	result := []TranscriptRow{}
	err := transcript.WithContext(ctx).
		Where(transcript.AssetID.Eq(assetID), transcript.Starred.Is(true)).
		Where(ownerConditions(ctx, transcript.TableName())...).
		Order(transcript.StartedAt.Desc(), transcript.ID.Desc()).
		Scan(&result)
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

// TranscriptUsage summarizes the caller's transcripts for the cleanup settings.
type TranscriptUsage struct {
	Total       int64
	Starred     int64
	StoredBytes int64
	// Purgeable and PurgeableBytes count the ended, unstarred transcripts that ended before the requested cutoff.
	Purgeable      int64
	PurgeableBytes int64
}

// TranscriptUsageBefore reports transcript counts and stored size, and what a purge with the cutoff would delete.
func (s *Store) TranscriptUsageBefore(ctx context.Context, cutoff int64) (TranscriptUsage, error) {
	var usage TranscriptUsage
	transcript := query.Use(s.db).TranscriptRow
	type totals struct {
		Count int64
		Bytes int64
	}
	sum := func(conditions ...gen.Condition) (totals, error) {
		var result totals
		err := transcript.WithContext(ctx).
			Where(ownerConditions(ctx, transcript.TableName())...).
			Where(conditions...).
			UnderlyingDB().
			Select("COUNT(*) AS count, COALESCE(SUM(stored_bytes), 0) AS bytes").
			Scan(&result).Error
		return result, err
	}
	all, err := sum()
	if err != nil {
		return usage, dbError(err)
	}
	starred, err := sum(transcript.Starred.Is(true))
	if err != nil {
		return usage, dbError(err)
	}
	purgeable, err := sum(transcript.EndedAt.IsNotNull(), transcript.EndedAt.Lt(cutoff), transcript.Starred.Is(false))
	if err != nil {
		return usage, dbError(err)
	}
	usage.Total, usage.StoredBytes = all.Count, all.Bytes
	usage.Starred = starred.Count
	usage.Purgeable, usage.PurgeableBytes = purgeable.Count, purgeable.Bytes
	return usage, nil
}
