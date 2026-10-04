package hitl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type RunBlob struct {
	ID   string
	Data []byte
}

type Store interface {
	SaveRun(context.Context, RunBlob) error
	ListRuns(context.Context) ([]RunBlob, error)
}

type requestRecord struct {
	Interrupt Interrupt     `json:"interrupt"`
	Status    requestStatus `json:"status"`
}

type runRecord struct {
	ID           string            `json:"id"`
	CheckpointID string            `json:"checkpointId"`
	Status       RunStatus         `json:"status"`
	Attempt      uint64            `json:"attempt"`
	Sequence     uint64            `json:"sequence"`
	Requests     []requestRecord   `json:"requests"`
	Answers      map[string]string `json:"answers,omitempty"`
	Events       []Event           `json:"events"`
	Terminal     *Event            `json:"terminal,omitempty"`
}

func (run *runState) record() runRecord {
	record := runRecord{
		ID:           run.id,
		CheckpointID: run.checkpointID,
		Status:       run.status,
		Attempt:      run.attempt,
		Sequence:     run.sequence,
		Answers:      run.answers,
		Events:       run.events,
	}
	if run.terminal != nil {
		terminal := *run.terminal
		record.Terminal = &terminal
	}
	for _, request := range run.requests {
		record.Requests = append(record.Requests, requestRecord{Interrupt: cloneInterrupt(request.interrupt), Status: request.status})
	}
	return record
}

func (m *Manager) persistLocked(run *runState) error {
	if m.store == nil {
		return nil
	}
	data, err := json.Marshal(run.record())
	if err != nil {
		return fmt.Errorf("hitl: encode run state: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(run.ctx), 5*time.Second)
	defer cancel()
	if err := m.store.SaveRun(ctx, RunBlob{ID: run.id, Data: data}); err != nil {
		return fmt.Errorf("hitl: persist run state: %w", err)
	}
	return nil
}

func (m *Manager) Restore(blob RunBlob) error {
	if m.store == nil {
		return errors.New("hitl: restore requires a configured store")
	}
	var record runRecord
	if err := json.Unmarshal(blob.Data, &record); err != nil {
		return fmt.Errorf("%w: decode run state: %v", ErrInvalidArgument, err)
	}
	if !validIDs(record.ID, record.CheckpointID) || record.ID != blob.ID {
		return ErrInvalidArgument
	}
	runCtx, cancel := context.WithCancel(context.Background())
	run := &runState{
		id:           record.ID,
		checkpointID: record.CheckpointID,
		ctx:          runCtx,
		cancel:       cancel,
		status:       record.Status,
		attempt:      record.Attempt,
		sequence:     record.Sequence,
		requests:     make(map[string]*requestState, len(record.Requests)),
		answers:      record.Answers,
		events:       record.Events,
	}
	if run.answers == nil {
		run.answers = make(map[string]string)
	}
	if record.Terminal != nil {
		terminal := *record.Terminal
		run.terminal = &terminal
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		return ErrManagerClosed
	}
	if _, exists := m.runs[record.ID]; exists {
		m.mu.Unlock()
		cancel()
		return ErrRunExists
	}
	for _, restored := range record.Requests {
		request := &requestState{interrupt: cloneInterrupt(restored.Interrupt), status: restored.Status}
		run.requests[request.interrupt.ID] = request
		if request.status == requestPending {
			m.nonces[request.interrupt.Nonce] = run.id
		}
	}
	m.runs[record.ID] = run
	m.mu.Unlock()
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.terminal != nil {
		return nil
	}
	now := m.now()
	for _, request := range run.requests {
		if request.status != requestPending {
			continue
		}
		if now.Before(request.interrupt.ExpiresAt) {
			remaining := request.interrupt.ExpiresAt.Sub(now)
			request.timer = time.AfterFunc(remaining, func() { m.expire(run.id, request.interrupt.ID) })
			continue
		}
		request.status = requestExpired
	}
	if run.status == RunStatusInterrupted {
		pending := false
		for _, request := range run.requests {
			if request.status == requestPending {
				pending = true
				break
			}
		}
		if !pending {
			run.status = RunStatusExpired
			event := run.appendEventLocked(EventTerminal, TerminalExpired, "", "")
			run.terminal = &event
			run.cancel()
		}
	}
	return m.persistLocked(run)
}
