package production

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/runbook"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestRunbookIPCProductionChallengeAndHistoryReadback(t *testing.T) {
	var execCalls atomic.Int32
	transport := newExecBatchTestTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
		execCalls.Add(1)
		code := 0
		return base.ExecResult{ExitCode: &code}, nil
	})
	manager := newExecBatchTestManager(t, transport)
	owner := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(owner, session.Asset{ID: "asset-production", Name: "Production API", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	hostname := "api.production.example"
	auditor := &execBatchProductionAuditor{}
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	batches := newExecBatchService(manager, execBatchTestAssets(map[string]store.AssetRow{
		"asset-production": {ID: "asset-production", Name: "Production API", Host: &hostname, Tags: "env:production"},
	}), auditor, database)
	service := newRunbookIPCService(batches, database)
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}

	start := dispatchExecBatchTest(dispatcher, owner, "runbook_run_start", runbook.StartRequest{
		RunID: "run-ipc", RunbookID: "runbook-ipc", RunbookName: "Production deploy",
		Steps:      []runbook.Step{{ID: "deploy", Name: "Deploy", Command: "deploy {{asset.name}}"}},
		SessionIDs: []string{connected.ID},
	})
	state := decodeExecBatchTest[runbook.RunState](t, start)
	if state.Status != runbook.RunAwaitingRender || !state.StopOnFailure || state.ConfirmationPolicy != runbook.ConfirmEveryStep {
		t.Fatalf("start state = %+v", state)
	}
	prepare := dispatchExecBatchTest(dispatcher, owner, "runbook_run_prepare", runbook.PrepareRequest{
		RunID: "run-ipc", StepNumber: 1,
		Commands: []runbook.RenderedCommand{{SessionID: connected.ID, Command: "deploy rendered-prod"}},
	})
	state = decodeExecBatchTest[runbook.RunState](t, prepare)
	if state.Status != runbook.RunAwaitingConfirmation || state.Steps[0].RenderedCommands[0].Command != "deploy rendered-prod" {
		t.Fatalf("prepare state = %+v", state)
	}
	unconfirmed := dispatchExecBatchTest(dispatcher, owner, "runbook_run_advance", runbook.AdvanceRequest{RunID: "run-ipc", StepNumber: 1})
	if unconfirmed.OK || unconfirmed.Error == nil || unconfirmed.Error.Code != ipc.CodeNeedsConfirm || execCalls.Load() != 0 {
		t.Fatalf("unconfirmed response = %+v, exec calls = %d", unconfirmed, execCalls.Load())
	}
	challenged := dispatchExecBatchTest(dispatcher, owner, "runbook_run_advance", runbook.AdvanceRequest{
		RunID: "run-ipc", StepNumber: 1, Confirmed: true,
	})
	state = decodeExecBatchTest[runbook.RunState](t, challenged)
	if state.Status != runbook.RunAwaitingProductionConfirmation || state.ProductionChallenge == nil ||
		state.ProductionCommand != "deploy rendered-prod" || len(state.ProductionChallenge.ProductionTargets) != 1 ||
		state.ProductionChallenge.ProductionTargets[0].Hostname != hostname || execCalls.Load() != 0 {
		t.Fatalf("production challenge state = %+v, exec calls = %d", state, execCalls.Load())
	}
	confirmed := dispatchExecBatchTest(dispatcher, owner, "runbook_run_advance", runbook.AdvanceRequest{
		RunID: "run-ipc", StepNumber: 1,
		ProductionConfirmation: &execbatch.ProductionConfirmation{
			Confirmed: true, TargetSetHash: state.ProductionChallenge.TargetSetHash, CommandHash: state.ProductionChallenge.CommandHash,
		},
	})
	state = decodeExecBatchTest[runbook.RunState](t, confirmed)
	if state.Status != runbook.RunSuccess || len(state.Steps[0].Results) != 1 ||
		state.Steps[0].Results[0].Status != execbatch.StatusSuccess || execCalls.Load() != 1 {
		t.Fatalf("confirmed state = %+v, exec calls = %d", state, execCalls.Load())
	}

	historyResponse := dispatchExecBatchTest(dispatcher, owner, "runbook_run_history_get", runbook.RunRequest{RunID: "run-ipc"})
	history := decodeExecBatchTest[store.ExecBatchRunHistory](t, historyResponse)
	if history.RunbookID != "runbook-ipc" || history.ReachedStep != 1 || history.StepCount != 1 || history.Status != store.ExecBatchStatusSuccess ||
		len(history.Batches) != 1 || !history.Batches[0].Batch.Production || history.Batches[0].Batch.StepNumber != 1 ||
		len(history.Batches[0].Results) != 1 || history.Batches[0].Results[0].SessionID != connected.ID {
		t.Fatalf("run history = %+v", history)
	}
	if strings.Contains(string(historyResponse.Data), "deploy rendered-prod") {
		t.Fatalf("history contains rendered command plaintext: %s", historyResponse.Data)
	}
}

func TestRunbookIPCDisconnectedTargetContinuesAndPersistsFailure(t *testing.T) {
	var badExecCalls atomic.Int32
	var goodExecCalls atomic.Int32
	transports := map[string]base.Transport{
		"asset-bad": newExecBatchTestTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
			badExecCalls.Add(1)
			code := 0
			return base.ExecResult{ExitCode: &code}, nil
		}),
		"asset-good": newExecBatchTestTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
			goodExecCalls.Add(1)
			code := 0
			return base.ExecResult{ExitCode: &code}, nil
		}),
	}
	manager := session.NewManager(session.Config{Connector: session.ConnectorFunc(func(_ context.Context, asset session.Asset, _ uint64) (base.Transport, error) {
		return transports[asset.ID], nil
	})})
	t.Cleanup(func() { _ = manager.Close() })
	owner := ipc.WithUserID(t.Context(), "owner-a")
	bad, err := manager.ConnectFor(owner, session.Asset{ID: "asset-bad", Name: "Bad", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	good, err := manager.ConnectFor(owner, session.Asset{ID: "asset-good", Name: "Good", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.DisconnectFor(owner, bad.ID); err != nil {
		t.Fatal(err)
	}
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	batches := newExecBatchService(manager, execBatchTestAssets(nil), nil, database)
	service := newRunbookIPCService(batches, database)
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}
	stop := false
	steps := []runbook.Step{{ID: "one", Name: "One", Command: "one"}, {ID: "two", Name: "Two", Command: "two"}}
	start := dispatchExecBatchTest(dispatcher, owner, "runbook_run_start", runbook.StartRequest{
		RunID: "run-disconnected", RunbookID: "runbook-disconnected", RunbookName: "Disconnected",
		Steps: steps, SessionIDs: []string{bad.ID, good.ID}, StopOnFailure: &stop,
	})
	decodeExecBatchTest[runbook.RunState](t, start)
	prepare := dispatchExecBatchTest(dispatcher, owner, "runbook_run_prepare", runbook.PrepareRequest{
		RunID: "run-disconnected", StepNumber: 1,
		Commands: []runbook.RenderedCommand{{SessionID: bad.ID, Command: "bad-one"}, {SessionID: good.ID, Command: "good-one"}},
	})
	decodeExecBatchTest[runbook.RunState](t, prepare)
	advance := dispatchExecBatchTest(dispatcher, owner, "runbook_run_advance", runbook.AdvanceRequest{
		RunID: "run-disconnected", StepNumber: 1, Confirmed: true,
	})
	state := decodeExecBatchTest[runbook.RunState](t, advance)
	if state.Status != runbook.RunAwaitingRender || state.CurrentStep != 2 || goodExecCalls.Load() != 1 || badExecCalls.Load() != 0 {
		t.Fatalf("disconnected first step = %+v, good calls = %d, bad calls = %d", state, goodExecCalls.Load(), badExecCalls.Load())
	}
	prepare = dispatchExecBatchTest(dispatcher, owner, "runbook_run_prepare", runbook.PrepareRequest{
		RunID: "run-disconnected", StepNumber: 2,
		Commands: []runbook.RenderedCommand{{SessionID: bad.ID, Command: "bad-two"}, {SessionID: good.ID, Command: "good-two"}},
	})
	decodeExecBatchTest[runbook.RunState](t, prepare)
	advance = dispatchExecBatchTest(dispatcher, owner, "runbook_run_advance", runbook.AdvanceRequest{
		RunID: "run-disconnected", StepNumber: 2, Confirmed: true,
	})
	state = decodeExecBatchTest[runbook.RunState](t, advance)
	if state.Status != runbook.RunFailed || goodExecCalls.Load() != 2 || badExecCalls.Load() != 0 {
		t.Fatalf("disconnected final state = %+v, good calls = %d, bad calls = %d", state, goodExecCalls.Load(), badExecCalls.Load())
	}
	historyResponse := dispatchExecBatchTest(dispatcher, owner, "runbook_run_history_get", runbook.RunRequest{RunID: "run-disconnected"})
	history := decodeExecBatchTest[store.ExecBatchRunHistory](t, historyResponse)
	if history.Status != store.ExecBatchStatusFailed || history.ReachedStep != 2 || len(history.Batches) != 4 {
		t.Fatalf("disconnected history = %+v", history)
	}
	failedBatches := 0
	successfulBatches := 0
	for _, batch := range history.Batches {
		switch batch.Batch.Status {
		case store.ExecBatchStatusFailed:
			failedBatches++
			if len(batch.Results) != 1 || batch.Results[0].SessionID != bad.ID || batch.Results[0].ErrorKind != store.ExecBatchErrorTransport {
				t.Fatalf("disconnected history failure = %+v", batch)
			}
		case store.ExecBatchStatusSuccess:
			successfulBatches++
			if len(batch.Results) != 1 || batch.Results[0].SessionID != good.ID {
				t.Fatalf("disconnected history success = %+v", batch)
			}
		}
	}
	if failedBatches != 2 || successfulBatches != 2 {
		t.Fatalf("history status counts = failed %d, success %d", failedBatches, successfulBatches)
	}
}
