package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
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

const titleMaxRunes = 20

var titleGenerationTimeout = 15 * time.Second

var conversationTitleInFlight sync.Map

func titlePrompt(message string) string {
	runes := []rune(message)
	if len(runes) > 500 {
		message = string(runes[:500])
	}
	return "用不超过20个字概括以下用户消息的主题，作为会话标题。只输出标题文本本身，不要输出任何其他内容：\n" + message
}

func normalizeTitle(content string) string {
	line := ""
	for _, candidate := range strings.FieldsFunc(content, func(r rune) bool { return r == '\r' || r == '\n' }) {
		if strings.TrimSpace(candidate) != "" {
			line = candidate
			break
		}
	}
	line = strings.TrimSpace(line)
	line = strings.Trim(line, "\"'`“”‘’「」『』")
	line = strings.Join(strings.Fields(line), " ")
	runes := []rune(line)
	if len(runes) > titleMaxRunes {
		return string(runes[:titleMaxRunes])
	}
	return line
}

func (r *Runner) maybeGenerateConversationTitle(current *job) {
	if r.config.Model == nil || r.store == nil || r.runs == nil {
		return
	}
	conversationID := current.args.ConversationID
	message := strings.TrimSpace(current.args.Message)
	if conversationID == "" || message == "" {
		return
	}
	if _, loaded := conversationTitleInFlight.LoadOrStore(conversationID, struct{}{}); loaded {
		return
	}
	profileID := current.args.ModelProfileID
	factory := r.config.Model
	if profileID != "" && r.config.ModelForProfile != nil {
		factory = func(ctx context.Context) (model.BaseChatModel, uint64, error) {
			return r.config.ModelForProfile(ctx, profileID)
		}
	}
	go func() {
		defer conversationTitleInFlight.Delete(conversationID)
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Warn("conversation title generation panicked", "error", recovered)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), titleGenerationTimeout)
		defer cancel()
		r.generateConversationTitle(ctx, factory, conversationID, message, profileID)
	}()
}

// conditionalTitleRenamer is implemented by stores that support an atomic
// compare-and-swap rename; it keeps a user rename that lands while the title model is
// running from being overwritten by the generated title.
type conditionalTitleRenamer interface {
	ConvRenameIfTitle(ctx context.Context, id, expectedTitle, title string) (bool, error)
}

func (r *Runner) generateConversationTitle(ctx context.Context, factory ModelFactory, conversationID, message, profileID string) {
	row, err := r.store.ConvGet(ctx, conversationID)
	if err != nil || row.Title != AutoTitle(message) {
		return
	}
	started := time.Now()
	title, tokensIn, tokensOut, err := generateTitle(ctx, factory, message)
	if err != nil || title == "" {
		return
	}
	if renamer, ok := r.store.(conditionalTitleRenamer); ok {
		renamed, err := renamer.ConvRenameIfTitle(ctx, conversationID, AutoTitle(message), title)
		if err != nil || !renamed {
			return
		}
	} else {
		row, err := r.store.ConvGet(ctx, conversationID)
		if err != nil || row.Title != AutoTitle(message) {
			return
		}
		if err := r.store.ConvRename(ctx, conversationID, title); err != nil {
			return
		}
	}
	r.recordTitleUsage(ctx, conversationID, profileID, title, tokensIn, tokensOut, time.Since(started).Milliseconds())
}

func generateTitle(ctx context.Context, factory ModelFactory, message string) (string, int64, int64, error) {
	chatModel, _, err := factory(ctx)
	if err != nil {
		return "", 0, 0, err
	}
	response, err := chatModel.Generate(ctx, []*schema.Message{schema.UserMessage(titlePrompt(message))})
	if err != nil {
		return "", 0, 0, err
	}
	if response == nil {
		return "", 0, 0, errors.New("标题生成为空响应")
	}
	var tokensIn, tokensOut int64
	if response.ResponseMeta != nil && response.ResponseMeta.Usage != nil {
		tokensIn = int64(max(response.ResponseMeta.Usage.PromptTokens, 0))
		tokensOut = int64(max(response.ResponseMeta.Usage.CompletionTokens, 0))
	}
	return normalizeTitle(response.Content), tokensIn, tokensOut, nil
}

func (r *Runner) recordTitleUsage(ctx context.Context, conversationID, profileID, title string, tokensIn, tokensOut, latencyMS int64) {
	runID := r.config.NewID()
	if strings.TrimSpace(runID) == "" {
		return
	}
	finished := time.Now().UnixMilli()
	_ = r.runs.RunInsert(ctx, store.RunRow{
		ID: runID, ConversationID: conversationID, Status: store.RunStatusCompleted,
		Source: store.RunSourceTitle, ProfileID: profileID, Answer: title, Turns: 1,
		TokensIn: tokensIn, TokensOut: tokensOut, LatencyMS: latencyMS, FinishedAt: &finished,
	})
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
