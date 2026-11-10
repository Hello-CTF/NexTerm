package production

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type commandSearchRequest struct {
	Text        string  `json:"text"`
	AssetID     *string `json:"assetId"`
	Cwd         *string `json:"cwd"`
	StartedFrom *int64  `json:"startedFrom"`
	StartedTo   *int64  `json:"startedTo"`
	Limit       int64   `json:"limit"`
	Offset      int64   `json:"offset"`
}

type commandSearchItemDTO struct {
	ID           int64   `json:"id"`
	SessionID    string  `json:"sessionId"`
	TabID        string  `json:"tabId"`
	AssetID      string  `json:"assetId"`
	Command      string  `json:"command"`
	Cwd          string  `json:"cwd"`
	Source       string  `json:"source"`
	ExitCode     *int32  `json:"exitCode"`
	StartedAt    int64   `json:"startedAt"`
	FinishedAt   int64   `json:"finishedAt"`
	TranscriptID *string `json:"transcriptId"`
}

type commandSearchResult struct {
	Items   []commandSearchItemDTO `json:"items"`
	HasMore bool                   `json:"hasMore"`
}

type transcriptHostSearchRequest struct {
	AssetID     string `json:"assetId"`
	Query       string `json:"query"`
	StartedFrom *int64 `json:"startedFrom"`
	StartedTo   *int64 `json:"startedTo"`
}

type transcriptHostSessionDTO struct {
	Transcript  transcriptSummaryDTO `json:"transcript"`
	Matches     []transcriptMatchDTO `json:"matches"`
	MoreMatches bool                 `json:"moreMatches"`
}

type transcriptHostSearchResult struct {
	Sessions        []transcriptHostSessionDTO `json:"sessions"`
	ScannedSessions int                        `json:"scannedSessions"`
	ScannedBytes    int64                      `json:"scannedBytes"`
	Truncated       bool                       `json:"truncated"`
	// UnreadableSessions counts sessions skipped because their recorded output cannot be decoded.
	UnreadableSessions int `json:"unreadableSessions"`
}

func (s *terminalCommandService) registerSearch(dispatcher *ipc.Dispatcher) error {
	if err := ipc.Register(dispatcher, "command_search", func(ctx context.Context, _ *ipc.Call, input commandSearchRequest) (commandSearchResult, error) {
		rows, hasMore, err := s.database.CommandLogSearch(ctx, store.CommandLogSearch{
			Text: input.Text, AssetID: input.AssetID, Cwd: input.Cwd, StartedFrom: input.StartedFrom, StartedTo: input.StartedTo,
			Limit: input.Limit, Offset: input.Offset,
		})
		if err != nil {
			return commandSearchResult{}, err
		}
		result := commandSearchResult{Items: make([]commandSearchItemDTO, 0, len(rows)), HasMore: hasMore}
		for _, row := range rows {
			result.Items = append(result.Items, commandSearchItemDTO{
				ID: row.ID, SessionID: row.SessionID, TabID: row.TabID, AssetID: row.AssetID,
				Command: row.Command, Cwd: row.Cwd, Source: row.Source, ExitCode: row.ExitCode,
				StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, TranscriptID: row.TranscriptID,
			})
		}
		return result, nil
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "transcript_search_host", func(ctx context.Context, _ *ipc.Call, input transcriptHostSearchRequest) (transcriptHostSearchResult, error) {
		if err := ensureTranscriptAssetID(input.AssetID); err != nil {
			return transcriptHostSearchResult{}, err
		}
		if input.Query == "" {
			return transcriptHostSearchResult{}, ipc.BadParam(errTranscriptEmptyQuery{})
		}
		found, err := s.database.TranscriptSearchHost(ctx, store.TranscriptHostSearch{
			AssetID: input.AssetID, Text: []byte(input.Query), StartedFrom: input.StartedFrom, StartedTo: input.StartedTo,
		})
		if err != nil {
			return transcriptHostSearchResult{}, err
		}
		assetDeleted := s.assetDeleted(ctx, input.AssetID)
		result := transcriptHostSearchResult{
			Sessions:        make([]transcriptHostSessionDTO, 0, len(found.Sessions)),
			ScannedSessions: found.ScannedSessions,
			ScannedBytes:    found.ScannedBytes,
			Truncated:       found.Truncated,

			UnreadableSessions: found.UnreadableSessions,
		}
		for _, session := range found.Sessions {
			matches := make([]transcriptMatchDTO, 0, len(session.Matches))
			for _, match := range session.Matches {
				matches = append(matches, transcriptMatchDTO{Seq: match.Seq, TS: match.TS, Preview: string(match.Preview)})
			}
			result.Sessions = append(result.Sessions, transcriptHostSessionDTO{
				Transcript:  s.transcriptSummary(ctx, session.Transcript, assetDeleted),
				Matches:     matches,
				MoreMatches: session.MoreMatches,
			})
		}
		return result, nil
	})
}
