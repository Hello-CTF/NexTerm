package store

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

func (s *Store) RecordingStart(ctx context.Context, sessionID, tabID, path string) (string, error) {
	id := ids.New()
	row := RecordingRow{ID: id, SessionID: sessionID, TabID: tabID, Path: path, StartedAt: ids.NowMS(), OwnerID: ipc.OwnerID(ctx)}
	if err := query.Use(s.db).RecordingRow.WithContext(ctx).Create(&row); err != nil {
		return "", dbError(err)
	}
	return id, nil
}

func (s *Store) RecordingUpdateBytes(ctx context.Context, id string, bytes int64) error {
	recording := query.Use(s.db).RecordingRow
	_, err := recording.WithContext(ctx).
		Where(recording.ID.Eq(id)).
		Where(ownerConditions(ctx, recording.TableName())...).
		UpdateColumns(map[string]any{"bytes": bytes})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) RecordingEnd(ctx context.Context, id string) error {
	recording := query.Use(s.db).RecordingRow
	_, err := recording.WithContext(ctx).
		Where(recording.ID.Eq(id)).
		Where(ownerConditions(ctx, recording.TableName())...).
		UpdateColumns(map[string]any{"ended_at": ids.NowMS()})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) RecordingList(ctx context.Context) ([]RecordingRow, error) {
	recording := query.Use(s.db).RecordingRow
	result := []RecordingRow{}
	err := recording.WithContext(ctx).
		Where(ownerConditions(ctx, recording.TableName())...).
		Order(recording.StartedAt.Desc()).
		Limit(200).
		Scan(&result)
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}
