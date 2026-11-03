package production

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type auditTimelineCursorDTO struct {
	TS     int64  `json:"ts"`
	Stream string `json:"stream"`
	ID     int64  `json:"id"`
}

type auditTimelineRequest struct {
	AssetID *string                 `json:"assetId,omitempty"`
	Origins []string                `json:"origins,omitempty"`
	From    *int64                  `json:"from,omitempty"`
	To      *int64                  `json:"to,omitempty"`
	Limit   int64                   `json:"limit,omitempty"`
	Cursor  *auditTimelineCursorDTO `json:"cursor,omitempty"`
}

type auditTimelineItemDTO struct {
	Stream     string  `json:"stream"` // audit | command | device
	ID         int64   `json:"id"`
	TS         int64   `json:"ts"`
	Origin     string  `json:"origin"` // user | ai | device
	Source     string  `json:"source"`
	Kind       string  `json:"kind"`
	AssetID    *string `json:"assetId"`
	SessionID  *string `json:"sessionId"`
	Command    *string `json:"command"`
	Payload    any     `json:"payload"`
	ExitCode   *int32  `json:"exitCode"`
	DurationMS *int64  `json:"durationMs"`
}

type auditTimelineResult struct {
	Items      []auditTimelineItemDTO  `json:"items"`
	NextCursor *auditTimelineCursorDTO `json:"nextCursor"`
}

func registerAuditTimeline(dispatcher *ipc.Dispatcher, database *store.Store, desktop bool) error {
	return ipc.Register(dispatcher, "audit_timeline", func(ctx context.Context, _ *ipc.Call, input auditTimelineRequest) (auditTimelineResult, error) {
		if desktop {
			// The desktop is a single-user machine: show every local record.
			ctx = ipc.WithUserID(ctx, "")
		}
		query := store.AuditTimelineQuery{AssetID: input.AssetID, Origins: input.Origins, From: input.From, To: input.To, Limit: input.Limit}
		if input.Cursor != nil {
			query.Cursor = &store.AuditTimelineCursor{TS: input.Cursor.TS, Stream: input.Cursor.Stream, ID: input.Cursor.ID}
		}
		items, next, err := database.AuditTimeline(ctx, query)
		if err != nil {
			return auditTimelineResult{}, err
		}
		result := auditTimelineResult{Items: make([]auditTimelineItemDTO, 0, len(items))}
		for _, item := range items {
			result.Items = append(result.Items, auditTimelineItemToDTO(item))
		}
		if next != nil {
			result.NextCursor = &auditTimelineCursorDTO{TS: next.TS, Stream: next.Stream, ID: next.ID}
		}
		return result, nil
	})
}

func auditTimelineItemToDTO(item store.AuditTimelineItem) auditTimelineItemDTO {
	dto := auditTimelineItemDTO{Stream: item.Stream, ID: item.ID, TS: item.TS, Origin: item.Origin}
	if row := item.Command; row != nil {
		asset, session, command := row.AssetID, row.SessionID, row.Command
		dto.Source, dto.Kind = row.Source, "command"
		dto.AssetID, dto.SessionID, dto.Command = &asset, &session, &command
		dto.ExitCode = row.ExitCode
		duration := row.FinishedAt - row.StartedAt
		dto.DurationMS = &duration
		return dto
	}
	if row := item.Audit; row != nil {
		dto.Source, dto.Kind = row.Source, row.Kind
		dto.AssetID, dto.SessionID = row.AssetID, row.SessionID
		dto.Payload = store.ParseJSONOr(row.PayloadJSON)
		dto.ExitCode, dto.DurationMS = row.ExitCode, row.DurationMS
	}
	return dto
}
