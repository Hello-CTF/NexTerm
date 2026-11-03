package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gen/field"
)

type CommandLogRow = model.CommandLog

type CommandLogInput struct {
	SessionID  string
	TabID      string
	AssetID    string
	UserID     string
	Command    string
	Source     string
	ExitCode   *int
	StartedAt  int64
	FinishedAt int64
}

type CommandLogQuery struct {
	SessionID *string
	AssetID   *string
	UserID    *string
	Limit     int64
	Offset    int64
}

func (s *Store) CommandLogInsert(ctx context.Context, input CommandLogInput) error {
	var userID *string
	if input.UserID != "" {
		userID = &input.UserID
	}
	var exitCode *int32
	if input.ExitCode != nil {
		code := int32(*input.ExitCode)
		exitCode = &code
	}
	row := CommandLogRow{
		SessionID: input.SessionID, TabID: input.TabID, AssetID: input.AssetID, UserID: userID,
		Command: input.Command, Source: input.Source, ExitCode: exitCode, StartedAt: input.StartedAt, FinishedAt: input.FinishedAt,
	}
	if err := query.Use(s.db).CommandLog.WithContext(ctx).Create(&row); err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) CommandLogCount(ctx context.Context, filter CommandLogQuery) (int64, error) {
	command := query.Use(s.db).CommandLog
	q := command.WithContext(ctx)
	if filter.SessionID != nil {
		q = q.Where(command.SessionID.Eq(*filter.SessionID))
	}
	if filter.AssetID != nil {
		q = q.Where(command.AssetID.Eq(*filter.AssetID))
	}
	if filter.UserID != nil {
		q = q.Where(command.UserID.Eq(*filter.UserID))
	}
	count, err := q.Count()
	if err != nil {
		return 0, dbError(err)
	}
	return count, nil
}

func (s *Store) CommandLogQuery(ctx context.Context, filter CommandLogQuery) ([]CommandLogRow, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	command := query.Use(s.db).CommandLog
	q := command.WithContext(ctx)
	if filter.SessionID != nil {
		q = q.Where(command.SessionID.Eq(*filter.SessionID))
	}
	if filter.AssetID != nil {
		q = q.Where(command.AssetID.Eq(*filter.AssetID))
	}
	if filter.UserID != nil {
		q = q.Where(command.UserID.Eq(*filter.UserID))
	}
	result := []CommandLogRow{}
	err := q.Order(command.StartedAt.Desc(), command.ID.Desc()).
		Limit(int(limit)).
		Offset(int(filter.Offset)).
		Scan(&result)
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

type CommandLogSearch struct {
	Text        string
	AssetID     *string
	StartedFrom *int64
	StartedTo   *int64
	Limit       int64
	Offset      int64
}

type CommandLogSearchRow struct {
	CommandLogRow
	TranscriptID *string
}

func escapeLike(text string) string {
	return strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(text)
}

// CommandLogSearch matches command text with LIKE only, so every backend
// behaves the same where it matters: ASCII matching is case-insensitive, the
// exact original text always matches, and % _ ! and backslash match literally
// ("!" is the escape because backslash differs between MySQL and PostgreSQL
// literals). Non-ASCII case folding and MySQL accent folding vary by backend;
// that can only add hits, never drop an exact one.
func (s *Store) CommandLogSearch(ctx context.Context, filter CommandLogSearch) ([]CommandLogSearchRow, bool, error) {
	if len(filter.Text) > 256 {
		return nil, false, badParam(fmt.Errorf("command search text is too long"))
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	command := query.Use(s.db).CommandLog
	q := command.WithContext(ctx)
	if owner := ipc.OwnerID(ctx); owner != "" {
		q = q.Where(command.UserID.Eq(owner))
	}
	if filter.Text != "" {
		q = q.Where(field.NewUnsafeFieldRaw(
			"(LOWER(command) LIKE ? ESCAPE '!' OR command LIKE ? ESCAPE '!')",
			"%"+escapeLike(strings.ToLower(filter.Text))+"%", "%"+escapeLike(filter.Text)+"%"))
	}
	if filter.AssetID != nil {
		q = q.Where(command.AssetID.Eq(*filter.AssetID))
	}
	if filter.StartedFrom != nil {
		q = q.Where(command.StartedAt.Gte(*filter.StartedFrom))
	}
	if filter.StartedTo != nil {
		q = q.Where(command.StartedAt.Lt(*filter.StartedTo))
	}
	var found []CommandLogRow
	if err := q.Order(command.StartedAt.Desc(), command.ID.Desc()).
		Limit(int(limit) + 1).
		Offset(int(filter.Offset)).
		Scan(&found); err != nil {
		return nil, false, dbError(err)
	}
	hasMore := int64(len(found)) > limit
	if hasMore {
		found = found[:limit]
	}
	result := make([]CommandLogSearchRow, 0, len(found))
	for _, row := range found {
		result = append(result, CommandLogSearchRow{CommandLogRow: row})
	}
	if err := s.attachCommandTranscripts(ctx, result); err != nil {
		return nil, false, err
	}
	return result, hasMore, nil
}

// attachCommandTranscripts links each command to the newest transcript of its
// session that started at or before the command, falling back to the
// session's earliest transcript.
func (s *Store) attachCommandTranscripts(ctx context.Context, rows []CommandLogSearchRow) error {
	if len(rows) == 0 {
		return nil
	}
	seen := map[string]bool{}
	sessionIDs := []string{}
	for _, row := range rows {
		if !seen[row.SessionID] {
			seen[row.SessionID] = true
			sessionIDs = append(sessionIDs, row.SessionID)
		}
	}
	transcript := query.Use(s.db).TranscriptRow
	var transcripts []TranscriptRow
	if err := transcript.WithContext(ctx).
		Where(transcript.SessionID.In(sessionIDs...)).
		Where(ownerConditions(ctx, transcript.TableName())...).
		Order(transcript.StartedAt, transcript.ID).
		Scan(&transcripts); err != nil {
		return dbError(err)
	}
	bySession := map[string][]TranscriptRow{}
	for _, row := range transcripts {
		bySession[row.SessionID] = append(bySession[row.SessionID], row)
	}
	for i := range rows {
		list := bySession[rows[i].SessionID]
		if len(list) == 0 {
			continue
		}
		pick := list[0]
		for _, candidate := range list {
			if candidate.StartedAt <= rows[i].StartedAt {
				pick = candidate
			}
		}
		id := pick.ID
		rows[i].TranscriptID = &id
	}
	return nil
}
