package store

import (
	"context"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

const (
	transcriptHostSearchMaxSessions = 20
	transcriptHostSearchMaxMatches  = 50
)

// transcriptHostSearchByteBudget is the total output bytes scanned across all
// sessions of one host search; a variable so tests can lower it.
var transcriptHostSearchByteBudget = 64 << 20

type TranscriptHostSearch struct {
	AssetID     string
	Text        []byte
	StartedFrom *int64
	StartedTo   *int64
}

type TranscriptSessionMatches struct {
	Transcript  TranscriptRow
	Matches     []TranscriptMatch
	MoreMatches bool
}

type TranscriptHostSearchResult struct {
	Sessions        []TranscriptSessionMatches
	ScannedSessions int
	ScannedBytes    int64
	Truncated       bool
}

func (s *Store) TranscriptSearchHost(ctx context.Context, input TranscriptHostSearch) (TranscriptHostSearchResult, error) {
	result := TranscriptHostSearchResult{Sessions: []TranscriptSessionMatches{}}
	if len(input.Text) == 0 {
		return result, badParam(fmt.Errorf("transcript search query is empty"))
	}
	if len(input.Text) > 256 {
		return result, badParam(fmt.Errorf("transcript search query is too long"))
	}
	transcript := query.Use(s.db).TranscriptRow
	q := transcript.WithContext(ctx).
		Where(transcript.AssetID.Eq(input.AssetID)).
		Where(ownerConditions(ctx, transcript.TableName())...)
	if input.StartedFrom != nil {
		q = q.Where(transcript.StartedAt.Gte(*input.StartedFrom))
	}
	if input.StartedTo != nil {
		q = q.Where(transcript.StartedAt.Lt(*input.StartedTo))
	}
	var rows []TranscriptRow
	if err := q.Order(transcript.StartedAt.Desc(), transcript.ID.Desc()).
		Limit(transcriptHostSearchMaxSessions + 1).
		Scan(&rows); err != nil {
		return result, dbError(err)
	}
	if len(rows) > transcriptHostSearchMaxSessions {
		rows = rows[:transcriptHostSearchMaxSessions]
		result.Truncated = true
	}
	for _, row := range rows {
		remaining := int64(transcriptHostSearchByteBudget) - result.ScannedBytes
		if remaining <= 0 {
			result.Truncated = true
			break
		}
		matches, scanned, stopped, err := s.scanTranscriptOutput(ctx, row.ID, input.Text, transcriptHostSearchMaxMatches, int(remaining))
		if err != nil {
			return result, err
		}
		result.ScannedSessions++
		result.ScannedBytes += int64(scanned)
		if stopped {
			result.Truncated = true
		}
		if len(matches) > 0 {
			result.Sessions = append(result.Sessions, TranscriptSessionMatches{
				Transcript:  row,
				Matches:     matches,
				MoreMatches: stopped && len(matches) >= transcriptHostSearchMaxMatches,
			})
		}
	}
	return result, nil
}
