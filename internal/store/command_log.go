package store

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
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
