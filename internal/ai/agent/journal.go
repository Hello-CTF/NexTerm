package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type RunStore interface {
	RunInsert(context.Context, store.RunRow) error
	RunGet(context.Context, string) (store.RunRow, error)
	RunDelete(context.Context, string) error
	RunList(ctx context.Context, conversationID string, limit int) ([]store.RunRow, error)
	RunsActive(context.Context) ([]store.RunRow, error)
	RunAppendEvent(ctx context.Context, runID, eventType string, payload func(seq uint64) ([]byte, error)) (uint64, error)
	RunEventsAfter(ctx context.Context, runID string, afterSeq uint64) ([]store.RunEventRow, error)
	RunEventsAfterLimit(ctx context.Context, runID string, afterSeq uint64, limit int) ([]store.RunEventRow, error)
	RunErrorCounts(ctx context.Context, runIDs []string) (map[string]store.RunErrorCounts, error)
	RunUpdateStatus(ctx context.Context, runID, status string) error
	RunFinish(ctx context.Context, runID, status, answer, errMsg string, turns int, tokensIn, tokensOut int64) error
	RunFinishUsage(ctx context.Context, runID, status, answer, errMsg string, turns int, tokensIn, tokensOut, cacheCreationTokens, latencyMS int64) error
	HitlRunSave(ctx context.Context, id string, data []byte) error
	HitlRunList(ctx context.Context) ([]store.HitlRunRow, error)
	HitlRunDelete(ctx context.Context, id string) error
}

type hitlStoreBridge struct {
	runs RunStore
}

func (b hitlStoreBridge) SaveRun(ctx context.Context, blob hitl.RunBlob) error {
	return b.runs.HitlRunSave(ctx, blob.ID, blob.Data)
}

func (b hitlStoreBridge) ListRuns(ctx context.Context) ([]hitl.RunBlob, error) {
	rows, err := b.runs.HitlRunList(ctx)
	if err != nil {
		return nil, err
	}
	blobs := make([]hitl.RunBlob, len(rows))
	for index, row := range rows {
		blobs[index] = hitl.RunBlob{ID: row.ID, OwnerID: row.OwnerID, Data: row.Data}
	}
	return blobs, nil
}

const (
	journalFlushWindow = 150 * time.Millisecond
	journalFlushBytes  = 32 << 10
	journalRetryMax    = 3 * time.Second
)

func journalBatchable(eventType string) bool {
	switch eventType {
	case "delta", "reasoning", "toolArgs":
		return true
	}
	return false
}

type journalStream struct {
	mu           sync.Mutex
	stream       Stream
	journal      RunStore
	runID        string
	flushContext context.Context
	pending      []Event
	pendingBytes int
	timer        *time.Timer
	retryDelay   time.Duration
	flushWarned  bool
	closed       bool
	prepared     bool
}

func WithRunJournal(stream Stream, journal RunStore, runID string) Stream {
	return &journalStream{stream: stream, journal: journal, runID: runID}
}

func (s *journalStream) Send(ctx context.Context, event Event) error {
	return s.send(ctx, nil, event, nil)
}

// SendTerminal persists the terminal event before updating the run, then
// delivers it to the live stream after the status update.
func (s *journalStream) SendTerminal(ctx, deliveryParent context.Context, event Event, beforeDeliver func()) error {
	return s.send(ctx, deliveryParent, event, beforeDeliver)
}

func (s *journalStream) send(ctx, deliveryParent context.Context, event Event, beforeDeliver func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("AI 事件流已关闭")
	}
	if s.flushContext == nil {
		// Keep the run's owner scope after the request or job is canceled.
		s.flushContext = context.WithoutCancel(ctx)
	} else if ipc.OwnerID(ctx) != ipc.OwnerID(s.flushContext) {
		return errors.New("AI 事件流账号上下文不一致")
	}
	if journalBatchable(event.Type) {
		s.bufferLocked(event)
		if s.pendingBytes >= journalFlushBytes {
			return s.flushLocked(ctx)
		}
		return nil
	}
	if err := s.flushLocked(ctx); err != nil {
		s.bufferLocked(event)
		if beforeDeliver != nil {
			beforeDeliver()
		}
		return s.sendTerminalFallbackLocked(ctx, deliveryParent, event, err)
	}
	journaled, err := s.sendLocked(ctx, deliveryParent, event, beforeDeliver)
	if err != nil && !journaled {
		s.bufferLocked(event)
		if beforeDeliver != nil {
			beforeDeliver()
		}
		return s.sendTerminalFallbackLocked(ctx, deliveryParent, event, err)
	}
	return err
}

func (s *journalStream) bufferLocked(event Event) {
	s.pending = append(s.pending, event)
	s.pendingBytes += journalEventBytes(event)
	s.scheduleFlushLocked()
}

func (s *journalStream) scheduleFlushLocked() {
	if s.closed || s.timer != nil || len(s.pending) == 0 {
		return
	}
	delay := journalFlushWindow
	if s.retryDelay > 0 {
		delay = s.retryDelay
	}
	s.timer = time.AfterFunc(delay, s.flushTimeout)
}

type terminalFallbackError struct{ cause error }

func (e *terminalFallbackError) Error() string { return e.cause.Error() }
func (e *terminalFallbackError) Unwrap() error { return e.cause }

func (s *journalStream) sendTerminalFallbackLocked(ctx, deliveryParent context.Context, event Event, journalErr error) error {
	if event.Type != "done" && event.Type != "error" && event.Type != "canceled" {
		return journalErr
	}
	// An unsequenced terminal event can settle the live UI while the journal retries.
	event.Seq = 0
	if err := s.deliverLocked(ctx, deliveryParent, event); err != nil {
		return errors.Join(journalErr, err)
	}
	return &terminalFallbackError{cause: journalErr}
}

func journalEventBytes(event Event) int {
	switch event.Type {
	case "delta", "reasoning":
		return len(event.Text)
	default:
		return len(event.Tool) + 16
	}
}

func (s *journalStream) flushTimeout() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	ctx, cancel := s.flushContextLocked()
	defer cancel()
	_ = s.flushLocked(ctx)
}

func (s *journalStream) flushContextLocked() (context.Context, context.CancelFunc) {
	base := s.flushContext
	if base == nil {
		base = context.Background()
	}
	return context.WithTimeout(base, 5*time.Second)
}

// flushLocked persists every buffered event to the journal first, then delivers each to
// the live stream on a best-effort basis. It returns an error only when a journal append
// fails; events not yet appended stay pending for the next flush. A live-stream failure
// never blocks journaling of the remaining events, so a terminal event sent right after a
// flush is always journaled even when the downstream stream is broken.
func (s *journalStream) flushLocked(ctx context.Context) error {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if len(s.pending) == 0 {
		return nil
	}
	merged := mergeJournalEvents(s.pending)
	for index, event := range merged {
		seq, err := s.journal.RunAppendEvent(ctx, s.runID, event.Type, func(seq uint64) ([]byte, error) {
			event.Seq = seq
			return json.Marshal(event)
		})
		if err != nil {
			s.retainLocked(merged[index:])
			if s.retryDelay == 0 {
				s.retryDelay = 2 * journalFlushWindow
			} else {
				s.retryDelay = min(2*s.retryDelay, journalRetryMax)
			}
			if !s.flushWarned {
				slog.Warn("AI 事件日志写入失败", "job", s.runID, "error", err)
				s.flushWarned = true
			}
			s.scheduleFlushLocked()
			return err
		}
		event.Seq = seq
		_ = s.stream.Send(ctx, event)
	}
	s.pending = nil
	s.pendingBytes = 0
	s.retryDelay = 0
	s.flushWarned = false
	return nil
}

func (s *journalStream) retainLocked(events []Event) {
	s.pending = events
	s.pendingBytes = 0
	for _, event := range events {
		s.pendingBytes += journalEventBytes(event)
	}
}

func (s *journalStream) sendLocked(ctx, deliveryParent context.Context, event Event, beforeDeliver func()) (bool, error) {
	seq, err := s.journal.RunAppendEvent(ctx, s.runID, event.Type, func(seq uint64) ([]byte, error) {
		event.Seq = seq
		return json.Marshal(event)
	})
	if err != nil {
		return false, err
	}
	event.Seq = seq
	if beforeDeliver != nil {
		beforeDeliver()
	}
	return true, s.deliverLocked(ctx, deliveryParent, event)
}

func (s *journalStream) deliverLocked(ctx, deliveryParent context.Context, event Event) error {
	if deliveryParent == nil {
		return s.stream.Send(ctx, event)
	}
	liveCtx, cancel := context.WithTimeout(deliveryParent, 5*time.Second)
	defer cancel()
	return s.stream.Send(liveCtx, event)
}

func mergeJournalEvents(events []Event) []Event {
	merged := make([]Event, 0, len(events))
	for _, event := range events {
		if len(merged) > 0 {
			last := &merged[len(merged)-1]
			if last.Type == event.Type {
				switch event.Type {
				case "delta", "reasoning":
					last.Text += event.Text
					continue
				case "toolArgs":
					if last.Tool == event.Tool {
						last.Chars = event.Chars
						continue
					}
				}
			}
		}
		merged = append(merged, event)
	}
	return merged
}

func (s *journalStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	flushErr := s.flushAtCloseLocked()
	closeErr := s.stream.Close()
	return errors.Join(flushErr, closeErr)
}

func (s *journalStream) CloseGracefully() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	flushErr := s.flushAtCloseLocked()
	var closeErr error
	if graceful, ok := s.stream.(interface{ CloseGracefully() error }); ok {
		closeErr = graceful.CloseGracefully()
	} else {
		closeErr = s.stream.Close()
	}
	return errors.Join(flushErr, closeErr)
}

// FlushPending finishes the journal before a run's status is marked terminal.
// The downstream stream stays open until the status update has completed.
func (s *journalStream) FlushPending() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.prepared = true
	return s.flushForCloseLocked()
}

func (s *journalStream) flushAtCloseLocked() error {
	if !s.prepared {
		return s.flushForCloseLocked()
	}
	ctx, cancel := s.flushContextLocked()
	defer cancel()
	return s.flushLocked(ctx)
}

func (s *journalStream) flushForCloseLocked() error {
	ctx, cancel := s.flushContextLocked()
	defer cancel()
	for {
		err := s.flushLocked(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return errors.Join(err, ctx.Err())
		}
		timer := time.NewTimer(s.retryDelay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(err, ctx.Err())
		}
	}
}

type RunDTO struct {
	ID                  string `json:"id"`
	ConversationID      string `json:"conversationId"`
	Status              string `json:"status"`
	Attempt             int64  `json:"attempt"`
	Seq                 uint64 `json:"seq"`
	PlanMode            bool   `json:"planMode"`
	Source              string `json:"source"`
	ProfileID           string `json:"profileId,omitempty"`
	Answer              string `json:"answer"`
	Turns               int    `json:"turns"`
	TokensIn            int64  `json:"tokensIn"`
	TokensOut           int64  `json:"tokensOut"`
	CacheCreationTokens int64  `json:"cacheCreationTokens"`
	LatencyMS           int64  `json:"latencyMs"`
	Retries             int64  `json:"retries"`
	Failures            int64  `json:"failures"`
	Error               string `json:"error,omitempty"`
	CreatedAt           int64  `json:"createdAt"`
	UpdatedAt           int64  `json:"updatedAt"`
	FinishedAt          *int64 `json:"finishedAt,omitempty"`
}

func (r *Runner) RunList(ctx context.Context, conversationID string, limit int) ([]RunDTO, error) {
	if r.runs == nil {
		return []RunDTO{}, nil
	}
	rows, err := r.runs.RunList(ctx, conversationID, limit)
	if err != nil {
		return nil, err
	}
	result := make([]RunDTO, len(rows))
	ids := make([]string, len(rows))
	for index, row := range rows {
		ids[index] = row.ID
	}
	counts, err := r.runs.RunErrorCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	for index, row := range rows {
		result[index] = RunDTO{
			ID: row.ID, ConversationID: row.ConversationID, Status: row.Status, Attempt: row.Attempt, Seq: row.Seq,
			PlanMode: row.PlanMode, Source: row.Source, ProfileID: row.ProfileID, Answer: row.Answer, Turns: row.Turns,
			TokensIn: row.TokensIn, TokensOut: row.TokensOut, CacheCreationTokens: row.CacheCreationTokens, LatencyMS: row.LatencyMS,
			Retries: counts[row.ID].Retries, Failures: counts[row.ID].Failures, Error: row.Error,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, FinishedAt: row.FinishedAt,
		}
	}
	return result, nil
}

func (r *Runner) RunEvents(ctx context.Context, jobID string, afterSeq uint64) ([]json.RawMessage, error) {
	return r.RunEventsLimit(ctx, jobID, afterSeq, 0)
}

func (r *Runner) RunEventsLimit(ctx context.Context, jobID string, afterSeq uint64, limit int) ([]json.RawMessage, error) {
	if r.runs == nil {
		return []json.RawMessage{}, nil
	}
	rows, err := r.runs.RunEventsAfterLimit(ctx, jobID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	result := make([]json.RawMessage, len(rows))
	for index, row := range rows {
		result[index] = store.ParseJSONOr(row.PayloadJSON)
	}
	return result, nil
}
