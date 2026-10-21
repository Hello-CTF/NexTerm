package production

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type fakeProgressRunner struct {
	progressOwner   string
	progressBatch   execbatch.BatchProgress
	progressTargets []execbatch.TargetProgress
	progressOK      bool
	rerunOwner      string
	rerunOriginal   execbatch.Request
	rerunResults    []execbatch.Result
	rerunBatchID    string
	rerunBatch      execbatch.Batch
	rerunErr        error
}

func (f *fakeProgressRunner) BatchProgress(ownerID, batchID string) (execbatch.BatchProgress, []execbatch.TargetProgress, bool) {
	f.progressOwner = ownerID
	if !f.progressOK {
		return execbatch.BatchProgress{}, nil, false
	}
	return f.progressBatch, f.progressTargets, true
}

func (f *fakeProgressRunner) Rerun(_ context.Context, ownerID string, original execbatch.Request, results []execbatch.Result, newBatchID string) (execbatch.Batch, error) {
	f.rerunOwner = ownerID
	f.rerunOriginal = original
	f.rerunResults = results
	f.rerunBatchID = newBatchID
	return f.rerunBatch, f.rerunErr
}

func requireIPCCode(t *testing.T, response ipc.Response, code ipc.Code) {
	t.Helper()
	if response.OK || response.Error == nil || response.Error.Code != code {
		t.Fatalf("response = %+v, want code %s", response, code)
	}
}

func TestExecBatchProgressCommand(t *testing.T) {
	runner := &fakeProgressRunner{
		progressOK: true,
		progressBatch: execbatch.BatchProgress{
			BatchID: "batch-1", Phase: execbatch.ProgressPhaseRunning, Total: 2, Running: 1, Pending: 1,
		},
		progressTargets: []execbatch.TargetProgress{
			{SessionID: "session-a", Position: 0, Phase: execbatch.ProgressPhaseRunning},
			{SessionID: "session-b", Position: 1, Phase: execbatch.ProgressPhasePending},
		},
	}
	dispatcher := ipc.NewDispatcher()
	if err := registerExecBatchProgressCommands(dispatcher, runner); err != nil {
		t.Fatal(err)
	}
	ctx := ipc.WithUserID(context.Background(), "owner-a")

	snapshot := decodeExecBatchTest[execBatchProgressSnapshot](t, dispatchExecBatchTest(dispatcher, ctx, "exec_batch_progress", map[string]any{"batchId": "batch-1"}))
	if runner.progressOwner != "owner-a" {
		t.Fatalf("progress owner = %q", runner.progressOwner)
	}
	if snapshot.Batch.Phase != execbatch.ProgressPhaseRunning || len(snapshot.Targets) != 2 || snapshot.Targets[0].SessionID != "session-a" {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	response := dispatchExecBatchTest(dispatcher, ctx, "exec_batch_progress", map[string]any{"batchId": "  "})
	requireIPCCode(t, response, ipc.CodeBadParam)

	runner.progressOK = false
	response = dispatchExecBatchTest(dispatcher, ctx, "exec_batch_progress", map[string]any{"batchId": "missing"})
	requireIPCCode(t, response, ipc.CodeNotFound)
}

func TestExecBatchRerunCommand(t *testing.T) {
	runner := &fakeProgressRunner{rerunBatch: execbatch.Batch{BatchID: "batch-2"}}
	dispatcher := ipc.NewDispatcher()
	if err := registerExecBatchProgressCommands(dispatcher, runner); err != nil {
		t.Fatal(err)
	}
	ctx := ipc.WithUserID(context.Background(), "owner-a")

	batch := decodeExecBatchTest[execbatch.Batch](t, dispatchExecBatchTest(dispatcher, ctx, "exec_batch_rerun", map[string]any{
		"batchId": "batch-2", "command": "deploy", "timeoutMs": 30000, "maxConcurrent": 4,
		"results": []map[string]any{
			{"sessionId": "ok", "status": "success"},
			{"sessionId": "bad", "status": "exit_error"},
			{"sessionId": "stop", "status": "canceled"},
		},
	}))
	if batch.BatchID != "batch-2" {
		t.Fatalf("batch = %+v", batch)
	}
	if runner.rerunOwner != "owner-a" || runner.rerunBatchID != "batch-2" {
		t.Fatalf("rerun owner/batch = %q/%q", runner.rerunOwner, runner.rerunBatchID)
	}
	if runner.rerunOriginal.Command != "deploy" || runner.rerunOriginal.TimeoutMS != 30000 || runner.rerunOriginal.MaxConcurrent != 4 {
		t.Fatalf("rerun original = %+v", runner.rerunOriginal)
	}
	if len(runner.rerunResults) != 3 || runner.rerunResults[1].SessionID != "bad" || runner.rerunResults[1].Status != execbatch.StatusExitError {
		t.Fatalf("rerun results = %+v", runner.rerunResults)
	}

	for _, invalid := range []map[string]any{
		{"batchId": "  ", "command": "deploy", "results": []map[string]any{{"sessionId": "a", "status": "success"}}},
		{"batchId": "batch-3", "command": "deploy"},
		{"batchId": "batch-3", "command": "deploy", "results": []map[string]any{{"sessionId": " ", "status": "success"}}},
		{"batchId": "batch-3", "command": "deploy", "results": []map[string]any{{"sessionId": "a", "status": " "}}},
	} {
		response := dispatchExecBatchTest(dispatcher, ctx, "exec_batch_rerun", invalid)
		requireIPCCode(t, response, ipc.CodeBadParam)
	}

	runner.rerunErr = &execbatch.ConfirmationRequiredError{BatchID: "batch-4"}
	response := dispatchExecBatchTest(dispatcher, ctx, "exec_batch_rerun", map[string]any{
		"batchId": "batch-4", "command": "deploy",
		"results": []map[string]any{{"sessionId": "bad", "status": "exit_error"}},
	})
	requireIPCCode(t, response, ipc.CodeNeedsConfirm)
}

func TestExecBatchRerunCommandExecutesFailedTargetsOnly(t *testing.T) {
	var execCalls atomic.Int32
	transport := newExecBatchTestTransport(func(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
		execCalls.Add(1)
		code := 1
		return base.ExecResult{ExitCode: &code}, nil
	})
	manager := newExecBatchTestManager(t, transport)
	ownerA := ipc.WithUserID(context.Background(), "owner-a")
	badSession, err := manager.ConnectFor(ownerA, session.Asset{ID: "asset-bad", Name: "bad", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	goneSession, err := manager.ConnectFor(ownerA, session.Asset{ID: "asset-gone", Name: "gone", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	okSession, err := manager.ConnectFor(ownerA, session.Asset{ID: "asset-ok", Name: "ok", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	service := newExecBatchService(manager, execBatchTestAssets(nil), nil)
	dispatcher := ipc.NewDispatcher()
	if err := registerExecBatchProgressCommands(dispatcher, service); err != nil {
		t.Fatal(err)
	}

	batch := decodeExecBatchTest[execbatch.Batch](t, dispatchExecBatchTest(dispatcher, ownerA, "exec_batch_rerun", map[string]any{
		"batchId": "batch-2", "command": "fail",
		"results": []map[string]any{
			{"sessionId": okSession.ID, "status": "success"},
			{"sessionId": badSession.ID, "status": "exit_error"},
			{"sessionId": goneSession.ID, "status": "transport_error"},
		},
	}))
	if len(batch.Results) != 2 {
		t.Fatalf("rerun results = %+v", batch.Results)
	}
	statuses := map[string]string{}
	for _, result := range batch.Results {
		statuses[result.SessionID] = string(result.Status)
	}
	if statuses[badSession.ID] != string(execbatch.StatusExitError) || statuses[goneSession.ID] != string(execbatch.StatusExitError) {
		t.Fatalf("rerun statuses = %+v", statuses)
	}
	if _, exists := statuses[okSession.ID]; exists {
		t.Fatalf("successful target was rerun: %+v", statuses)
	}
	if execCalls.Load() != 2 {
		t.Fatalf("exec calls = %d, want 2", execCalls.Load())
	}
}

func TestExecBatchProgressCommandLiveLifecycle(t *testing.T) {
	release := make(chan struct{})
	transport := newExecBatchTestTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
		select {
		case <-release:
			code := 0
			return base.ExecResult{ExitCode: &code}, nil
		case <-ctx.Done():
			return base.ExecResult{}, ctx.Err()
		}
	})
	manager := newExecBatchTestManager(t, transport)
	ownerA := ipc.WithUserID(context.Background(), "owner-a")
	connected, err := manager.ConnectFor(ownerA, session.Asset{ID: "asset-a", Name: "a", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	service := newExecBatchService(manager, execBatchTestAssets(nil), nil)
	dispatcher := ipc.NewDispatcher()
	if err := registerExecBatchProgressCommands(dispatcher, service); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = service.Execute(ownerA, "owner-a", execbatch.Request{
			BatchID: "batch-1", SessionIDs: []string{connected.ID}, Command: "hold",
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot := dispatchExecBatchTest(dispatcher, ownerA, "exec_batch_progress", map[string]any{"batchId": "batch-1"})
		if snapshot.OK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("progress never became available")
		}
		time.Sleep(time.Millisecond)
	}
	if response := dispatchExecBatchTest(dispatcher, ipc.WithUserID(context.Background(), "owner-b"), "exec_batch_progress", map[string]any{"batchId": "batch-1"}); response.OK {
		t.Fatal("owner B read owner A's batch progress")
	}
	close(release)
	<-done
	// The terminal snapshot stays readable for a bounded TTL after
	// completion: the poll observes the finished transition, not not_found.
	terminal := decodeExecBatchTest[execBatchProgressSnapshot](t, dispatchExecBatchTest(dispatcher, ownerA, "exec_batch_progress", map[string]any{"batchId": "batch-1"}))
	if terminal.Batch.Phase != execbatch.ProgressPhaseFinished || terminal.Batch.Succeeded != 1 || terminal.Batch.FinishedAt == 0 {
		t.Fatalf("terminal snapshot = %+v", terminal.Batch)
	}
	if len(terminal.Targets) != 1 || terminal.Targets[0].Status != execbatch.StatusSuccess {
		t.Fatalf("terminal targets = %+v", terminal.Targets)
	}
}

func TestExecBatchRerunCommandProductionConfirmation(t *testing.T) {
	var execCalls atomic.Int32
	transport := newExecBatchTestTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
		execCalls.Add(1)
		code := 1
		return base.ExecResult{ExitCode: &code}, nil
	})
	manager := newExecBatchTestManager(t, transport)
	owner := ipc.WithUserID(context.Background(), "owner-a")
	connected, err := manager.ConnectFor(owner, session.Asset{ID: "asset-production", Name: "Production API", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	auditor := &execBatchProductionAuditor{}
	service := newExecBatchService(manager, execBatchTestAssets(map[string]store.AssetRow{
		"asset-production": {ID: "asset-production", Name: "Production API", Tags: "env:production"},
	}), auditor)
	dispatcher := ipc.NewDispatcher()
	if err := registerExecBatchCommands(dispatcher, service, nil); err != nil {
		t.Fatal(err)
	}
	if err := registerExecBatchProgressCommands(dispatcher, service); err != nil {
		t.Fatal(err)
	}

	original := execbatch.Request{BatchID: "batch-1", SessionIDs: []string{connected.ID}, Command: "deploy"}
	response := dispatchExecBatchTest(dispatcher, owner, "exec_batch_execute", original)
	if !response.OK && response.Error != nil && response.Error.Code == ipc.CodeNeedsConfirm {
		detail := decodeConfirmationDetail(t, response)
		original.ProductionConfirmation = &execbatch.ProductionConfirmation{
			Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash,
			EnvironmentNames: detail.RequiredEnvironmentNames,
		}
	}
	first := decodeExecBatchTest[execbatch.Batch](t, dispatchExecBatchTest(dispatcher, owner, "exec_batch_execute", original))
	if len(first.Results) != 1 || first.Results[0].Status != execbatch.StatusExitError {
		t.Fatalf("first batch = %+v", first.Results)
	}
	callsAfterFirst := execCalls.Load()

	rerunResults := []map[string]any{{"sessionId": connected.ID, "status": "exit_error"}}
	// No confirmation: the rerun demands a fresh, batch-bound one.
	response = dispatchExecBatchTest(dispatcher, owner, "exec_batch_rerun", map[string]any{
		"batchId": "batch-2", "command": "deploy", "results": rerunResults,
	})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNeedsConfirm {
		t.Fatalf("unconfirmed rerun = %+v", response)
	}
	detail2 := decodeConfirmationDetail(t, response)
	if detail2.BatchID != "batch-2" {
		t.Fatalf("rerun challenge batch = %q", detail2.BatchID)
	}

	// The original batch's confirmation is stale for the rerun.
	response = dispatchExecBatchTest(dispatcher, owner, "exec_batch_rerun", map[string]any{
		"batchId": "batch-2", "command": "deploy", "results": rerunResults,
		"productionConfirmation": original.ProductionConfirmation,
	})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNeedsConfirm {
		t.Fatalf("stale-confirmed rerun = %+v", response)
	}

	// A fresh confirmation bound to the rerun batch executes the failed
	// target and records the confirmation audit.
	confirmed := map[string]any{
		"batchId": "batch-2", "command": "deploy", "results": rerunResults,
		"productionConfirmation": &execbatch.ProductionConfirmation{
			Confirmed: true, TargetSetHash: detail2.TargetSetHash, CommandHash: detail2.CommandHash,
			EnvironmentNames: detail2.RequiredEnvironmentNames,
		},
	}
	batch := decodeExecBatchTest[execbatch.Batch](t, dispatchExecBatchTest(dispatcher, owner, "exec_batch_rerun", confirmed))
	if len(batch.Results) != 1 || batch.Results[0].Status != execbatch.StatusExitError {
		t.Fatalf("confirmed rerun = %+v", batch.Results)
	}
	if execCalls.Load() != callsAfterFirst+1 {
		t.Fatalf("rerun exec calls = %d, want %d", execCalls.Load(), callsAfterFirst+1)
	}
	auditor.mu.Lock()
	defer auditor.mu.Unlock()
	var rerunAudit []store.AuditInput
	for _, record := range auditor.records {
		if record.Kind == "exec_batch_production_confirmation" {
			encoded, marshalErr := json.Marshal(record.Payload)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if strings.Contains(string(encoded), `"batchId":"batch-2"`) {
				rerunAudit = append(rerunAudit, record)
			}
		}
	}
	if len(rerunAudit) != 1 {
		t.Fatalf("rerun confirmation audit records = %+v", auditor.records)
	}
}

func decodeConfirmationDetail(t *testing.T, response ipc.Response) execbatch.ConfirmationRequiredDetail {
	t.Helper()
	encoded, err := json.Marshal(response.Error.Detail)
	if err != nil {
		t.Fatal(err)
	}
	var detail execbatch.ConfirmationRequiredDetail
	if err := json.Unmarshal(encoded, &detail); err != nil {
		t.Fatal(err)
	}
	return detail
}
