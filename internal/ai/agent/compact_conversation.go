package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// historyTypeSummary rows hold a summary written by /compact. History is read
// from the latest summary on, so earlier messages no longer reach the model.
const historyTypeSummary = "summary"

var (
	ErrConversationBusy      = ipc.NewError(ipc.CodeForbidden, "这段对话还有任务在运行或等待确认，结束后再压缩")
	ErrNothingToCompact      = ipc.NewError(ipc.CodeBadParam, "对话内容太少，不需要压缩")
	errCompactNoConversation = ipc.NewError(ipc.CodeBadParam, "请先开始一段对话再压缩")
)

type CompactResult struct {
	// Messages is how many history messages the summary replaced.
	Messages int `json:"messages"`
}

// CompactConversation folds the conversation's history into one persisted
// summary using the active model, so later runs start from the summary
// instead of the full history.
func (r *Runner) CompactConversation(ctx context.Context, conversationID string) (CompactResult, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return CompactResult{}, errCompactNoConversation
	}
	row, err := r.store.ConvGet(ctx, conversationID)
	if err != nil {
		return CompactResult{}, err
	}
	executionCtx := conversationExecutionContext(ctx, row.OwnerID)
	unlock := r.lockConversation(conversationID)
	defer unlock()
	if r.conversationHasJob(conversationID) {
		return CompactResult{}, ErrConversationBusy
	}
	rows, err := r.store.MsgList(executionCtx, conversationID)
	if err != nil {
		return CompactResult{}, err
	}
	_, history := splitSystemPrefix(historyMessages(rows, ""))
	if len(history) < 2 {
		return CompactResult{}, ErrNothingToCompact
	}
	if r.config.Model == nil {
		return CompactResult{}, errors.New("AI 模型未配置")
	}
	chatModel, window, err := r.config.Model(executionCtx)
	if err != nil {
		return CompactResult{}, err
	}
	if window == 0 {
		window = 262144
	}
	handler := newCompactionHandler(chatModel, window, nil)
	summaryCtx, cancel := context.WithTimeout(executionCtx, handler.summaryCallTimeout())
	defer cancel()
	summary, err := handler.summarizeAll(summaryCtx, history)
	if err != nil {
		return CompactResult{}, err
	}
	summaryRow := map[string]any{"type": historyTypeSummary, "summary": summary, "messages": len(history)}
	if err := r.store.MsgInsert(executionCtx, conversationID, historyTypeSummary, summaryRow, nil, nil); err != nil {
		return CompactResult{}, err
	}
	return CompactResult{Messages: len(history)}, nil
}

// conversationHasJob reports a run of the conversation that is running or
// parked on a confirmation.
func (r *Runner) conversationHasJob(conversationID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, current := range r.jobs {
		if current.args.ConversationID == conversationID {
			return true
		}
	}
	return false
}
