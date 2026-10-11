package store

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

func (s *Store) ConvRenameIfTitle(ctx context.Context, id, expectedTitle, title string) (bool, error) {
	conversation := query.Use(s.db).ConversationRow
	info, err := conversation.WithContext(ctx).
		Where(conversation.ID.Eq(id), conversation.Title.Eq(expectedTitle)).
		Where(ownerConditions(ctx, conversation.TableName())...).
		UpdateColumns(map[string]any{"title": title, "updated_at": ids.NowMS()})
	if err != nil {
		return false, dbError(err)
	}
	return info.RowsAffected > 0, nil
}
