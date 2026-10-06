package takeover

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/adk"
)

type recoveryRecord struct {
	JobID       string `json:"jobId"`
	TabID       string `json:"tabId"`
	AssetID     string `json:"assetId,omitempty"`
	Instruction string `json:"instruction"`
	AllowWrite  *bool  `json:"allowWrite,omitempty"`
	MaxSteps    int    `json:"maxSteps"`
	PausedAt    int64  `json:"pausedAt"`
}

func recoveryKey(jobID string) string {
	return "takeover:recovery:" + jobID
}

func execKey(jobID string) string {
	return "takeover:exec:" + jobID
}

func (m *Manager) persistRecovery(state *runState) {
	record := recoveryRecord{
		JobID: state.id, TabID: state.args.TabID, AssetID: state.assetID,
		Instruction: state.args.Instruction, AllowWrite: state.args.AllowWrite,
		MaxSteps: state.args.MaxSteps, PausedAt: m.deps.Now().UnixMilli(),
	}
	data, err := json.Marshal(record)
	if err != nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(state.ctx), 5*time.Second)
	defer cancel()
	_ = m.checkpoints.Set(writeCtx, recoveryKey(state.id), data)
}

func (m *Manager) loadRecovery(ctx context.Context, jobID string) (*recoveryRecord, error) {
	data, found, err := m.checkpoints.Get(ctx, recoveryKey(jobID))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNotFound
	}
	var record recoveryRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (m *Manager) deleteRecovery(ctx context.Context, jobID string) {
	if deleter, ok := m.checkpoints.(adk.CheckPointDeleter); ok {
		_ = deleter.Delete(ctx, recoveryKey(jobID))
	}
}

type detachedCheckpoints struct{ adk.CheckPointStore }

func (d detachedCheckpoints) Get(ctx context.Context, id string) ([]byte, bool, error) {
	storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return d.CheckPointStore.Get(storeCtx, id)
}

func (d detachedCheckpoints) Set(ctx context.Context, id string, data []byte) error {
	storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return d.CheckPointStore.Set(storeCtx, id, data)
}

func (d detachedCheckpoints) Delete(ctx context.Context, id string) error {
	if deleter, ok := d.CheckPointStore.(adk.CheckPointDeleter); ok {
		storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return deleter.Delete(storeCtx, id)
	}
	return nil
}

const (
	execStateAttempt = "attempt"
	execStateDone    = "done"
)

type execRecord struct {
	State  string       `json:"state"`
	Output tools.Output `json:"output,omitempty"`
}

type execRecords map[string]execRecord

func (m *Manager) loadExecRecords(ctx context.Context, jobID string) (execRecords, error) {
	data, found, err := m.checkpoints.Get(ctx, execKey(jobID))
	if err != nil {
		return nil, err
	}
	if !found {
		return execRecords{}, nil
	}
	var records execRecords
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	if records == nil {
		records = execRecords{}
	}
	return records, nil
}

func (m *Manager) saveExecRecords(ctx context.Context, jobID string, records execRecords) error {
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	return m.checkpoints.Set(ctx, execKey(jobID), data)
}
