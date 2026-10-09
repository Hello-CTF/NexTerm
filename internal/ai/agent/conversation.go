package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"

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

const (
	retitleContextLimit   = 8
	retitleContextRunes   = 200
	retitlePrefixMaxRunes = 16
)

func splitTitlePrefix(title string) (prefix, topic string) {
	if strings.HasPrefix(title, "【") {
		if idx := strings.Index(title, "】"); idx >= 0 {
			return cutTitleTopic(title, idx+len("】"))
		}
	}
	if idx := strings.IndexAny(title, ":："); idx > 0 {
		sepLen := 1
		if strings.HasPrefix(title[idx:], "：") {
			sepLen = len("：")
		}
		after := []rune(title[idx+sepLen:])
		before := []rune(title[:idx])
		// 数字夹冒号(12:30 这类时间)不是前缀分隔符。
		if len(before) > 0 && len(after) > 0 && unicode.IsDigit(before[len(before)-1]) && unicode.IsDigit(after[0]) {
			return "", title
		}
		if len([]rune(title[:idx+sepLen])) <= retitlePrefixMaxRunes {
			return cutTitleTopic(title, idx+sepLen)
		}
	}
	return "", title
}

func cutTitleTopic(title string, restStart int) (string, string) {
	trimmed := strings.TrimSpace(title[restStart:])
	return title[:len(title)-len(trimmed)], trimmed
}

func retitleContextLines(rows []store.MessageRow) []string {
	lines := make([]string, 0, retitleContextLimit)
	for _, row := range rows {
		if row.Role != "user" && row.Role != "assistant" {
			continue
		}
		var envelope struct {
			Content string `json:"content"`
		}
		if json.Unmarshal([]byte(row.ContentJSON), &envelope) != nil {
			continue
		}
		text := strings.Join(strings.Fields(envelope.Content), " ")
		if text == "" {
			continue
		}
		if runes := []rune(text); len(runes) > retitleContextRunes {
			text = string(runes[:retitleContextRunes]) + "…"
		}
		lines = append(lines, row.Role+": "+text)
	}
	if len(lines) > retitleContextLimit {
		lines = lines[len(lines)-retitleContextLimit:]
	}
	return lines
}

func retitlePrompt(currentTitle, prefix string, context []string) string {
	var b strings.Builder
	b.WriteString("以下是某个 AI 会话的现有标题与最近对话内容。\n现有标题：")
	b.WriteString(currentTitle)
	b.WriteString("\n")
	if len(context) > 0 {
		b.WriteString("最近对话：\n")
		for _, line := range context {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	if prefix == "" {
		b.WriteString("请用不超过20个字概括这个会话的主题，作为新标题。只输出标题文本本身，不要输出任何其他内容。")
	} else {
		fmt.Fprintf(&b, "%q 是标题的分组前缀。请只为此会话概括一个新的主题部分（不超过20个字），输出时不要包含前缀，也不要输出任何其他内容。", prefix)
	}
	return b.String()
}

func (r *Runner) RetitleConversation(ctx context.Context, conversationID string) (string, error) {
	if r.config.Model == nil || r.store == nil {
		return "", errors.New("标题生成未配置")
	}
	row, err := r.store.ConvGet(ctx, conversationID)
	if err != nil {
		return "", err
	}
	rows, err := r.store.MsgList(ctx, conversationID)
	if err != nil {
		return "", err
	}
	prefix, _ := splitTitlePrefix(row.Title)
	ctx, cancel := context.WithTimeout(ctx, titleGenerationTimeout)
	defer cancel()
	started := time.Now()
	topic, tokensIn, tokensOut, err := generateTitle(ctx, r.config.Model, retitlePrompt(row.Title, prefix, retitleContextLines(rows)))
	if err != nil {
		return "", err
	}
	if topic == "" {
		return "", errors.New("标题生成为空响应")
	}
	title := prefix + topic
	if err := r.store.ConvRename(ctx, conversationID, title); err != nil {
		return "", err
	}
	if r.runs != nil {
		profileID := ""
		if r.config.Profiles != nil {
			if active, ok := r.config.Profiles.ActiveProfile(); ok {
				profileID = active.ID
			}
		}
		r.recordTitleUsage(ctx, conversationID, profileID, title, tokensIn, tokensOut, time.Since(started).Milliseconds())
	}
	return title, nil
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
