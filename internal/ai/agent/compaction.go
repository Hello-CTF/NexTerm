package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const compactionTriggerPercent = 70

const durableCompactionReserve = 2048

const defaultSummaryTimeout = 90 * time.Second

func compactionTriggerTokens(window uint64) int {
	return int(window * compactionTriggerPercent / 100)
}

func estimateHistoryTokens(messages []*schema.Message) int {
	return estimatedMessages(messages) / 3
}

type compactionHandler struct {
	*adk.BaseChatModelAgentMiddleware
	model          model.BaseChatModel
	window         uint64
	summaryTimeout time.Duration
	onCompact      func()
	onUsage        func(*schema.Message, time.Duration) error
}

func newCompactionHandler(chatModel model.BaseChatModel, window uint64, onCompact func(), onUsage ...func(*schema.Message, time.Duration) error) *compactionHandler {
	handler := &compactionHandler{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}, model: chatModel, window: window, summaryTimeout: defaultSummaryTimeout, onCompact: onCompact}
	if len(onUsage) > 0 {
		handler.onUsage = onUsage[0]
	}
	return handler
}

func (h *compactionHandler) generate(ctx context.Context, input []*schema.Message) (*schema.Message, error) {
	started := time.Now()
	response, err := h.model.Generate(ctx, input)
	if h.onUsage != nil && response != nil {
		if usageErr := h.onUsage(response, time.Since(started)); err == nil {
			err = usageErr
		}
	}
	return response, err
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
	summaryCtx, cancel := context.WithTimeout(ctx, h.summaryCallTimeout())
	defer cancel()
	system, contextMsgs := splitSystemPrefix(messages)
	summary, err := h.summarizeAll(summaryCtx, contextMsgs)
	if err != nil {
		return nil, err
	}
	return h.finalize(system, contextMsgs, summary, retainedReserve), nil
}

func (h *compactionHandler) summaryCallTimeout() time.Duration {
	if h.summaryTimeout <= 0 {
		return defaultSummaryTimeout
	}
	return h.summaryTimeout
}

func (h *compactionHandler) summarizeAll(ctx context.Context, contextMsgs []*schema.Message) (string, error) {
	contextMsgs = provider.ProjectModelMessages(contextMsgs)
	chunks := chunkMessagesForSummary(contextMsgs, h.summaryChunkPayloadBytes())
	texts := make([]string, 0, len(chunks))
	for index, chunk := range chunks {
		input := make([]*schema.Message, 0, len(chunk)+2)
		input = append(input, schema.SystemMessage(summarySystemInstruction()))
		input = append(input, chunk...)
		input = append(input, schema.UserMessage(historySummaryInstruction()))
		response, err := h.generate(ctx, input)
		if err != nil {
			return "", err
		}
		text, err := summaryResponseText(response)
		if err != nil {
			return "", err
		}
		texts = append(texts, fmt.Sprintf("[Segment %d/%d]\n%s", index+1, len(chunks), text))
	}
	return h.mergeSummaries(ctx, texts)
}

func (h *compactionHandler) mergeSummaries(ctx context.Context, texts []string) (string, error) {
	combined := strings.Join(texts, "\n\n")
	for depth := 0; len(combined) > h.summaryChunkBytes() && depth < 8; depth++ {
		groups := packSummaryGroups(texts, max(h.summaryChunkBytes()-len(summarySystemInstruction())-len(summaryMergeInstruction())-128, 256))
		merged := make([]string, 0, len(groups))
		for _, group := range groups {
			response, err := h.generate(ctx, []*schema.Message{
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
		combined += "\n[Summary truncated for length]"
	}
	return combined, nil
}

func (h *compactionHandler) finalize(system, contextMsgs []*schema.Message, summary string, retainedReserve int) []*schema.Message {
	summaryMessage := schema.UserMessage("[History Summary]\n" + summary)
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

// historySummaryInstruction asks for a handoff summary: the model that
// continues sees only this summary and the newest messages, so it must keep
// everything needed to carry on without rediscovering it.
func historySummaryInstruction() string {
	return "Summarize the conversation above for the assistant that will continue it. That assistant will see only " +
		"this summary and the newest messages, so make the summary self-sufficient. Write it in the language the " +
		"conversation uses and keep it proportional: a short exchange needs a few lines, a long multi-step task needs detail. Preserve:\n" +
		"1. The user's latest request and what it actually asks for, including ambiguity already resolved. Quote a long " +
		"request's actual ask, since long messages may be cut. If several requests are open, say which one governs the next step.\n" +
		"2. Constraints and preferences still in force. Keep decisions already settled apart from questions still open.\n" +
		"3. What was actually done, at high fidelity: the exact commands, the hosts, containers, sessions and working " +
		"directories they ran in, the file paths touched, whether each step succeeded or failed, and the results themselves " +
		"(key output values, error lines). Keep only the final working version of any change; drop resolved errors.\n" +
		"4. What is still unknown or unverified. If a step claimed success that was never checked, say it is unverified.\n" +
		"5. The plan forward: the exact next command or action, the remaining steps, decisions already made for them, and " +
		"obstacles you can foresee.\n" +
		"Do not add pleasantries or repeat long outputs; keep identifiers and key values exact."
}

func summarySystemInstruction() string {
	return "You write handoff summaries of operations sessions. Output only the summary itself, without pleasantries or restating these instructions."
}

func summaryMergeInstruction() string {
	return "The following summary segments come from consecutive parts of the same conversation. Merge them into one " +
		"summary in the conversation's language, keeping every key fact (the latest request, constraints, commands and " +
		"their results, paths, unverified claims, the plan forward) and removing duplicates:"
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
	var units [][]*schema.Message
	for _, unit := range summaryUnits(messages) {
		if estimatedMessages(unit) > limit {
			for _, segment := range summaryUnitSegments(unit, limit) {
				units = append(units, []*schema.Message{segment})
			}
			continue
		}
		units = append(units, unit)
	}
	var chunks [][]*schema.Message
	current := []*schema.Message{}
	currentBytes := 0
	for _, unit := range units {
		unitBytes := estimatedMessages(unit)
		if currentBytes+unitBytes > limit && len(current) > 0 {
			chunks = append(chunks, current)
			current = []*schema.Message{}
			currentBytes = 0
		}
		current = append(current, unit...)
		currentBytes += unitBytes
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

func summaryUnits(messages []*schema.Message) [][]*schema.Message {
	var units [][]*schema.Message
	for index := 0; index < len(messages); index++ {
		message := messages[index]
		if message.Role == schema.Assistant && len(message.ToolCalls) > 0 {
			unit := []*schema.Message{message}
			for index+1 < len(messages) && messages[index+1].Role == schema.Tool {
				index++
				unit = append(unit, messages[index])
			}
			units = append(units, unit)
			continue
		}
		if message.Role == schema.Tool {
			units = append(units, []*schema.Message{schema.UserMessage(fmt.Sprintf("[Tool Result] %s(%s)\n%s", message.ToolName, message.ToolCallID, message.Content))})
			continue
		}
		units = append(units, []*schema.Message{message})
	}
	return units
}

func summaryUnitSegments(unit []*schema.Message, limit int) []*schema.Message {
	var builder strings.Builder
	for _, message := range unit {
		builder.WriteString(messageSummaryText(message))
	}
	role := schema.User
	if unit[0].Role == schema.Assistant {
		role = schema.Assistant
	}
	pieceLimit := max(limit-len("[Single-Message Segment]")-16, 64)
	pieces := splitTextForSummary(builder.String(), pieceLimit)
	segments := make([]*schema.Message, 0, len(pieces))
	for index, piece := range pieces {
		content := fmt.Sprintf("[Single-Message Segment %d/%d]\n%s", index+1, len(pieces), piece)
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
	if message.Role == schema.Tool {
		fmt.Fprintf(&builder, "[Tool Result] %s(%s)\n", message.ToolName, message.ToolCallID)
	}
	if message.ReasoningContent != "" {
		builder.WriteString("[Reasoning]\n")
		builder.WriteString(message.ReasoningContent)
		builder.WriteString("\n")
	}
	for _, call := range message.ToolCalls {
		fmt.Fprintf(&builder, "[Tool Call] %s(%s): %s\n", call.Function.Name, call.ID, call.Function.Arguments)
	}
	for _, part := range message.UserInputMultiContent {
		if part.Type == schema.ChatMessagePartTypeText {
			builder.WriteString(part.Text)
			builder.WriteString("\n")
			continue
		}
		builder.WriteString("[Non-Text Content]\n")
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
			builder.WriteString("[Non-Text Content]\n")
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
	flush := func() {
		if len(current) > 0 {
			groups = append(groups, current)
			current = []string{}
			currentBytes = 0
		}
	}
	for _, text := range texts {
		for _, piece := range splitTextForSummary(text, limit) {
			if currentBytes+len(piece) > limit && len(current) > 0 {
				flush()
			}
			current = append(current, piece)
			currentBytes += len(piece)
		}
	}
	flush()
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
			if message.Role == schema.Tool {
				target := len(message.Content) - remaining
				if target < 0 {
					target = 0
				}
				if content, ok := truncateToolResultJSON(message.Content, target); ok {
					copied.Content = content
					remaining -= len(message.Content) - len(copied.Content)
					result[index] = &copied
					continue
				}
			}
			keep := len(copied.Content) - remaining - len("[History Truncated]") - 1
			if keep < 0 {
				keep = 0
			}
			text, _ := prefixBytes(copied.Content, keep)
			copied.Content = text + "\n[History Truncated]"
			remaining -= len(message.Content) - len(copied.Content)
			result[index] = &copied
		}
	}
	return result
}

func truncateToolResultJSON(content string, budget int) (string, bool) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &envelope); err != nil || envelope == nil {
		return "", false
	}
	var text string
	if raw, exists := envelope["text"]; !exists || json.Unmarshal(raw, &text) != nil {
		return "", false
	}
	var modelText string
	hasModelText := false
	if raw, exists := envelope["modelText"]; exists {
		if json.Unmarshal(raw, &modelText) != nil {
			return "", false
		}
		hasModelText = true
	}
	build := func(keep int) (string, error) {
		display, _ := prefixBytes(text, keep)
		encodedText, err := json.Marshal(display + "\n[History Truncated]")
		if err != nil {
			return "", err
		}
		envelope["text"] = encodedText
		if hasModelText {
			model, _ := prefixBytes(modelText, keep)
			encodedModelText, err := json.Marshal(model + "\n[History Truncated]")
			if err != nil {
				return "", err
			}
			envelope["modelText"] = encodedModelText
		}
		encoded, err := json.Marshal(envelope)
		return string(encoded), err
	}
	if budget < 0 {
		budget = 0
	}
	best, err := build(0)
	if err != nil {
		return "", false
	}
	low, high := 0, len(text)
	for low <= high {
		keep := low + (high-low)/2
		candidate, err := build(keep)
		if err != nil {
			return "", false
		}
		if len(candidate) <= budget {
			best = candidate
			low = keep + 1
		} else {
			high = keep - 1
		}
	}
	return best, true
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
