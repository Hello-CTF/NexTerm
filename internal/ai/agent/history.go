package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/store"
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
	ModelText string          `json:"modelText,omitempty"`
	Summary   string          `json:"summary"`
	Truncated bool            `json:"truncated"`
	ExitCode  int             `json:"exitCode"`
	Panic     bool            `json:"panic"`
	Path      string          `json:"path"`
	Before    string          `json:"before"`
	After     string          `json:"after"`
	Plan      string          `json:"plan"`
}

// persistToolCallRows saves one assistant turn's tool calls. Text the model
// wrote alongside the calls is saved first as an interim assistant row, and
// rebuildHistory joins it with the calls into one assistant message.
func (r *Runner) persistToolCallRows(ctx context.Context, current *job, text string, calls []schema.ToolCall) error {
	conversationID, jobID := current.args.ConversationID, current.id
	if text != "" {
		interim := map[string]any{"role": "assistant", "content": text, "jobId": jobID, "interim": true}
		if err := r.store.MsgInsert(ctx, conversationID, "assistant", interim, nil, nil); err != nil {
			return err
		}
	}
	// The turn's text is saved, so it is no longer a partial answer.
	current.resetStreamed()
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
	r.recordToolResult(current, text, result.ExecFailed)
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
	if result.ModelText != "" {
		row["modelText"] = result.ModelText
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

// parsedHistoryRow is one stored row decoded for history rebuilding.
type parsedHistoryRow struct {
	historyRow
	Interim     bool   `json:"interim"`
	Partial     bool   `json:"partial"`
	BeforeJobID string `json:"beforeJobId"`
}

// partialAnswerNote follows a partial answer in the model input so the model
// knows the user saw an answer that was cut off; the stored text stays as
// streamed.
const partialAnswerNote = "\n\n[answer interrupted]"

// orderHistoryRows keeps each run's rows together, in the order the runs
// started. Two runs that wrote into one conversation at the same time
// interleaved their rows; regrouping them pairs every tool call with its own
// result. A final answer saved without a run ID stays with the run whose row
// came before it, and an automatic summary goes in front of the run it was
// made for, replacing exactly the history before that run.
func orderHistoryRows(rows []store.MessageRow) []parsedHistoryRow {
	type block struct{ rows []parsedHistoryRow }
	var blocks []*block
	byJob := map[string]*block{}
	var last *block
	for _, row := range rows {
		var persisted parsedHistoryRow
		if json.Unmarshal([]byte(row.ContentJSON), &persisted) != nil {
			continue
		}
		switch {
		case persisted.JobID != "":
			current := byJob[persisted.JobID]
			if current == nil {
				current = &block{}
				blocks = append(blocks, current)
				byJob[persisted.JobID] = current
			}
			current.rows = append(current.rows, persisted)
			last = current
		case persisted.Type == historyTypeSummary && persisted.BeforeJobID != "" && byJob[persisted.BeforeJobID] != nil:
			target := byJob[persisted.BeforeJobID]
			index := slices.Index(blocks, target)
			blocks = slices.Insert(blocks, index, &block{rows: []parsedHistoryRow{persisted}})
		case persisted.Type == "" && persisted.Role == "assistant" && last != nil:
			last.rows = append(last.rows, persisted)
		default:
			current := &block{rows: []parsedHistoryRow{persisted}}
			blocks = append(blocks, current)
			last = current
		}
	}
	ordered := make([]parsedHistoryRow, 0, len(rows))
	for _, current := range blocks {
		ordered = append(ordered, current.rows...)
	}
	return ordered
}

func rebuildHistory(rows []store.MessageRow, jobID string) []*schema.Message {
	messages := make([]*schema.Message, 0, len(rows))
	carrying := false
	groupCalls := map[string]struct{}{}
	groupAnswered := map[string]struct{}{}
	groupToolEnd := -1
	for _, persisted := range orderHistoryRows(rows) {
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
			envelope, err := json.Marshal(tools.Output{OK: persisted.OK, Text: persisted.Text, ModelText: persisted.ModelText, ExitCode: persisted.ExitCode, Truncated: persisted.Truncated, Panic: persisted.Panic})
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
		case historyTypeSummary:
			// A summary replaces everything before it.
			if persisted.Summary == "" {
				continue
			}
			messages = append(messages[:0], schema.UserMessage("[History Summary]\n"+persisted.Summary))
			carrying = false
			groupCalls = map[string]struct{}{}
			groupAnswered = map[string]struct{}{}
			groupToolEnd = -1
		default:
			if persisted.Content == "" {
				continue
			}
			switch persisted.Role {
			case "user":
				messages = append(messages, schema.UserMessage(persisted.Content))
			case "assistant":
				content := persisted.Content
				if persisted.Partial {
					if strings.TrimSpace(content) == "" {
						continue
					}
					content += partialAnswerNote
				}
				messages = append(messages, schema.AssistantMessage(content, nil))
				if persisted.Interim {
					// The turn's tool calls follow and join this message.
					carrying = true
					groupCalls = map[string]struct{}{}
					groupAnswered = map[string]struct{}{}
					groupToolEnd = len(messages) - 1
					continue
				}
			default:
				continue
			}
			carrying = false
		}
	}
	return messages
}

func interruptedToolResult(callID string) *schema.Message {
	envelope, _ := json.Marshal(tools.Output{
		OK:        false,
		Text:      "工具调用未完成：本轮在返回结果前已中断，操作可能已执行，也可能未执行",
		ModelText: "Tool call did not complete: the run was interrupted before a result came back. The action may or may not have taken effect; check the current state before relying on it.",
	})
	return schema.ToolMessage(string(envelope), callID)
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
		// A call left without a result (the run was canceled or failed mid-tool) is answered with an
		// interrupted result. Dropping it instead would leave an assistant message with neither content nor
		// tool calls, which providers reject, and would hide that the action may already have taken effect.
		repaired = append(repaired, message)
		for _, call := range message.ToolCalls {
			if result, ok := answered[call.ID]; ok {
				repaired = append(repaired, result)
				continue
			}
			unansweredCalls++
			repaired = append(repaired, interruptedToolResult(call.ID))
		}
		index = end
	}
	return repaired, orphanResults, unansweredCalls
}
