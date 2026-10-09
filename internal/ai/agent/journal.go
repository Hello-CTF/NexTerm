package agent

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
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
		blobs[index] = hitl.RunBlob{ID: row.ID, Data: row.Data}
	}
	return blobs, nil
}

const (
	journalFlushWindow = 150 * time.Millisecond
	journalFlushBytes  = 32 << 10
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
	pending      []Event
	pendingBytes int
	timer        *time.Timer
}

func WithRunJournal(stream Stream, journal RunStore, runID string) Stream {
	return &journalStream{stream: stream, journal: journal, runID: runID}
}

func (s *journalStream) Send(ctx context.Context, event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if journalBatchable(event.Type) {
		s.bufferLocked(event)
		if s.pendingBytes >= journalFlushBytes {
			return s.flushLocked(ctx)
		}
		return nil
	}
	if err := s.flushLocked(ctx); err != nil {
		return err
	}
	return s.sendLocked(ctx, event)
}

func (s *journalStream) bufferLocked(event Event) {
	if s.timer == nil {
		s.timer = time.AfterFunc(journalFlushWindow, s.flushTimeout)
	}
	s.pending = append(s.pending, event)
	s.pendingBytes += journalEventBytes(event)
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
	_ = s.flushLocked(context.Background())
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
			return err
		}
		event.Seq = seq
		_ = s.stream.Send(ctx, event)
	}
	s.pending = nil
	s.pendingBytes = 0
	return nil
}

func (s *journalStream) retainLocked(events []Event) {
	s.pending = events
	s.pendingBytes = 0
	for _, event := range events {
		s.pendingBytes += journalEventBytes(event)
	}
}

func (s *journalStream) sendLocked(ctx context.Context, event Event) error {
	seq, err := s.journal.RunAppendEvent(ctx, s.runID, event.Type, func(seq uint64) ([]byte, error) {
		event.Seq = seq
		return json.Marshal(event)
	})
	if err != nil {
		return err
	}
	event.Seq = seq
	return s.stream.Send(ctx, event)
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
	flushErr := s.flushLocked(context.Background())
	closeErr := s.stream.Close()
	if flushErr != nil {
		return flushErr
	}
	return closeErr
}

func (s *journalStream) CloseGracefully() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	flushErr := s.flushLocked(context.Background())
	var closeErr error
	if graceful, ok := s.stream.(interface{ CloseGracefully() error }); ok {
		closeErr = graceful.CloseGracefully()
	} else {
		closeErr = s.stream.Close()
	}
	if flushErr != nil {
		return flushErr
	}
	return closeErr
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
