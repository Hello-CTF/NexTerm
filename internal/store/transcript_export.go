package store

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gen"
)

// TranscriptExportFilter selects the caller's transcripts for a batch export. An empty AssetID covers every host;
// StartedFrom (inclusive) and StartedBefore (exclusive) bound the start time in Unix milliseconds, zero meaning unbounded.
type TranscriptExportFilter struct {
	AssetID       string
	StartedFrom   int64
	StartedBefore int64
	StarredOnly   bool
}

// TranscriptListForExport returns the metadata of the matching transcripts, oldest first. Content is not loaded.
func (s *Store) TranscriptListForExport(ctx context.Context, filter TranscriptExportFilter) ([]TranscriptRow, error) {
	transcript := query.Use(s.db).TranscriptRow
	conditions := []gen.Condition{}
	if filter.AssetID != "" {
		conditions = append(conditions, transcript.AssetID.Eq(filter.AssetID))
	}
	if filter.StartedFrom > 0 {
		conditions = append(conditions, transcript.StartedAt.Gte(filter.StartedFrom))
	}
	if filter.StartedBefore > 0 {
		conditions = append(conditions, transcript.StartedAt.Lt(filter.StartedBefore))
	}
	if filter.StarredOnly {
		conditions = append(conditions, transcript.Starred.Is(true))
	}
	result := []TranscriptRow{}
	err := transcript.WithContext(ctx).
		Where(conditions...).
		Where(ownerConditions(ctx, transcript.TableName())...).
		Order(transcript.StartedAt, transcript.ID).
		Scan(&result)
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}
