package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const compactionTriggerPercent = 70

const durableCompactionReserve = 2048

func compactionTriggerTokens(window uint64) int {
	return int(window * compactionTriggerPercent / 100)
}

func estimateHistoryTokens(messages []*schema.Message) int {
	return estimatedMessages(messages) / 3
}

type compactionHandler struct {
	*adk.BaseChatModelAgentMiddleware
	model     model.BaseChatModel
	window    uint64
	onCompact func()
}

func newCompactionHandler(chatModel model.BaseChatModel, window uint64, onCompact func()) *compactionHandler {
	return &compactionHandler{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}, model: chatModel, window: window, onCompact: onCompact}
}

func (h *compactionHandler) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	messages := state.Messages
	summarized := false
	if h.model != nil && estimateHistoryTokens(messages) > compactionTriggerTokens(h.window) {
		finalized, err := h.summarize(ctx, messages, 0)
		switch {
		case err == nil:
			messages = finalized
			summarized = true
		case ctx.Err() != nil:
			return ctx, nil, err
		}
	}
	fitted, compacted, err := fitMessageBudget(messages, h.window)
	if err != nil {
		return ctx, nil, err
	}
	if !summarized && !compacted {
		return ctx, state, nil
	}
	if h.onCompact != nil {
		h.onCompact()
	}
	final := *state
	final.Messages = fitted
	return ctx, &final, nil
}

func (h *compactionHandler) compactHistory(ctx context.Context, messages []*schema.Message) ([]*schema.Message, error) {
	if h.model != nil && estimateHistoryTokens(messages) > compactionTriggerTokens(h.window) {
		finalized, err := h.summarize(ctx, messages, durableCompactionReserve)
		switch {
		case err == nil:
			if h.onCompact != nil {
				h.onCompact()
			}
			return finalized, nil
		case ctx.Err() != nil:
			return nil, err
		}
	}
	fitted, compacted := fitHistoryBudget(messages, h.window)
	if compacted && h.onCompact != nil {
		h.onCompact()
	}
	return fitted, nil
}

func (h *compactionHandler) summarize(ctx context.Context, messages []*schema.Message, retainedReserve int) ([]*schema.Message, error) {
	system, contextMsgs := splitSystemPrefix(messages)
	summary, err := h.summarizeAll(ctx, contextMsgs)
	if err != nil {
		return nil, err
	}
	return h.finalize(system, contextMsgs, summary, retainedReserve), nil
}

func (h *compactionHandler) summarizeAll(ctx context.Context, contextMsgs []*schema.Message) (string, error) {
	chunks := chunkMessagesForSummary(contextMsgs, h.summaryChunkPayloadBytes())
	texts := make([]string, 0, len(chunks))
	for index, chunk := range chunks {
		input := make([]*schema.Message, 0, len(chunk)+2)
		input = append(input, schema.SystemMessage(summarySystemInstruction()))
		input = append(input, chunk...)
		input = append(input, schema.UserMessage(historySummaryInstruction()))
		response, err := h.model.Generate(ctx, input)
		if err != nil {
			return "", err
		}
		text, err := summaryResponseText(response)
		if err != nil {
			return "", err
		}
		texts = append(texts, fmt.Sprintf("【第 %d/%d 段】\n%s", index+1, len(chunks), text))
	}
	return h.mergeSummaries(ctx, texts)
}

func (h *compactionHandler) mergeSummaries(ctx context.Context, texts []string) (string, error) {
	combined := strings.Join(texts, "\n\n")
	for depth := 0; len(combined) > h.summaryChunkBytes() && len(texts) > 1 && depth < 4; depth++ {
		groups := packSummaryGroups(texts, h.summaryChunkBytes()-len(summaryMergeInstruction())-128)
		merged := make([]string, 0, len(groups))
		for _, group := range groups {
			response, err := h.model.Generate(ctx, []*schema.Message{
				schema.SystemMessage(summarySystemInstruction()),
				schema.UserMessage(summaryMergeInstruction() + "\n\n" + strings.Join(group, "\n\n")),
			})
			if err != nil {
				return "", err
			}
			text, err := summaryResponseText(response)
			if err != nil {
				return "", err
			}
			merged = append(merged, text)
		}
		texts = merged
		combined = strings.Join(texts, "\n\n")
	}
	if len(combined) > h.summaryChunkBytes() {
		combined, _ = prefixBytes(combined, h.summaryChunkBytes())
		combined += "\n[摘要过长已截断]"
	}
	return combined, nil
}

func (h *compactionHandler) finalize(system, contextMsgs []*schema.Message, summary string, retainedReserve int) []*schema.Message {
	summaryMessage := schema.UserMessage("[历史摘要]\n" + summary)
	retained := retainRecentUnit(contextMsgs)
	if retainedReserve > 0 {
		budget := compactionTriggerTokens(h.window)*3 - estimatedMessages(system) - estimatedMessages([]*schema.Message{summaryMessage}) - retainedReserve
		if budget <= 0 {
			retained = nil
		} else {
			retained = truncateContentsToBudget(retained, budget)
		}
	}
	result := make([]*schema.Message, 0, len(system)+1+len(retained))
	result = append(result, system...)
	result = append(result, summaryMessage)
	result = append(result, retained...)
	return result
}

func (h *compactionHandler) summaryChunkBytes() int {
	return max(int(h.window*3)/2, 512)
}

func (h *compactionHandler) summaryChunkPayloadBytes() int {
	return max(h.summaryChunkBytes()-512, 256)
}

func summaryResponseText(response *schema.Message) (string, error) {
	text := strings.TrimSpace(summaryText(response))
	if text == "" {
		return "", errors.New("历史摘要内容为空")
	}
	return text, nil
}

func historySummaryInstruction() string {
	return "请把以上这段对话历史压缩成一份中文摘要。必须保留：1) 用户的关键要求、约束与偏好；2) 执行过的命令、工作目录、主机/容器/会话标识；3) 每个工具调用的结论（成功或失败、关键输出、报错与修复过程）；4) 涉及的文件路径与修改要点；5) 尚未完成的步骤与下一步计划。用分点短句书写，不要复述寒暄与重复输出。"
}

func summarySystemInstruction() string {
	return "你是运维会话历史压缩器。只输出摘要正文，不要寒暄，不要复述指令。"
}

func summaryMergeInstruction() string {
	return "以下多段摘要来自同一段会话历史的相邻片段，请合并为一份连续的中文摘要，保留全部关键事实（命令、路径、工具结论、未完成步骤），去掉重复内容："
}

func splitSystemPrefix(messages []*schema.Message) ([]*schema.Message, []*schema.Message) {
	var system []*schema.Message
	rest := messages
	for len(rest) > 0 && rest[0].Role == schema.System {
		system = append(system, rest[0])
		rest = rest[1:]
	}
	return system, rest
}

func chunkMessagesForSummary(messages []*schema.Message, limit int) [][]*schema.Message {
	expanded := make([]*schema.Message, 0, len(messages))
	for _, message := range messages {
		if estimatedMessages([]*schema.Message{message}) > limit {
			expanded = append(expanded, messagesForSummary(message, limit)...)
			continue
		}
		expanded = append(expanded, message)
	}
	var chunks [][]*schema.Message
	current := []*schema.Message{}
	currentBytes := 0
	for _, message := range expanded {
		size := estimatedMessages([]*schema.Message{message})
		if currentBytes+size > limit && len(current) > 0 {
			chunks = append(chunks, current)
			current = []*schema.Message{}
			currentBytes = 0
		}
		current = append(current, message)
		currentBytes += size
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

func messagesForSummary(message *schema.Message, limit int) []*schema.Message {
	if estimatedMessages([]*schema.Message{message}) <= limit {
		return []*schema.Message{message}
	}
	text := messageSummaryText(message)
	role := message.Role
	if role != schema.User && role != schema.Assistant {
		role = schema.User
	}
	pieceLimit := max(limit-len("[单条历史分段]")-16, 64)
	pieces := splitTextForSummary(text, pieceLimit)
	segments := make([]*schema.Message, 0, len(pieces))
	for index, piece := range pieces {
		content := fmt.Sprintf("[单条历史分段 %d/%d]\n%s", index+1, len(pieces), piece)
		if role == schema.Assistant {
			segments = append(segments, schema.AssistantMessage(content, nil))
			continue
		}
		segments = append(segments, schema.UserMessage(content))
	}
	return segments
}

func messageSummaryText(message *schema.Message) string {
	var builder strings.Builder
	if message.ReasoningContent != "" {
		builder.WriteString("[推理]\n")
		builder.WriteString(message.ReasoningContent)
		builder.WriteString("\n")
	}
	for _, call := range message.ToolCalls {
		fmt.Fprintf(&builder, "[工具调用] %s(%s): %s\n", call.Function.Name, call.ID, call.Function.Arguments)
	}
	for _, part := range message.UserInputMultiContent {
		if part.Type == schema.ChatMessagePartTypeText {
			builder.WriteString(part.Text)
			builder.WriteString("\n")
			continue
		}
		builder.WriteString("[非文本内容]\n")
	}
	for _, part := range message.AssistantGenMultiContent {
		switch part.Type {
		case schema.ChatMessagePartTypeText:
			builder.WriteString(part.Text)
			builder.WriteString("\n")
		case schema.ChatMessagePartTypeReasoning:
			if part.Reasoning != nil {
				builder.WriteString(part.Reasoning.Text)
				builder.WriteString("\n")
			}
		default:
			builder.WriteString("[非文本内容]\n")
		}
	}
	if message.Content != "" {
		builder.WriteString(message.Content)
		builder.WriteString("\n")
	}
	return builder.String()
}

func splitTextForSummary(text string, limit int) []string {
	var pieces []string
	for len(text) > limit {
		piece, _ := prefixBytes(text, limit)
		pieces = append(pieces, piece)
		text = text[len(piece):]
	}
	if len(text) > 0 {
		pieces = append(pieces, text)
	}
	return pieces
}

func packSummaryGroups(texts []string, limit int) [][]string {
	var groups [][]string
	current := []string{}
	currentBytes := 0
	for _, text := range texts {
		if len(text) > limit {
			text, _ = prefixBytes(text, limit)
			text += "…"
		}
		if currentBytes+len(text) > limit && len(current) > 0 {
			groups = append(groups, current)
			current = []string{}
			currentBytes = 0
		}
		current = append(current, text)
		currentBytes += len(text)
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups
}

func fitHistoryBudget(messages []*schema.Message, window uint64) ([]*schema.Message, bool) {
	if window == 0 || estimatedMessages(messages) <= int(window*3) {
		return messages, false
	}
	limit := int(window * 3)
	working := append([]*schema.Message(nil), messages...)
	compacted := false
	for estimatedMessages(working) > limit && len(working) > 1 {
		var dropped bool
		working, dropped = dropOldestMessageUnit(working)
		if !dropped {
			break
		}
		compacted = true
	}
	if estimatedMessages(working) > limit {
		working = truncateContentsToBudget(working, limit)
		compacted = true
	}
	return working, compacted
}

func truncateContentsToBudget(messages []*schema.Message, budget int) []*schema.Message {
	if estimatedMessages(messages) <= budget {
		return messages
	}
	result := append([]*schema.Message(nil), messages...)
	remaining := estimatedMessages(result) - budget
	for _, systemPass := range []bool{false, true} {
		for index, message := range result {
			if remaining <= 0 {
				return result
			}
			if (message.Role == schema.System) != systemPass {
				continue
			}
			copied := *message
			keep := len(copied.Content) - remaining - len("[历史已截断]") - 1
			if keep < 0 {
				keep = 0
			}
			text, _ := prefixBytes(copied.Content, keep)
			copied.Content = text + "\n[历史已截断]"
			remaining -= len(message.Content) - len(copied.Content)
			result[index] = &copied
		}
	}
	return result
}

func summaryText(message *schema.Message) string {
	if message == nil {
		return ""
	}
	var parts []string
	for _, part := range message.AssistantGenMultiContent {
		if part.Type == schema.ChatMessagePartTypeText && part.Text != "" {
			parts = append(parts, part.Text)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return message.Content
}

func retainRecentUnit(messages []*schema.Message) []*schema.Message {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == schema.User {
			return messages[index:]
		}
	}
	return nil
}
