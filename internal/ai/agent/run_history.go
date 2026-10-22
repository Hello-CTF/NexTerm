package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// AI-05 run history states. They are the backend-owned projection of the
// store statuses: the UI never derives them from raw statuses itself.
const (
	RunHistoryStateQueued   = "queued"
	RunHistoryStateRunning  = "running"
	RunHistoryStateIdle     = "idle"
	RunHistoryStateDone     = "done"
	RunHistoryStateError    = "error"
	RunHistoryStateCanceled = "canceled"
)

const (
	runHistoryDefaultLimit = 50
	runHistoryMaxLimit     = 200
	// runRecoveryRestoreCap bounds how many of the conversation's newest
	// runs the recovery audit restores; truncated reports the older runs
	// beyond it.
	runRecoveryRestoreCap = 200
)

// RunHistoryEvidence is the durable terminal record behind a finished
// entry's state: the finish timestamp is the observation, and the summary
// carries the run's own answer or error text. Unfinished runs carry no
// evidence instead of a fabricated one.
type RunHistoryEvidence struct {
	Kind       string `json:"kind"`
	ObservedAt int64  `json:"observedAt"`
	Summary    string `json:"summary"`
}

// RunHistoryEntry is one run in the AI-05 history contract.
type RunHistoryEntry struct {
	RunID      string              `json:"runId"`
	State      string              `json:"state"`
	Evidence   *RunHistoryEvidence `json:"evidence"`
	StartedAt  int64               `json:"startedAt"`
	FinishedAt *int64              `json:"finishedAt"`
	Summary    string              `json:"summary"`
}

// RunHistoryPage is one backend page. NextCursor is opaque to the caller
// and is passed back verbatim to fetch the next older page; nil means the
// backend has no more history.
type RunHistoryPage struct {
	Entries    []RunHistoryEntry `json:"entries"`
	NextCursor *string           `json:"nextCursor"`
}

// ConversationRecoverySummary audits the restart-recovery posture of the
// conversation's newest runs: how many were restored, which of them stay
// interrupted, the single newest resumable run, and whether older runs
// exist beyond the restored set.
type ConversationRecoverySummary struct {
	ConversationID    string   `json:"conversationId"`
	RestoredRuns      int      `json:"restoredRuns"`
	ResumableRunID    *string  `json:"resumableRunId"`
	InterruptedRunIDs []string `json:"interruptedRunIds"`
	Truncated         bool     `json:"truncated"`
}

// runHistoryPager is the keyset-paging capability of the run store,
// satisfied by *store.Store.
type runHistoryPager interface {
	RunPage(ctx context.Context, filter store.RunPageFilter) (store.RunPage, error)
}

// RunHistory returns one cursor page of the conversation's run history with
// server-side status filtering. The cursor is opaque: an empty cursor fetches
// the newest page, and a returned cursor fetches the next older one.
func (r *Runner) RunHistory(ctx context.Context, conversationID, filter, cursor string, limit int) (RunHistoryPage, error) {
	if r.runs == nil {
		return RunHistoryPage{}, ipc.NewError(ipc.CodeUnsupported, "AI 会话存储未配置")
	}
	if strings.TrimSpace(conversationID) == "" {
		return RunHistoryPage{}, ipc.NewError(ipc.CodeBadParam, "会话 ID 不能为空")
	}
	statuses, unfinishedOnly, keep, err := runHistoryFilter(filter)
	if err != nil {
		return RunHistoryPage{}, err
	}
	if limit <= 0 {
		limit = runHistoryDefaultLimit
	}
	if limit > runHistoryMaxLimit {
		limit = runHistoryMaxLimit
	}
	page := RunHistoryPage{Entries: []RunHistoryEntry{}}
	// No store status backs "queued": the filter honestly matches nothing.
	if filter == RunHistoryStateQueued {
		return page, nil
	}
	pager, ok := r.runs.(runHistoryPager)
	if !ok {
		return RunHistoryPage{}, ipc.NewError(ipc.CodeUnsupported, "AI 运行存储不支持分页")
	}
	var after *store.RunCursor
	if cursor != "" {
		after, err = decodeRunHistoryCursor(cursor)
		if err != nil {
			return RunHistoryPage{}, ipc.NewError(ipc.CodeBadParam, "运行历史游标无效")
		}
	}
	var lastConsumed *store.RunCursor
	for len(page.Entries) < limit {
		storePage, err := pager.RunPage(ctx, store.RunPageFilter{
			ConversationID: conversationID,
			Statuses:       statuses,
			UnfinishedOnly: unfinishedOnly,
			After:          after,
			Limit:          limit - len(page.Entries),
		})
		if err != nil {
			return RunHistoryPage{}, err
		}
		consumed := 0
		for _, row := range storePage.Runs {
			if len(page.Entries) == limit {
				break
			}
			consumed++
			lastConsumed = &store.RunCursor{CreatedAt: row.CreatedAt, ID: row.ID}
			if keep != nil && !keep(row) {
				continue
			}
			page.Entries = append(page.Entries, runHistoryEntryFromRow(row))
		}
		more := storePage.NextCursor != nil || consumed < len(storePage.Runs)
		if !more {
			return page, nil
		}
		after = storePage.NextCursor
		if after == nil {
			// Rows remain in the last store page past a full result page:
			// resume right after the last consumed row.
			after = lastConsumed
		}
	}
	if lastConsumed == nil {
		return page, nil
	}
	encoded := encodeRunHistoryCursor(*lastConsumed)
	page.NextCursor = &encoded
	return page, nil
}

// ConversationRecoverySummary audits the conversation's newest runs (up to
// the restore cap). A nil summary with a nil error means the conversation
// holds no runs and there is nothing to report.
func (r *Runner) ConversationRecoverySummary(ctx context.Context, conversationID string) (*ConversationRecoverySummary, error) {
	if r.runs == nil {
		return nil, ipc.NewError(ipc.CodeUnsupported, "AI 会话存储未配置")
	}
	if strings.TrimSpace(conversationID) == "" {
		return nil, ipc.NewError(ipc.CodeBadParam, "会话 ID 不能为空")
	}
	pager, ok := r.runs.(runHistoryPager)
	if !ok {
		return nil, ipc.NewError(ipc.CodeUnsupported, "AI 运行存储不支持分页")
	}
	summary := &ConversationRecoverySummary{ConversationID: conversationID, InterruptedRunIDs: []string{}}
	var after *store.RunCursor
	for {
		pageSize := runRecoveryRestoreCap - summary.RestoredRuns
		if pageSize > runRecoveryPageSize {
			pageSize = runRecoveryPageSize
		}
		storePage, err := pager.RunPage(ctx, store.RunPageFilter{
			ConversationID: conversationID,
			After:          after,
			Limit:          pageSize,
		})
		if err != nil {
			return nil, err
		}
		for _, row := range storePage.Runs {
			summary.RestoredRuns++
			if row.Status == store.RunStatusInterrupted && row.FinishedAt == nil {
				summary.InterruptedRunIDs = append(summary.InterruptedRunIDs, row.ID)
				if summary.ResumableRunID == nil && r.runResumable(ctx, row.ID) {
					resumable := row.ID
					summary.ResumableRunID = &resumable
				}
			}
		}
		if summary.RestoredRuns >= runRecoveryRestoreCap {
			summary.Truncated = storePage.NextCursor != nil
			break
		}
		if storePage.NextCursor == nil {
			break
		}
		after = storePage.NextCursor
	}
	if summary.RestoredRuns == 0 {
		return nil, nil
	}
	return summary, nil
}

// runResumable reports whether a restore would actually accept the run: a
// live HITL snapshot with pending requests and no terminal outcome. Without
// the runtime the answer is honestly no.
func (r *Runner) runResumable(ctx context.Context, runID string) bool {
	if r.hitl == nil {
		return false
	}
	snapshot, err := r.hitl.Snapshot(runID)
	if err != nil {
		return false
	}
	return snapshot.Terminal == nil && len(snapshot.Pending) > 0
}

// runHistoryFilter translates the AI-05 status filter into store statuses.
// An empty status list keeps every status; keep is the agent-side refinement
// a single store query cannot express.
func runHistoryFilter(filter string) (statuses []string, unfinishedOnly bool, keep func(store.RunRow) bool, err error) {
	switch filter {
	case "", "all":
		return nil, false, nil, nil
	case RunHistoryStateQueued:
		return nil, false, nil, nil
	case RunHistoryStateRunning:
		return []string{store.RunStatusRunning}, false, nil, nil
	case RunHistoryStateIdle:
		return []string{store.RunStatusInterrupted}, true, nil, nil
	case RunHistoryStateDone:
		return []string{store.RunStatusCompleted}, false, nil, nil
	case RunHistoryStateError:
		return []string{store.RunStatusFailed, store.RunStatusExpired, RunStatusSuperseded, store.RunStatusInterrupted}, false, func(row store.RunRow) bool {
			return row.Status != store.RunStatusInterrupted || row.FinishedAt != nil
		}, nil
	case RunHistoryStateCanceled:
		return []string{store.RunStatusCanceled}, false, nil, nil
	default:
		return nil, false, nil, ipc.NewError(ipc.CodeBadParam, "未知的运行状态过滤")
	}
}

// runHistoryEntryFromRow projects the durable run row onto the AI-05
// contract. Terminal states come only from the row's own finish record.
func runHistoryEntryFromRow(row store.RunRow) RunHistoryEntry {
	state := runHistoryState(row)
	return RunHistoryEntry{
		RunID:      row.ID,
		State:      state,
		Evidence:   runHistoryEvidence(row, state),
		StartedAt:  row.CreatedAt,
		FinishedAt: row.FinishedAt,
		Summary:    runHistorySummary(row, state),
	}
}

func runHistoryState(row store.RunRow) string {
	switch row.Status {
	case store.RunStatusRunning:
		return RunHistoryStateRunning
	case store.RunStatusInterrupted:
		if row.FinishedAt == nil {
			return RunHistoryStateIdle
		}
		return RunHistoryStateError
	case store.RunStatusCompleted:
		return RunHistoryStateDone
	case store.RunStatusCanceled:
		return RunHistoryStateCanceled
	case store.RunStatusFailed, store.RunStatusExpired, RunStatusSuperseded:
		return RunHistoryStateError
	default:
		return RunHistoryStateError
	}
}

func runHistoryEvidence(row store.RunRow, state string) *RunHistoryEvidence {
	if row.FinishedAt == nil {
		return nil
	}
	switch state {
	case RunHistoryStateDone:
		summary := runHistoryExcerpt(row.Answer)
		if summary == "" {
			summary = "已完成"
		}
		return &RunHistoryEvidence{Kind: "output", ObservedAt: *row.FinishedAt, Summary: summary}
	case RunHistoryStateError, RunHistoryStateCanceled:
		return &RunHistoryEvidence{Kind: "exit", ObservedAt: *row.FinishedAt, Summary: runHistoryTerminalText(row)}
	default:
		return nil
	}
}

func runHistorySummary(row store.RunRow, state string) string {
	switch state {
	case RunHistoryStateRunning:
		return "运行中"
	case RunHistoryStateIdle:
		return "中断待恢复"
	case RunHistoryStateDone:
		if summary := runHistoryExcerpt(row.Answer); summary != "" {
			return summary
		}
		return "已完成"
	case RunHistoryStateError, RunHistoryStateCanceled:
		return runHistoryTerminalText(row)
	default:
		return runHistoryTerminalText(row)
	}
}

func runHistoryTerminalText(row store.RunRow) string {
	if text := strings.TrimSpace(row.Error); text != "" {
		return text
	}
	switch row.Status {
	case store.RunStatusFailed:
		return "运行失败"
	case store.RunStatusExpired:
		return "确认已过期"
	case RunStatusSuperseded:
		return "已被更新的运行取代"
	case store.RunStatusInterrupted:
		return "AI 任务已中断"
	case store.RunStatusCanceled:
		return "已取消"
	default:
		return "未知运行状态"
	}
}

const runHistorySummaryRunes = 80

func runHistoryExcerpt(text string) string {
	trimmed := strings.TrimSpace(text)
	runes := []rune(trimmed)
	if len(runes) > runHistorySummaryRunes {
		return string(runes[:runHistorySummaryRunes]) + "…"
	}
	return trimmed
}

func encodeRunHistoryCursor(cursor store.RunCursor) string {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeRunHistoryCursor(cursor string) (*store.RunCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, err
	}
	var parsed store.RunCursor
	if err := json.Unmarshal(decoded, &parsed); err != nil {
		return nil, err
	}
	if parsed.ID == "" {
		return nil, ipc.NewError(ipc.CodeBadParam, "运行历史游标无效")
	}
	return &parsed, nil
}
