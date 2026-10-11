package store

import (
	"context"
	"encoding/json"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func marshalJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", ipc.WrapError(ipc.CodeInternal, "数据编码失败: "+err.Error(), err)
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
	_, err = s.db.ExecContext(ctx, `INSERT INTO ai_conversation(owner_id, id, title, scope_json, created_at, updated_at)
VALUES(?,?,?,?,?,?)`, ipc.OwnerID(ctx), id, title, scopeJSON, now, now)
	if err != nil {
		return ConversationRow{}, dbError(err)
	}
	return s.ConvGet(ctx, id)
}

func (s *Store) ConvGet(ctx context.Context, id string) (ConversationRow, error) {
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	row, err := scanConversation(s.db.QueryRowContext(ctx,
		"SELECT id, title, scope_json, created_at, updated_at FROM ai_conversation WHERE id = ?"+condition, append([]any{id}, args...)...))
	if isNoRows(err) {
		return ConversationRow{}, notFound("会话")
	}
	if err != nil {
		return ConversationRow{}, dbError(err)
	}
	return row, nil
}

func (s *Store) ConvList(ctx context.Context) ([]ConversationRow, error) {
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, title, scope_json, created_at, updated_at FROM ai_conversation WHERE 1=1"+condition+" ORDER BY updated_at DESC", args...)
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
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	_, err := s.db.ExecContext(ctx, "UPDATE ai_conversation SET title=?, updated_at=? WHERE id = ?"+condition, append([]any{title, ids.NowMS(), id}, args...)...)
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) ConvDelete(ctx context.Context, id string) (returnErr error) {
	if _, err := s.ConvGet(ctx, id); err != nil {
		return err
	}
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() {
		if returnErr != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, "DELETE FROM ai_message WHERE conversation_id = ?"+condition, append([]any{id}, args...)...); err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM cron_job WHERE session_id = ?"+condition, append([]any{id}, args...)...); err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM ai_conversation WHERE id = ?"+condition, append([]any{id}, args...)...); err != nil {
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
	if _, err := s.ConvGet(ctx, conversationID); err != nil {
		return err
	}
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	insertArgs := []any{ipc.OwnerID(ctx), ids.New(), conversationID, role, contentJSON, tokensIn, tokensOut, ids.NowMS(), conversationID}
	insertArgs = append(insertArgs, args...)
	_, err = tx.ExecContext(ctx, `INSERT INTO ai_message(owner_id, id, conversation_id, role, content_json, tokens_in, tokens_out, created_at, seq)
VALUES(?,?,?,?,?,?,?,?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM ai_message WHERE conversation_id = ?`+condition+`))`, insertArgs...)
	if err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE ai_conversation SET updated_at=? WHERE id = ?"+condition, append([]any{ids.NowMS(), conversationID}, args...)...); err != nil {
		return dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) MsgTruncateAfter(ctx context.Context, conversationID, messageID string) (returnErr error) {
	if _, err := s.ConvGet(ctx, conversationID); err != nil {
		return err
	}
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() {
		if returnErr != nil {
			_ = tx.Rollback()
		}
	}()
	var seq int64
	err = tx.QueryRowContext(ctx, `SELECT seq FROM ai_message WHERE id = ? AND conversation_id = ?`+condition, append([]any{messageID, conversationID}, args...)...).Scan(&seq)
	if isNoRows(err) {
		return notFound("消息")
	}
	if err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ai_message WHERE conversation_id = ? AND seq > ?`+condition, append([]any{conversationID, seq}, args...)...); err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE ai_conversation SET updated_at=? WHERE id = ?"+condition, append([]any{ids.NowMS(), conversationID}, args...)...); err != nil {
		return dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) MsgList(ctx context.Context, conversationID string) ([]MessageRow, error) {
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	rows, err := s.db.QueryContext(ctx, `SELECT id, conversation_id, role, content_json, tokens_in, tokens_out, created_at
FROM ai_message WHERE conversation_id = ?`+condition+` ORDER BY seq`, append([]any{conversationID}, args...)...)
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
