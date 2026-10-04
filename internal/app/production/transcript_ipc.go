package production

import (
	"context"
	"encoding/base64"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

type transcriptSummaryDTO struct {
	ID           string `json:"id"`
	SessionID    string `json:"sessionId"`
	AssetID      string `json:"assetId"`
	AssetName    string `json:"assetName"`
	AssetKind    string `json:"assetKind"`
	AssetDeleted bool   `json:"assetDeleted"`
	StartedAt    int64  `json:"startedAt"`
	EndedAt      *int64 `json:"endedAt"`
	Bytes        int64  `json:"bytes"`
	Chunks       int64  `json:"chunks"`
	Truncated    bool   `json:"truncated"`
	Active       bool   `json:"active"`
}

type transcriptListRequest struct {
	AssetID string `json:"assetId"`
}

type transcriptReadRequest struct {
	ID       string `json:"id"`
	AfterSeq int64  `json:"afterSeq"`
	MaxBytes int    `json:"maxBytes"`
}

type transcriptChunkDTO struct {
	Seq        int64  `json:"seq"`
	TabID      string `json:"tabId"`
	TS         int64  `json:"ts"`
	DataBase64 string `json:"dataBase64"`
}

type transcriptReadResult struct {
	Chunks     []transcriptChunkDTO `json:"chunks"`
	NextSeq    int64                `json:"nextSeq"`
	Done       bool                 `json:"done"`
	TotalBytes int64                `json:"totalBytes"`
}

type transcriptSearchRequest struct {
	ID    string `json:"id"`
	Query string `json:"query"`
}

type transcriptMatchDTO struct {
	Seq     int64  `json:"seq"`
	TS      int64  `json:"ts"`
	Preview string `json:"preview"`
}

type transcriptIDRequest struct {
	ID string `json:"id"`
}

func (s *terminalCommandService) registerTranscripts(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "transcript_list", func(ctx context.Context, _ *ipc.Call, input transcriptListRequest) ([]transcriptSummaryDTO, error) {
				if err := store.EnsureID(input.AssetID); err != nil {
					return nil, err
				}
				rows, err := s.database.TranscriptListByAsset(ctx, input.AssetID, 0)
				if err != nil {
					return nil, err
				}
				assetDeleted := s.assetDeleted(ctx, input.AssetID)
				result := make([]transcriptSummaryDTO, 0, len(rows))
				for _, row := range rows {
					result = append(result, s.transcriptSummary(ctx, row, assetDeleted))
				}
				return result, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "transcript_read", func(ctx context.Context, _ *ipc.Call, input transcriptReadRequest) (transcriptReadResult, error) {
				if err := store.EnsureID(input.ID); err != nil {
					return transcriptReadResult{}, err
				}
				row, err := s.database.TranscriptGet(ctx, input.ID)
				if err != nil {
					return transcriptReadResult{}, err
				}
				chunks, err := s.database.TranscriptChunks(ctx, input.ID, input.AfterSeq, input.MaxBytes)
				if err != nil {
					return transcriptReadResult{}, err
				}
				result := transcriptReadResult{
					Chunks:     make([]transcriptChunkDTO, 0, len(chunks)),
					NextSeq:    input.AfterSeq,
					TotalBytes: row.Bytes,
				}
				for _, chunk := range chunks {
					result.Chunks = append(result.Chunks, transcriptChunkDTO{
						Seq:        chunk.Seq,
						TabID:      chunk.TabID,
						TS:         chunk.TS,
						DataBase64: base64.StdEncoding.EncodeToString(chunk.Data),
					})
					result.NextSeq = chunk.Seq + 1
				}
				result.Done = result.NextSeq >= row.Chunks
				return result, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "transcript_search", func(ctx context.Context, _ *ipc.Call, input transcriptSearchRequest) ([]transcriptMatchDTO, error) {
				if err := store.EnsureID(input.ID); err != nil {
					return nil, err
				}
				if input.Query == "" {
					return nil, ipc.BadParam(errTranscriptEmptyQuery{})
				}
				if _, err := s.database.TranscriptGet(ctx, input.ID); err != nil {
					return nil, err
				}
				matches, err := s.database.TranscriptSearch(ctx, input.ID, []byte(input.Query), 0, 0)
				if err != nil {
					return nil, err
				}
				result := make([]transcriptMatchDTO, 0, len(matches))
				for _, match := range matches {
					result = append(result, transcriptMatchDTO{
						Seq:     match.Seq,
						TS:      match.TS,
						Preview: string(match.Preview),
					})
				}
				return result, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "transcript_delete", func(ctx context.Context, _ *ipc.Call, input transcriptIDRequest) (any, error) {
				if err := store.EnsureID(input.ID); err != nil {
					return nil, err
				}
				return nil, s.database.TranscriptDelete(ctx, input.ID)
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

type errTranscriptEmptyQuery struct{}

func (errTranscriptEmptyQuery) Error() string { return "transcript search query is empty" }

func (s *terminalCommandService) assetDeleted(ctx context.Context, assetID string) bool {
	row, err := s.database.AssetGet(ctx, assetID)
	if err != nil {
		return true
	}
	return row.DeletedAt != nil
}

func (s *terminalCommandService) transcriptSummary(ctx context.Context, row store.TranscriptRow, assetDeleted bool) transcriptSummaryDTO {
	active := false
	if row.EndedAt == nil {
		if _, err := s.sessions.Session(row.SessionID); err == nil {
			active = true
		}
	}
	return transcriptSummaryDTO{
		ID:           row.ID,
		SessionID:    row.SessionID,
		AssetID:      row.AssetID,
		AssetName:    row.AssetName,
		AssetKind:    row.AssetKind,
		AssetDeleted: assetDeleted,
		StartedAt:    row.StartedAt,
		EndedAt:      row.EndedAt,
		Bytes:        row.Bytes,
		Chunks:       row.Chunks,
		Truncated:    row.Truncated,
		Active:       active,
	}
}
