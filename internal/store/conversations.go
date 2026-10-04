package store

import (
	"context"
	"encoding/json"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func marshalJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", ipc.WrapError(ipc.CodeInternal, "内部错误: JSON: "+err.Error(), err)
	}
	return string(encoded), nil
}

func scanConversation(row rowScanner) (ConversationRow, error) {
	var result ConversationRow
	err := row.Scan(&result.ID, &result.Title, &result.ScopeJSON, &result.CreatedAt, &result.UpdatedAt)
	return result, err
}

func (s *Store) ConvCreate(ctx context.Context, title string, scope any) (ConversationRow, error) {
	scopeJSON, err := marshalJSON(scope)
	if err != nil {
		return ConversationRow{}, err
	}
	id := ids.New()
	now := ids.NowMS()
	_, err = s.db.ExecContext(ctx, `INSERT INTO ai_conversation(id, title, scope_json, created_at, updated_at)
VALUES(?,?,?,?,?)`, id, title, scopeJSON, now, now)
	if err != nil {
		return ConversationRow{}, dbError(err)
	}
	return s.ConvGet(ctx, id)
}

func (s *Store) ConvGet(ctx context.Context, id string) (ConversationRow, error) {
	row, err := scanConversation(s.db.QueryRowContext(ctx,
		"SELECT id, title, scope_json, created_at, updated_at FROM ai_conversation WHERE id = ?", id))
	if isNoRows(err) {
		return ConversationRow{}, notFound("会话 " + id)
	}
	if err != nil {
		return ConversationRow{}, dbError(err)
	}
	return row, nil
}

func (s *Store) ConvList(ctx context.Context) ([]ConversationRow, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, title, scope_json, created_at, updated_at FROM ai_conversation ORDER BY updated_at DESC")
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []ConversationRow{}
	for rows.Next() {
		row, err := scanConversation(rows)
		if err != nil {
			return nil, dbError(err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) ConvRename(ctx context.Context, id, title string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE ai_conversation SET title=?, updated_at=? WHERE id=?", title, ids.NowMS(), id)
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) ConvTouch(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE ai_conversation SET updated_at=? WHERE id=?", ids.NowMS(), id)
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) ConvDelete(ctx context.Context, id string) (returnErr error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() {
		if returnErr != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, "DELETE FROM ai_message WHERE conversation_id = ?", id); err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM ai_conversation WHERE id = ?", id); err != nil {
		return dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) MsgInsert(ctx context.Context, conversationID, role string, content any, tokensIn, tokensOut *int64) error {
	contentJSON, err := marshalJSON(content)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO ai_message(id, conversation_id, role, content_json, tokens_in, tokens_out, created_at, seq)
VALUES(?,?,?,?,?,?,?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM ai_message WHERE conversation_id = ?))`,
		ids.New(), conversationID, role, contentJSON, tokensIn, tokensOut, ids.NowMS(), conversationID)
	if err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE ai_conversation SET updated_at=? WHERE id=?", ids.NowMS(), conversationID); err != nil {
		return dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) MsgList(ctx context.Context, conversationID string) ([]MessageRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, conversation_id, role, content_json, tokens_in, tokens_out, created_at
FROM ai_message WHERE conversation_id = ? ORDER BY seq`, conversationID)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []MessageRow{}
	for rows.Next() {
		var row MessageRow
		if err := rows.Scan(&row.ID, &row.ConversationID, &row.Role, &row.ContentJSON,
			&row.TokensIn, &row.TokensOut, &row.CreatedAt); err != nil {
			return nil, dbError(err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
