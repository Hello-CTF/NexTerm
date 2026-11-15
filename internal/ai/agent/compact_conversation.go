package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ai/privacy"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// historyTypeSummary rows hold a summary written by /compact or by an
// automatic summary at run start. History is read from the latest summary on,
// so earlier messages no longer reach the model.
const historyTypeSummary = "summary"

var (
	ErrNothingToCompact      = ipc.NewError(ipc.CodeBadParam, "对话内容过少，无需压缩")
	errCompactNoConversation = ipc.NewError(ipc.CodeBadParam, "请先开始对话，再进行压缩")
	ErrCompactHistoryChanged = ipc.NewError(CodeConversationBusy, "压缩期间对话有了新消息，摘要未保存，请重试")
)

type CompactResult struct {
	// Messages is how many history messages the summary replaced.
	Messages int `json:"messages"`
}

// CompactConversation folds the conversation's history into one persisted
// summary using the active model, so later runs start from the summary
// instead of the full history. The conversation stays unlocked while the
// model summarizes; the summary is saved only when no run started and no
// message arrived meanwhile.
func (r *Runner) CompactConversation(ctx context.Context, conversationID string) (CompactResult, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return CompactResult{}, errCompactNoConversation
	}
	row, err := r.store.ConvGet(ctx, conversationID)
	if err != nil {
		return CompactResult{}, err
	}
	executionCtx := privacy.WithConversation(conversationExecutionContext(ctx, row.OwnerID), conversationID)
	rows, err := r.compactSnapshot(executionCtx, conversationID)
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
		window = defaultContextWindow
	}
	handler := newCompactionHandler(chatModel, window, nil)
	summaryCtx, cancel := context.WithTimeout(executionCtx, handler.summaryCallTimeout())
	defer cancel()
	summary, err := handler.summarizeAll(summaryCtx, history)
	if err != nil {
		return CompactResult{}, err
	}
	unlock := r.lockConversation(conversationID)
	defer unlock()
	current, err := r.compactCheck(executionCtx, conversationID)
	if err != nil {
		return CompactResult{}, err
	}
	if !sameHistoryHead(rows, current) {
		return CompactResult{}, ErrCompactHistoryChanged
	}
	summaryRow := map[string]any{"type": historyTypeSummary, "summary": summary, "messages": len(history)}
	if err := r.store.MsgInsert(executionCtx, conversationID, historyTypeSummary, summaryRow, nil, nil); err != nil {
		return CompactResult{}, err
	}
	return CompactResult{Messages: len(history)}, nil
}

func (r *Runner) compactSnapshot(ctx context.Context, conversationID string) ([]store.MessageRow, error) {
	unlock := r.lockConversation(conversationID)
	defer unlock()
	return r.compactCheck(ctx, conversationID)
}

// compactCheck refuses while the conversation has an active run and returns
// its rows. Callers hold the conversation lock.
func (r *Runner) compactCheck(ctx context.Context, conversationID string) ([]store.MessageRow, error) {
	busy, err := r.conversationBusy(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	if busy {
		return nil, ErrConversationBusy
	}
	return r.store.MsgList(ctx, conversationID)
}

func sameHistoryHead(before, after []store.MessageRow) bool {
	if len(before) != len(after) {
		return false
	}
	return len(before) == 0 || before[len(before)-1].ID == after[len(after)-1].ID
}
