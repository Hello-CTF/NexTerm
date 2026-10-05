package store

import (
	"context"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

func (s *Store) ConvRenameIfTitle(ctx context.Context, id, expectedTitle, title string) (bool, error) {
	result, err := s.db.ExecContext(ctx, "UPDATE ai_conversation SET title=?, updated_at=? WHERE id=? AND title=?", title, ids.NowMS(), id, expectedTitle)
	if err != nil {
		return false, dbError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, dbError(err)
	}
	return affected > 0, nil
}
