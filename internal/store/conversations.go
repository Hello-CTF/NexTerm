package store

import (
	"context"
	"encoding/json"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
)

func marshalJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", ipc.WrapError(ipc.CodeInternal, "数据编码失败: "+err.Error(), err)
	}
	return string(encoded), nil
}

func (s *Store) ConvCreate(ctx context.Context, title string, scope any) (ConversationRow, error) {
	scopeJSON, err := marshalJSON(scope)
	if err != nil {
		return ConversationRow{}, err
	}
	id := ids.New()
	now := ids.NowMS()
	row := ConversationRow{ID: id, Title: title, ScopeJSON: scopeJSON, CreatedAt: now, UpdatedAt: now, OwnerID: ipc.OwnerID(ctx)}
	if err := query.Use(s.db).ConversationRow.WithContext(ctx).Create(&row); err != nil {
		return ConversationRow{}, dbError(err)
	}
	return s.ConvGet(ctx, id)
}

func (s *Store) ConvGet(ctx context.Context, id string) (ConversationRow, error) {
	conversation := query.Use(s.db).ConversationRow
	row, err := conversation.WithContext(ctx).
		Where(conversation.ID.Eq(id)).
		Where(s.aiHistoryOwnerConditions(ctx, conversation.TableName())...).
		First()
	if isNoRows(err) {
		return ConversationRow{}, notFound("会话")
	}
	if err != nil {
		return ConversationRow{}, dbError(err)
	}
	return *row, nil
}

func (s *Store) ConvList(ctx context.Context) ([]ConversationRow, error) {
	conversation := query.Use(s.db).ConversationRow
	result := []ConversationRow{}
	err := conversation.WithContext(ctx).
		Where(s.aiHistoryOwnerConditions(ctx, conversation.TableName())...).
		Order(conversation.UpdatedAt.Desc()).
		Scan(&result)
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

func (s *Store) ConvRename(ctx context.Context, id, title string) error {
	conversation := query.Use(s.db).ConversationRow
	_, err := conversation.WithContext(ctx).
		Where(conversation.ID.Eq(id)).
		Where(s.aiHistoryOwnerConditions(ctx, conversation.TableName())...).
		UpdateColumns(map[string]any{"title": title, "updated_at": ids.NowMS()})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) ConvDelete(ctx context.Context, id string) error {
	if _, err := s.ConvGet(ctx, id); err != nil {
		return err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := query.Use(tx)
		message := queries.MessageRow
		if _, err := message.WithContext(ctx).
			Where(message.ConversationID.Eq(id)).
			Where(s.aiHistoryOwnerConditions(ctx, message.TableName())...).
			Delete(); err != nil {
			return err
		}
		cron := queries.CronJob
		if _, err := cron.WithContext(ctx).
			Where(cron.SessionID.Eq(id)).
			Where(s.aiHistoryOwnerConditions(ctx, cron.TableName())...).
			Delete(); err != nil {
			return err
		}
		conversation := queries.ConversationRow
		_, err := conversation.WithContext(ctx).
			Where(conversation.ID.Eq(id)).
			Where(s.aiHistoryOwnerConditions(ctx, conversation.TableName())...).
			Delete()
		return err
	})
	if err != nil {
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
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := query.Use(tx)
		message := queries.MessageRow
		q := message.WithContext(ctx).
			Where(message.ConversationID.Eq(conversationID)).
			Where(s.aiHistoryOwnerConditions(ctx, message.TableName())...)
		var maxSeq int64
		if err := q.UnderlyingDB().Select("COALESCE(MAX(seq), 0)").Scan(&maxSeq).Error; err != nil {
			return err
		}
		now := ids.NowMS()
		row := MessageRow{
			ID: ids.New(), ConversationID: conversationID, Role: role, ContentJSON: contentJSON,
			TokensIn: tokensIn, TokensOut: tokensOut, CreatedAt: now, Seq: maxSeq + 1, OwnerID: ipc.OwnerID(ctx),
		}
		if err := message.WithContext(ctx).Create(&row); err != nil {
			return err
		}
		conversation := queries.ConversationRow
		_, err = conversation.WithContext(ctx).
			Where(conversation.ID.Eq(conversationID)).
			Where(s.aiHistoryOwnerConditions(ctx, conversation.TableName())...).
			UpdateColumns(map[string]any{"updated_at": now})
		return err
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) MsgTruncateAfter(ctx context.Context, conversationID, messageID string) error {
	if _, err := s.ConvGet(ctx, conversationID); err != nil {
		return err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := query.Use(tx)
		message := queries.MessageRow
		row, err := message.WithContext(ctx).
			Where(message.ID.Eq(messageID), message.ConversationID.Eq(conversationID)).
			Where(s.aiHistoryOwnerConditions(ctx, message.TableName())...).
			First()
		if isNoRows(err) {
			return notFound("消息")
		}
		if err != nil {
			return err
		}
		if _, err := message.WithContext(ctx).
			Where(message.ConversationID.Eq(conversationID), message.Seq.Gt(row.Seq)).
			Where(s.aiHistoryOwnerConditions(ctx, message.TableName())...).
			Delete(); err != nil {
			return err
		}
		conversation := queries.ConversationRow
		_, err = conversation.WithContext(ctx).
			Where(conversation.ID.Eq(conversationID)).
			Where(s.aiHistoryOwnerConditions(ctx, conversation.TableName())...).
			UpdateColumns(map[string]any{"updated_at": ids.NowMS()})
		return err
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) MsgList(ctx context.Context, conversationID string) ([]MessageRow, error) {
	message := query.Use(s.db).MessageRow
	result := []MessageRow{}
	err := message.WithContext(ctx).
		Where(message.ConversationID.Eq(conversationID)).
		Where(s.aiHistoryOwnerConditions(ctx, message.TableName())...).
		Order(message.Seq).
		Scan(&result)
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}
