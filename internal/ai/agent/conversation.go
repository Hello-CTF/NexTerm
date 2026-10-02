package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

type ConversationDTO struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Scope     any    `json:"scope"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

type MessageDTO struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversationId"`
	Role           string `json:"role"`
	Content        any    `json:"content"`
	TokensIn       *int64 `json:"tokensIn"`
	TokensOut      *int64 `json:"tokensOut"`
	CreatedAt      int64  `json:"createdAt"`
}

func AutoTitle(message string) string {
	line := ""
	for _, candidate := range strings.FieldsFunc(message, func(r rune) bool { return r == '\r' || r == '\n' }) {
		if strings.TrimSpace(candidate) != "" {
			line = candidate
			break
		}
	}
	line = strings.Join(strings.Fields(line), " ")
	runes := []rune(line)
	if len(runes) > 24 {
		return string(runes[:24]) + "…"
	}
	if line == "" {
		return "新对话"
	}
	return line
}

func conversationDTO(row store.ConversationRow) ConversationDTO {
	var scope any
	if json.Unmarshal([]byte(row.ScopeJSON), &scope) != nil {
		scope = map[string]any{}
	}
	return ConversationDTO{ID: row.ID, Title: row.Title, Scope: scope, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func messageDTO(row store.MessageRow) MessageDTO {
	var content any
	if json.Unmarshal([]byte(row.ContentJSON), &content) != nil {
		content = map[string]any{}
	}
	return MessageDTO{ID: row.ID, ConversationID: row.ConversationID, Role: row.Role, Content: content, TokensIn: row.TokensIn, TokensOut: row.TokensOut, CreatedAt: row.CreatedAt}
}

func (r *Runner) CreateConversation(ctx context.Context, title string, scope any) (ConversationDTO, error) {
	if strings.TrimSpace(title) == "" {
		title = "新对话"
	}
	row, err := r.store.ConvCreate(ctx, title, map[string]any{"scope": scope})
	return conversationDTO(row), err
}

func (r *Runner) Conversations(ctx context.Context) ([]ConversationDTO, error) {
	rows, err := r.store.ConvList(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]ConversationDTO, len(rows))
	for i, row := range rows {
		result[i] = conversationDTO(row)
	}
	return result, nil
}

func (r *Runner) DeleteConversation(ctx context.Context, id string) error {
	return r.store.ConvDelete(ctx, id)
}

func (r *Runner) Messages(ctx context.Context, conversationID string) ([]MessageDTO, error) {
	rows, err := r.store.MsgList(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	result := make([]MessageDTO, len(rows))
	for i, row := range rows {
		result[i] = messageDTO(row)
	}
	return result, nil
}
