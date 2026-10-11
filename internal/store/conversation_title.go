package store

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func (s *Store) ConvRenameIfTitle(ctx context.Context, id, expectedTitle, title string) (bool, error) {
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	result, err := s.db.ExecContext(ctx, "UPDATE ai_conversation SET title=?, updated_at=? WHERE id=? AND title=?"+condition,
		append([]any{title, ids.NowMS(), id, expectedTitle}, args...)...)
	if err != nil {
		return false, dbError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, dbError(err)
	}
	return affected > 0, nil
}
