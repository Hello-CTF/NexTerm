package agent

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/hitl"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

type RunStore interface {
	RunInsert(context.Context, store.RunRow) error
	RunGet(context.Context, string) (store.RunRow, error)
	RunDelete(context.Context, string) error
	RunList(ctx context.Context, conversationID string, limit int) ([]store.RunRow, error)
	RunsActive(context.Context) ([]store.RunRow, error)
	RunAppendEvent(ctx context.Context, runID, eventType string, payload func(seq uint64) ([]byte, error)) (uint64, error)
	RunEventsAfter(ctx context.Context, runID string, afterSeq uint64) ([]store.RunEventRow, error)
	RunUpdateStatus(ctx context.Context, runID, status string) error
	RunFinish(ctx context.Context, runID, status, answer, errMsg string, turns int, tokensIn, tokensOut int64) error
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

type journalStream struct {
	mu      sync.Mutex
	stream  Stream
	journal RunStore
	runID   string
}

func WithRunJournal(stream Stream, journal RunStore, runID string) Stream {
	return &journalStream{stream: stream, journal: journal, runID: runID}
}

func (s *journalStream) Send(ctx context.Context, event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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

func (s *journalStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream.Close()
}

func (s *journalStream) CloseGracefully() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if graceful, ok := s.stream.(interface{ CloseGracefully() error }); ok {
		return graceful.CloseGracefully()
	}
	return s.stream.Close()
}

type RunDTO struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversationId"`
	Status         string `json:"status"`
	Attempt        int64  `json:"attempt"`
	Seq            uint64 `json:"seq"`
	PlanMode       bool   `json:"planMode"`
	Source         string `json:"source"`
	Answer         string `json:"answer"`
	Turns          int    `json:"turns"`
	TokensIn       int64  `json:"tokensIn"`
	TokensOut      int64  `json:"tokensOut"`
	Error          string `json:"error,omitempty"`
	CreatedAt      int64  `json:"createdAt"`
	UpdatedAt      int64  `json:"updatedAt"`
	FinishedAt     *int64 `json:"finishedAt,omitempty"`
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
	for index, row := range rows {
		result[index] = RunDTO{
			ID: row.ID, ConversationID: row.ConversationID, Status: row.Status, Attempt: row.Attempt, Seq: row.Seq,
			PlanMode: row.PlanMode, Source: row.Source, Answer: row.Answer, Turns: row.Turns,
			TokensIn: row.TokensIn, TokensOut: row.TokensOut, Error: row.Error,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, FinishedAt: row.FinishedAt,
		}
	}
	return result, nil
}

func (r *Runner) RunEvents(ctx context.Context, jobID string, afterSeq uint64) ([]json.RawMessage, error) {
	if r.runs == nil {
		return []json.RawMessage{}, nil
	}
	rows, err := r.runs.RunEventsAfter(ctx, jobID, afterSeq)
	if err != nil {
		return nil, err
	}
	result := make([]json.RawMessage, len(rows))
	for index, row := range rows {
		result[index] = store.ParseJSONOr(row.PayloadJSON)
	}
	return result, nil
}
