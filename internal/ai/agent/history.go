package agent

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/schema"
)

const (
	historyTypeToolCall      = "toolCall"
	historyTypeToolResult    = "toolResult"
	historyTypeFileChange    = "fileChange"
	historyTypePlanSubmitted = "planSubmitted"
)

const persistedToolResultLimit = 64 << 10

type historyRow struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	JobID     string          `json:"jobId"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Args      json.RawMessage `json:"args"`
	Tool      string          `json:"tool"`
	OK        bool            `json:"ok"`
	Text      string          `json:"text"`
	Summary   string          `json:"summary"`
	Truncated bool            `json:"truncated"`
	ExitCode  int             `json:"exitCode"`
	Panic     bool            `json:"panic"`
	Path      string          `json:"path"`
	Before    string          `json:"before"`
	After     string          `json:"after"`
	Plan      string          `json:"plan"`
}

func (r *Runner) persistToolCallRows(ctx context.Context, conversationID, jobID string, calls []schema.ToolCall) error {
	for _, call := range calls {
		row := map[string]any{
			"type":  historyTypeToolCall,
			"jobId": jobID,
			"id":    call.ID,
			"name":  call.Function.Name,
			"args":  rawObject(json.RawMessage(call.Function.Arguments)),
		}
		if err := r.store.MsgInsert(ctx, conversationID, historyTypeToolCall, row, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) persistToolResultRows(ctx context.Context, current *job, message *schema.Message, result tools.Output, text string) error {
	if result.Change != nil {
		change := map[string]any{
			"type":   historyTypeFileChange,
			"jobId":  current.id,
			"id":     result.Change.ID,
			"path":   result.Change.Path,
			"before": result.Change.Before,
			"after":  result.Change.After,
		}
		if err := r.store.MsgInsert(ctx, current.args.ConversationID, historyTypeFileChange, change, nil, nil); err != nil {
			return err
		}
	}
	row := map[string]any{
		"type":      historyTypeToolResult,
		"jobId":     current.id,
		"id":        message.ToolCallID,
		"tool":      message.ToolName,
		"ok":        result.OK,
		"text":      text,
		"summary":   summarize(result.Text),
		"truncated": result.Truncated,
		"exitCode":  result.ExitCode,
		"panic":     result.Panic,
	}
	if err := r.store.MsgInsert(ctx, current.args.ConversationID, historyTypeToolResult, row, nil, nil); err != nil {
		return err
	}
	if result.Plan != "" && current.args.PlanMode {
		plan := map[string]any{"type": historyTypePlanSubmitted, "jobId": current.id, "plan": result.Plan}
		if err := r.store.MsgInsert(ctx, current.args.ConversationID, historyTypePlanSubmitted, plan, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

func historyMessages(rows []store.MessageRow, jobID string) []*schema.Message {
	messages := rebuildHistory(rows, jobID)
	repaired, orphanResults, unansweredCalls := repairHistory(messages)
	if orphanResults > 0 || unansweredCalls > 0 {
		conversationID := ""
		if len(rows) > 0 {
			conversationID = rows[0].ConversationID
		}
		slog.Warn("修复 AI 工具历史配对", "conversation", conversationID, "job", jobID, "orphanResults", orphanResults, "unansweredCalls", unansweredCalls)
	}
	return repaired
}

func rebuildHistory(rows []store.MessageRow, jobID string) []*schema.Message {
	messages := make([]*schema.Message, 0, len(rows))
	carrying := false
	groupCalls := map[string]struct{}{}
	groupAnswered := map[string]struct{}{}
	groupToolEnd := -1
	for _, row := range rows {
		var persisted historyRow
		if json.Unmarshal([]byte(row.ContentJSON), &persisted) != nil {
			continue
		}
		if persisted.JobID != "" && persisted.JobID == jobID {
			continue
		}
		switch persisted.Type {
		case historyTypeToolCall:
			if persisted.ID == "" {
				continue
			}
			call := schema.ToolCall{ID: persisted.ID, Type: "function", Function: schema.FunctionCall{Name: persisted.Name, Arguments: string(persisted.Args)}}
			if carrying {
				last := messages[len(messages)-1]
				last.ToolCalls = append(last.ToolCalls, call)
				groupCalls[persisted.ID] = struct{}{}
				continue
			}
			messages = append(messages, schema.AssistantMessage("", []schema.ToolCall{call}))
			carrying = true
			groupCalls = map[string]struct{}{persisted.ID: {}}
			groupAnswered = map[string]struct{}{}
			groupToolEnd = len(messages) - 1
		case historyTypeToolResult:
			envelope, err := json.Marshal(tools.Output{OK: persisted.OK, Text: persisted.Text, ExitCode: persisted.ExitCode, Truncated: persisted.Truncated, Panic: persisted.Panic})
			if err != nil {
				continue
			}
			message := schema.ToolMessage(string(envelope), persisted.ID)
			if _, ok := groupCalls[persisted.ID]; !ok {
				messages = append(messages, message)
				carrying = false
				continue
			}
			if _, dup := groupAnswered[persisted.ID]; dup {
				messages = append(messages, message)
				carrying = false
				continue
			}
			groupAnswered[persisted.ID] = struct{}{}
			messages = append(messages, nil)
			copy(messages[groupToolEnd+2:], messages[groupToolEnd+1:])
			messages[groupToolEnd+1] = message
			groupToolEnd++
			carrying = false
		case historyTypeFileChange, historyTypePlanSubmitted:
			continue
		default:
			if persisted.Content == "" {
				continue
			}
			role := persisted.Role
			if role == "" {
				role = row.Role
			}
			switch role {
			case "user":
				messages = append(messages, schema.UserMessage(persisted.Content))
			case "assistant":
				messages = append(messages, schema.AssistantMessage(persisted.Content, nil))
			default:
				continue
			}
			carrying = false
		}
	}
	return messages
}

func repairHistory(messages []*schema.Message) ([]*schema.Message, int, int) {
	orphanResults := 0
	unansweredCalls := 0
	repaired := make([]*schema.Message, 0, len(messages))
	for index := 0; index < len(messages); {
		message := messages[index]
		if message.Role == schema.Tool {
			orphanResults++
			index++
			continue
		}
		if message.Role != schema.Assistant || len(message.ToolCalls) == 0 {
			repaired = append(repaired, message)
			index++
			continue
		}
		end := index + 1
		for end < len(messages) && messages[end].Role == schema.Tool {
			end++
		}
		wanted := make(map[string]struct{}, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			wanted[call.ID] = struct{}{}
		}
		answered := make(map[string]*schema.Message, len(wanted))
		for _, candidate := range messages[index+1 : end] {
			if _, ok := wanted[candidate.ToolCallID]; !ok {
				orphanResults++
				continue
			}
			if _, dup := answered[candidate.ToolCallID]; dup {
				orphanResults++
				continue
			}
			answered[candidate.ToolCallID] = candidate
		}
		remaining := make([]schema.ToolCall, 0, len(answered))
		for _, call := range message.ToolCalls {
			if _, ok := answered[call.ID]; ok {
				remaining = append(remaining, call)
			}
		}
		unansweredCalls += len(message.ToolCalls) - len(remaining)
		if len(remaining) != len(message.ToolCalls) {
			cloned := *message
			cloned.ToolCalls = remaining
			message = &cloned
		}
		repaired = append(repaired, message)
		for _, call := range remaining {
			repaired = append(repaired, answered[call.ID])
		}
		index = end
	}
	return repaired, orphanResults, unansweredCalls
}
