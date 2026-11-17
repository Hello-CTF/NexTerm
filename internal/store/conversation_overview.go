package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gen/field"
)

// Conversation states shown by the AI workbench, derived from the conversation's chat runs.
const (
	ConvStatusRunning     = "running"
	ConvStatusInterrupted = "interrupted"
	ConvStatusCompleted   = "completed"
	ConvStatusFailed      = "failed"
	// ConvStatusIdle is a conversation without any run, such as one created and never sent.
	ConvStatusIdle = "idle"
)

// MaxConvOverviewQueryBytes bounds the workbench search text.
const MaxConvOverviewQueryBytes = 256

const convOverviewChunk = 500

// ConvOverviewFilter selects conversations for the AI workbench. Every field is optional.
type ConvOverviewFilter struct {
	IDs []string
	// Query matches the title or any saved message text; ASCII matching is case-insensitive.
	Query string
	// UpdatedFrom (inclusive) and UpdatedBefore (exclusive) bound the last activity in Unix ms; 0 is unbounded.
	UpdatedFrom   int64
	UpdatedBefore int64
}

// ConvOverviewRow is one conversation with the host its scope names and the state of its runs.
type ConvOverviewRow struct {
	ConversationRow
	// AssetID is the host recorded in the conversation scope; empty when the conversation names none.
	AssetID   string
	Status    string
	RunCount  int64
	LastRunAt int64
}

// ConversationAssetID returns the asset the stored conversation scope names.
func ConversationAssetID(scopeJSON string) string {
	var envelope struct {
		Scope struct {
			AssetID string `json:"assetId"`
		} `json:"scope"`
	}
	if json.Unmarshal([]byte(scopeJSON), &envelope) != nil {
		return ""
	}
	return envelope.Scope.AssetID
}

// ConvOverview lists the caller's conversations matching the filter, most recently active first.
func (s *Store) ConvOverview(ctx context.Context, filter ConvOverviewFilter) ([]ConvOverviewRow, error) {
	if len(filter.Query) > MaxConvOverviewQueryBytes {
		return nil, badParam(errors.New("搜索内容过长"))
	}
	if filter.UpdatedFrom < 0 || filter.UpdatedBefore < 0 || (filter.UpdatedBefore > 0 && filter.UpdatedBefore <= filter.UpdatedFrom) {
		return nil, badParam(errors.New("时间范围无效"))
	}
	queries := query.Use(s.db)
	conversation := queries.ConversationRow
	q := conversation.WithContext(ctx).Where(s.aiHistoryOwnerConditions(ctx, conversation.TableName())...)
	if filter.IDs != nil {
		if len(filter.IDs) == 0 {
			return []ConvOverviewRow{}, nil
		}
		q = q.Where(conversation.ID.In(filter.IDs...))
	}
	if filter.UpdatedFrom > 0 {
		q = q.Where(conversation.UpdatedAt.Gte(filter.UpdatedFrom))
	}
	if filter.UpdatedBefore > 0 {
		q = q.Where(conversation.UpdatedAt.Lt(filter.UpdatedBefore))
	}
	rows := []ConversationRow{}
	if err := q.Order(conversation.UpdatedAt.Desc(), conversation.ID.Desc()).Scan(&rows); err != nil {
		return nil, dbError(err)
	}
	if text := strings.TrimSpace(filter.Query); text != "" {
		matched, err := s.convMessageMatches(ctx, text)
		if err != nil {
			return nil, err
		}
		lowered := strings.ToLower(text)
		kept := rows[:0]
		for _, row := range rows {
			if matched[row.ID] || strings.Contains(strings.ToLower(row.Title), lowered) {
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	runs, err := s.convRunStates(ctx, rows)
	if err != nil {
		return nil, err
	}
	result := make([]ConvOverviewRow, len(rows))
	for index, row := range rows {
		state := runs[row.ID]
		result[index] = ConvOverviewRow{
			ConversationRow: row, AssetID: ConversationAssetID(row.ScopeJSON),
			Status: state.status(), RunCount: state.count, LastRunAt: state.lastRunAt,
		}
	}
	return result, nil
}

// convMessageMatches returns the conversations with a saved message containing text. The JSON encoding of the
// message is searched, so text with characters JSON escapes (quotes, backslashes, control characters) does not match.
func (s *Store) convMessageMatches(ctx context.Context, text string) (map[string]bool, error) {
	message := query.Use(s.db).MessageRow
	ids := []string{}
	err := message.WithContext(ctx).
		Where(s.aiHistoryOwnerConditions(ctx, message.TableName())...).
		Where(field.NewUnsafeFieldRaw(
			"(LOWER(content_json) LIKE ? ESCAPE '!' OR content_json LIKE ? ESCAPE '!')",
			"%"+escapeLike(strings.ToLower(text))+"%", "%"+escapeLike(text)+"%")).
		Distinct(message.ConversationID).
		Pluck(message.ConversationID, &ids)
	if err != nil {
		return nil, dbError(err)
	}
	result := make(map[string]bool, len(ids))
	for _, id := range ids {
		result[id] = true
	}
	return result, nil
}

type convRunState struct {
	count       int64
	lastRunAt   int64
	latest      string
	running     bool
	interrupted bool
}

// status follows the dock: a running run wins, then an interrupted run still waiting to be resumed, then the
// latest run's outcome. A stopped (canceled) or expired run counts as interrupted.
func (s convRunState) status() string {
	switch {
	case s.running:
		return ConvStatusRunning
	case s.interrupted:
		return ConvStatusInterrupted
	}
	switch s.latest {
	case "":
		return ConvStatusIdle
	case RunStatusCompleted:
		return ConvStatusCompleted
	case RunStatusFailed:
		return ConvStatusFailed
	default:
		return ConvStatusInterrupted
	}
}

func (s *Store) convRunStates(ctx context.Context, rows []ConversationRow) (map[string]convRunState, error) {
	result := make(map[string]convRunState, len(rows))
	run := query.Use(s.db).RunRow
	for start := 0; start < len(rows); start += convOverviewChunk {
		end := min(start+convOverviewChunk, len(rows))
		ids := make([]string, 0, end-start)
		for _, row := range rows[start:end] {
			ids = append(ids, row.ID)
		}
		found := []RunRow{}
		err := run.WithContext(ctx).
			Select(run.ConversationID, run.Status, run.FinishedAt, run.CreatedAt).
			Where(run.ConversationID.In(ids...), run.Source.Neq(RunSourceTitle)).
			Where(s.aiHistoryOwnerConditions(ctx, run.TableName())...).
			Scan(&found)
		if err != nil {
			return nil, dbError(err)
		}
		for _, row := range found {
			state := result[row.ConversationID]
			state.count++
			if row.CreatedAt >= state.lastRunAt {
				state.lastRunAt = row.CreatedAt
				state.latest = row.Status
			}
			if row.Status == RunStatusRunning {
				state.running = true
			}
			if row.Status == RunStatusInterrupted && row.FinishedAt == nil {
				state.interrupted = true
			}
			result[row.ConversationID] = state
		}
	}
	return result, nil
}

// SelectConvOverview keeps the rows of one host (assetID), of no host (unlinked) and of one status; empty values
// keep every row.
func SelectConvOverview(rows []ConvOverviewRow, assetID string, unlinked bool, status string) []ConvOverviewRow {
	result := make([]ConvOverviewRow, 0, len(rows))
	for _, row := range rows {
		if unlinked && row.AssetID != "" {
			continue
		}
		if assetID != "" && row.AssetID != assetID {
			continue
		}
		if status != "" && row.Status != status {
			continue
		}
		result = append(result, row)
	}
	return result
}

// ConvInteractionEvents is the confirmation and question history of one conversation: the requests its runs raised
// and how each was resolved, in the order they happened.
func (s *Store) ConvInteractionEvents(ctx context.Context, conversationID string) ([]RunEventRow, error) {
	if _, err := s.ConvGet(ctx, conversationID); err != nil {
		return nil, err
	}
	runs, err := s.RunList(ctx, conversationID, 0)
	if err != nil {
		return nil, err
	}
	result := []RunEventRow{}
	event := query.Use(s.db).RunEventRow
	for start := 0; start < len(runs); start += convOverviewChunk {
		end := min(start+convOverviewChunk, len(runs))
		ids := make([]string, 0, end-start)
		for _, run := range runs[start:end] {
			ids = append(ids, run.ID)
		}
		found := []RunEventRow{}
		err := event.WithContext(ctx).
			Where(event.RunID.In(ids...), event.Type.In("confirmRequired", "questionRequired", "interactionResolved")).
			Where(s.aiHistoryOwnerConditions(ctx, event.TableName())...).
			Scan(&found)
		if err != nil {
			return nil, dbError(err)
		}
		result = append(result, found...)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].CreatedAt != result[j].CreatedAt {
			return result[i].CreatedAt < result[j].CreatedAt
		}
		if result[i].RunID != result[j].RunID {
			return result[i].RunID < result[j].RunID
		}
		return result[i].Seq < result[j].Seq
	})
	return result, nil
}
