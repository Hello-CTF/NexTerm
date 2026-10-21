package production

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
			EnvironmentNames: state.ProductionChallenge.RequiredEnvironmentNames,
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

type runbookIPCExecutor struct {
	mu      sync.Mutex
	block   map[string]bool
	cancels map[string]context.CancelFunc
	started chan string
}

func newRunbookIPCExecutor(block ...string) *runbookIPCExecutor {
	blocked := make(map[string]bool, len(block))
	for _, batchID := range block {
		blocked[batchID] = true
	}
	return &runbookIPCExecutor{block: blocked, cancels: map[string]context.CancelFunc{}, started: make(chan string, 16)}
}

func (e *runbookIPCExecutor) Execute(ctx context.Context, _ string, request execbatch.Request) (execbatch.Batch, error) {
	results := make([]execbatch.Result, len(request.SessionIDs))
	for index, sessionID := range request.SessionIDs {
		results[index] = execbatch.Result{SessionID: sessionID, SessionName: sessionID, Status: execbatch.StatusSuccess}
	}
	batch := execbatch.Batch{BatchID: request.BatchID, Command: request.Command, Results: results}
	e.mu.Lock()
	blocked := e.block[request.BatchID]
	e.mu.Unlock()
	if !blocked {
		return batch, nil
	}
	execCtx, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	e.cancels[request.BatchID] = cancel
	e.mu.Unlock()
	e.started <- request.BatchID
	<-execCtx.Done()
	e.mu.Lock()
	delete(e.cancels, request.BatchID)
	e.mu.Unlock()
	for index := range results {
		results[index].Status = execbatch.StatusCanceled
		results[index].Error = context.Canceled.Error()
	}
	return batch, nil
}

func (e *runbookIPCExecutor) Cancel(_ context.Context, _, batchID string) (execbatch.CancelResult, error) {
	e.mu.Lock()
	cancel := e.cancels[batchID]
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return execbatch.CancelResult{BatchID: batchID, Canceled: true}, nil
}

func runbookIPCStartRun(t *testing.T, dispatcher *ipc.Dispatcher, ctx context.Context, runID, name string) ipc.Response {
	t.Helper()
	return dispatchExecBatchTest(dispatcher, ctx, "runbook_run_start", runbook.StartRequest{
		RunID: runID, RunbookID: "rb-" + runID, RunbookName: name,
		Steps: []runbook.Step{{ID: "s1", Name: "S1", Command: "do"}}, SessionIDs: []string{"session-1"},
	})
}

func runbookIPCGetRun(t *testing.T, dispatcher *ipc.Dispatcher, ctx context.Context, runID string) ipc.Response {
	t.Helper()
	return dispatchExecBatchTest(dispatcher, ctx, "runbook_run_get", runbook.RunRequest{RunID: runID})
}

func runbookIPCCompleteRun(t *testing.T, dispatcher *ipc.Dispatcher, ctx context.Context, runID string) runbook.RunState {
	t.Helper()
	prepare := dispatchExecBatchTest(dispatcher, ctx, "runbook_run_prepare", runbook.PrepareRequest{
		RunID: runID, StepNumber: 1,
		Commands: []runbook.RenderedCommand{{SessionID: "session-1", Command: "do"}},
	})
	decodeExecBatchTest[runbook.RunState](t, prepare)
	advance := dispatchExecBatchTest(dispatcher, ctx, "runbook_run_advance", runbook.AdvanceRequest{RunID: runID, StepNumber: 1, Confirmed: true})
	return decodeExecBatchTest[runbook.RunState](t, advance)
}

func runbookIPCAdvanceAsync(dispatcher *ipc.Dispatcher, ctx context.Context, runID string) <-chan ipc.Response {
	done := make(chan ipc.Response, 1)
	go func() {
		dispatchExecBatchTest(dispatcher, ctx, "runbook_run_prepare", runbook.PrepareRequest{
			RunID: runID, StepNumber: 1,
			Commands: []runbook.RenderedCommand{{SessionID: "session-1", Command: "do"}},
		})
		done <- dispatchExecBatchTest(dispatcher, ctx, "runbook_run_advance", runbook.AdvanceRequest{RunID: runID, StepNumber: 1, Confirmed: true})
	}()
	return done
}

func requireRunbookIPCNotFound(t *testing.T, response ipc.Response) {
	t.Helper()
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
		t.Fatalf("response = %+v, want not-found", response)
	}
}

func TestRunbookIPCOwnersShareRunIDWithoutInterference(t *testing.T) {
	service := newRunbookIPCService(newRunbookIPCExecutor())
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	ownerB := ipc.WithUserID(t.Context(), "owner-b")

	stateA := decodeExecBatchTest[runbook.RunState](t, runbookIPCStartRun(t, dispatcher, ownerA, "shared-run", "runbook A"))
	stateB := decodeExecBatchTest[runbook.RunState](t, runbookIPCStartRun(t, dispatcher, ownerB, "shared-run", "runbook B"))
	if stateA.RunbookName != "runbook A" || stateB.RunbookName != "runbook B" {
		t.Fatalf("shared runID start states = %+v, %+v", stateA, stateB)
	}
	getA := decodeExecBatchTest[runbook.RunState](t, runbookIPCGetRun(t, dispatcher, ownerA, "shared-run"))
	getB := decodeExecBatchTest[runbook.RunState](t, runbookIPCGetRun(t, dispatcher, ownerB, "shared-run"))
	if getA.RunbookName != "runbook A" || getB.RunbookName != "runbook B" {
		t.Fatalf("shared runID reads = %+v, %+v", getA, getB)
	}

	stateA = runbookIPCCompleteRun(t, dispatcher, ownerA, "shared-run")
	if stateA.Status != runbook.RunSuccess {
		t.Fatalf("owner A final state = %+v", stateA)
	}
	getB = decodeExecBatchTest[runbook.RunState](t, runbookIPCGetRun(t, dispatcher, ownerB, "shared-run"))
	if getB.Status != runbook.RunAwaitingRender {
		t.Fatalf("owner A completion changed owner B run: %+v", getB)
	}
	stateB = runbookIPCCompleteRun(t, dispatcher, ownerB, "shared-run")
	if stateB.Status != runbook.RunSuccess {
		t.Fatalf("owner B final state = %+v", stateB)
	}

	requireRunbookIPCNotFound(t, runbookIPCGetRun(t, dispatcher, ownerA, "only-b"))
}

func TestRunbookIPCActiveRunLimitIsPerOwner(t *testing.T) {
	service := newRunbookIPCService(newRunbookIPCExecutor())
	service.maxActiveRunsPerOwner = 1
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	ownerB := ipc.WithUserID(t.Context(), "owner-b")

	decodeExecBatchTest[runbook.RunState](t, runbookIPCStartRun(t, dispatcher, ownerA, "run-a1", "A1"))
	overflow := runbookIPCStartRun(t, dispatcher, ownerA, "run-a2", "A2")
	if overflow.OK || overflow.Error == nil || overflow.Error.Code != ipc.Code("conflict") {
		t.Fatalf("active run overflow = %+v, want conflict", overflow)
	}
	decodeExecBatchTest[runbook.RunState](t, runbookIPCStartRun(t, dispatcher, ownerB, "run-b1", "B1"))

	canceled := dispatchExecBatchTest(dispatcher, ownerA, "runbook_run_cancel", runbook.RunRequest{RunID: "run-a1"})
	if state := decodeExecBatchTest[runbook.RunState](t, canceled); state.Status != runbook.RunCanceled {
		t.Fatalf("canceled run state = %+v", state)
	}
	decodeExecBatchTest[runbook.RunState](t, runbookIPCStartRun(t, dispatcher, ownerA, "run-a2", "A2"))
}

func TestRunbookIPCRetainedRunsEvictLRUAndTTL(t *testing.T) {
	service := newRunbookIPCService(newRunbookIPCExecutor())
	now := time.Unix(1000, 0)
	service.now = func() time.Time { return now }
	service.maxRetainedRunsPerOwner = 2
	service.retainedRunTTL = time.Hour
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}
	owner := ipc.WithUserID(t.Context(), "owner-a")

	complete := func(runID string) {
		t.Helper()
		runbookIPCStartRun(t, dispatcher, owner, runID, runID)
		if state := runbookIPCCompleteRun(t, dispatcher, owner, runID); state.Status != runbook.RunSuccess {
			t.Fatalf("%s final state = %+v", runID, state)
		}
	}
	complete("run-r1")
	now = now.Add(time.Minute)
	complete("run-r2")
	now = now.Add(time.Minute)
	complete("run-r3")
	now = now.Add(time.Minute)
	decodeExecBatchTest[runbook.RunState](t, runbookIPCGetRun(t, dispatcher, owner, "run-r1"))

	now = now.Add(time.Minute)
	runbookIPCStartRun(t, dispatcher, owner, "run-r4", "run-r4")
	requireRunbookIPCNotFound(t, runbookIPCGetRun(t, dispatcher, owner, "run-r2"))
	decodeExecBatchTest[runbook.RunState](t, runbookIPCGetRun(t, dispatcher, owner, "run-r1"))
	if state := runbookIPCCompleteRun(t, dispatcher, owner, "run-r4"); state.Status != runbook.RunSuccess {
		t.Fatalf("run-r4 final state = %+v", state)
	}

	now = now.Add(2 * time.Hour)
	runbookIPCStartRun(t, dispatcher, owner, "run-r5", "run-r5")
	for _, runID := range []string{"run-r1", "run-r3", "run-r4"} {
		requireRunbookIPCNotFound(t, runbookIPCGetRun(t, dispatcher, owner, runID))
	}
	decodeExecBatchTest[runbook.RunState](t, runbookIPCGetRun(t, dispatcher, owner, "run-r5"))
}

func TestRunbookIPCEvictionSkipsActiveRuns(t *testing.T) {
	executor := newRunbookIPCExecutor("run-live:s01:g01")
	service := newRunbookIPCService(executor)
	service.maxRetainedRunsPerOwner = 0
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}
	owner := ipc.WithUserID(t.Context(), "owner-a")

	runbookIPCStartRun(t, dispatcher, owner, "run-live", "live")
	advance := runbookIPCAdvanceAsync(dispatcher, owner, "run-live")
	select {
	case <-executor.started:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked run did not start executing")
	}
	runbookIPCStartRun(t, dispatcher, owner, "run-done", "done")
	if state := runbookIPCCompleteRun(t, dispatcher, owner, "run-done"); state.Status != runbook.RunSuccess {
		t.Fatalf("run-done final state = %+v", state)
	}
	runbookIPCStartRun(t, dispatcher, owner, "run-next", "next")
	decodeExecBatchTest[runbook.RunState](t, runbookIPCGetRun(t, dispatcher, owner, "run-live"))
	requireRunbookIPCNotFound(t, runbookIPCGetRun(t, dispatcher, owner, "run-done"))

	canceled := dispatchExecBatchTest(dispatcher, owner, "runbook_run_cancel", runbook.RunRequest{RunID: "run-live"})
	decodeExecBatchTest[runbook.RunState](t, canceled)
	select {
	case response := <-advance:
		if state := decodeExecBatchTest[runbook.RunState](t, response); state.Status != runbook.RunCanceled {
			t.Fatalf("blocked run state = %+v", state)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked run did not finish after cancel")
	}
}

func TestRunbookIPCAccountCleanupDropsOnlyOwnerRuns(t *testing.T) {
	executor := newRunbookIPCExecutor("run-live:s01:g01", "run-b-live:s01:g01")
	service := newRunbookIPCService(executor)
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	ownerB := ipc.WithUserID(t.Context(), "owner-b")

	runbookIPCStartRun(t, dispatcher, ownerA, "run-live", "live")
	advanceA := runbookIPCAdvanceAsync(dispatcher, ownerA, "run-live")
	runbookIPCStartRun(t, dispatcher, ownerA, "run-done", "done")
	if state := runbookIPCCompleteRun(t, dispatcher, ownerA, "run-done"); state.Status != runbook.RunSuccess {
		t.Fatalf("run-done final state = %+v", state)
	}
	runbookIPCStartRun(t, dispatcher, ownerB, "run-b-live", "b-live")
	advanceB := runbookIPCAdvanceAsync(dispatcher, ownerB, "run-b-live")
	for range 2 {
		select {
		case <-executor.started:
		case <-time.After(2 * time.Second):
			t.Fatal("blocked runs did not start executing")
		}
	}

	service.CancelOwner(context.WithoutCancel(t.Context()), "owner-a", "logout-all", nil)
	select {
	case response := <-advanceA:
		if state := decodeExecBatchTest[runbook.RunState](t, response); state.Status != runbook.RunCanceled {
			t.Fatalf("owner A live run state = %+v", state)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owner cleanup did not cancel the live run")
	}
	requireRunbookIPCNotFound(t, runbookIPCGetRun(t, dispatcher, ownerA, "run-live"))
	requireRunbookIPCNotFound(t, runbookIPCGetRun(t, dispatcher, ownerA, "run-done"))
	decodeExecBatchTest[runbook.RunState](t, runbookIPCGetRun(t, dispatcher, ownerB, "run-b-live"))

	commit, rollback := service.PrepareOwnerCleanup(t.Context(), "owner-b", "logout-all", nil)
	rollback()
	decodeExecBatchTest[runbook.RunState](t, runbookIPCGetRun(t, dispatcher, ownerB, "run-b-live"))
	commit()
	requireRunbookIPCNotFound(t, runbookIPCGetRun(t, dispatcher, ownerB, "run-b-live"))
	select {
	case response := <-advanceB:
		if state := decodeExecBatchTest[runbook.RunState](t, response); state.Status != runbook.RunCanceled {
			t.Fatalf("owner B live run state after commit = %+v", state)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("committed cleanup did not cancel owner B's live run")
	}
}

func TestRunbookIPCShutdownCancelsActiveRunsAndClearsRegistry(t *testing.T) {
	executor := newRunbookIPCExecutor("run-a:s01:g01", "run-b:s01:g01")
	service := newRunbookIPCService(executor)
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	ownerB := ipc.WithUserID(t.Context(), "owner-b")

	runbookIPCStartRun(t, dispatcher, ownerA, "run-a", "A")
	advanceA := runbookIPCAdvanceAsync(dispatcher, ownerA, "run-a")
	runbookIPCStartRun(t, dispatcher, ownerB, "run-b", "B")
	advanceB := runbookIPCAdvanceAsync(dispatcher, ownerB, "run-b")
	for range 2 {
		select {
		case <-executor.started:
		case <-time.After(2 * time.Second):
			t.Fatal("blocked runs did not start executing")
		}
	}

	if err := service.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	for name, advance := range map[string]<-chan ipc.Response{"run-a": advanceA, "run-b": advanceB} {
		select {
		case response := <-advance:
			if state := decodeExecBatchTest[runbook.RunState](t, response); state.Status != runbook.RunCanceled {
				t.Fatalf("%s state after shutdown = %+v", name, state)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s did not finish after shutdown", name)
		}
	}
	requireRunbookIPCNotFound(t, runbookIPCGetRun(t, dispatcher, ownerA, "run-a"))
	requireRunbookIPCNotFound(t, runbookIPCGetRun(t, dispatcher, ownerB, "run-b"))
	if err := service.Shutdown(t.Context()); err != nil {
		t.Fatalf("second shutdown = %v", err)
	}
}

func TestRunbookIPCEvictedRunKeepsDurableHistory(t *testing.T) {
	transport := newExecBatchTestTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
		code := 0
		return base.ExecResult{ExitCode: &code}, nil
	})
	manager := newExecBatchTestManager(t, transport)
	owner := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(owner, session.Asset{ID: "asset-history", Name: "history", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	batches := newExecBatchService(manager, execBatchTestAssets(nil), nil, database)
	service := newRunbookIPCService(batches, database)
	service.maxRetainedRunsPerOwner = 0
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}

	start := dispatchExecBatchTest(dispatcher, owner, "runbook_run_start", runbook.StartRequest{
		RunID: "run-history", RunbookID: "rb-history", RunbookName: "history",
		Steps:      []runbook.Step{{ID: "s1", Name: "S1", Command: "do"}},
		SessionIDs: []string{connected.ID},
	})
	decodeExecBatchTest[runbook.RunState](t, start)
	prepare := dispatchExecBatchTest(dispatcher, owner, "runbook_run_prepare", runbook.PrepareRequest{
		RunID: "run-history", StepNumber: 1,
		Commands: []runbook.RenderedCommand{{SessionID: connected.ID, Command: "do"}},
	})
	decodeExecBatchTest[runbook.RunState](t, prepare)
	advance := dispatchExecBatchTest(dispatcher, owner, "runbook_run_advance", runbook.AdvanceRequest{RunID: "run-history", StepNumber: 1, Confirmed: true})
	if state := decodeExecBatchTest[runbook.RunState](t, advance); state.Status != runbook.RunSuccess {
		t.Fatalf("run-history final state = %+v", state)
	}

	runbookIPCStartRun(t, dispatcher, owner, "run-evictor", "evictor")
	requireRunbookIPCNotFound(t, runbookIPCGetRun(t, dispatcher, owner, "run-history"))

	historyResponse := dispatchExecBatchTest(dispatcher, owner, "runbook_run_history_get", runbook.RunRequest{RunID: "run-history"})
	history := decodeExecBatchTest[store.ExecBatchRunHistory](t, historyResponse)
	if history.RunbookID != "rb-history" || history.Status != store.ExecBatchStatusSuccess || len(history.Batches) != 1 {
		t.Fatalf("durable history after live eviction = %+v", history)
	}
}

func TestRunbookIPCConcurrentOwnersLifecycle(t *testing.T) {
	service := newRunbookIPCService(newRunbookIPCExecutor())
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}
	owners := map[string]string{"owner-a": "runbook A", "owner-b": "runbook B"}
	var workers sync.WaitGroup
	errs := make(chan error, len(owners)*2)
	for ownerID, name := range owners {
		workers.Add(1)
		go func() {
			defer workers.Done()
			owner := ipc.WithUserID(t.Context(), ownerID)
			start := runbookIPCStartRun(t, dispatcher, owner, "race-run", name)
			state, err := decodeExecBatchTestErr[runbook.RunState](start)
			if err != nil {
				errs <- err
				return
			}
			if state.RunbookName != name {
				errs <- fmt.Errorf("%s start observed %q", ownerID, state.RunbookName)
				return
			}
			prepare := dispatchExecBatchTest(dispatcher, owner, "runbook_run_prepare", runbook.PrepareRequest{
				RunID: "race-run", StepNumber: 1,
				Commands: []runbook.RenderedCommand{{SessionID: "session-1", Command: "do"}},
			})
			if _, err := decodeExecBatchTestErr[runbook.RunState](prepare); err != nil {
				errs <- err
				return
			}
			advance := dispatchExecBatchTest(dispatcher, owner, "runbook_run_advance", runbook.AdvanceRequest{RunID: "race-run", StepNumber: 1, Confirmed: true})
			final, err := decodeExecBatchTestErr[runbook.RunState](advance)
			if err != nil {
				errs <- err
				return
			}
			if final.Status != runbook.RunSuccess || final.RunbookName != name {
				errs <- fmt.Errorf("%s final state = %+v", ownerID, final)
			}
		}()
	}
	workers.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func decodeExecBatchTestErr[T any](response ipc.Response) (T, error) {
	var value T
	if !response.OK {
		return value, fmt.Errorf("dispatch failed: %+v", response)
	}
	if err := json.Unmarshal(response.Data, &value); err != nil {
		return value, err
	}
	return value, nil
}

func TestRunbookIPCProductionShutdownCancelsLiveRuns(t *testing.T) {
	started := make(chan context.Context, 1)
	transport := newExecBatchTestTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
		started <- ctx
		<-ctx.Done()
		return base.ExecResult{}, ctx.Err()
	})
	manager := newExecBatchTestManager(t, transport)
	storage, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	application, err := NewProductionWithServices(Config{}, ProductionServices{Sessions: manager, Store: storage})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	owner := ipc.WithUserID(t.Context(), "owner-a")
	asset, err := storage.AssetCreate(owner, store.AssetInput{Kind: session.KindSSH, Name: "asset-live"})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := manager.ConnectFor(owner, session.Asset{ID: asset.ID, Name: "asset-live", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := application.Dispatcher
	start := dispatchExecBatchTest(dispatcher, owner, "runbook_run_start", runbook.StartRequest{
		RunID: "run-shutdown", RunbookID: "rb-shutdown", RunbookName: "shutdown",
		Steps:      []runbook.Step{{ID: "s1", Name: "S1", Command: "do"}},
		SessionIDs: []string{connected.ID},
	})
	decodeExecBatchTest[runbook.RunState](t, start)
	prepare := dispatchExecBatchTest(dispatcher, owner, "runbook_run_prepare", runbook.PrepareRequest{
		RunID: "run-shutdown", StepNumber: 1,
		Commands: []runbook.RenderedCommand{{SessionID: connected.ID, Command: "do"}},
	})
	decodeExecBatchTest[runbook.RunState](t, prepare)
	advance := make(chan ipc.Response, 1)
	go func() {
		advance <- dispatchExecBatchTest(dispatcher, owner, "runbook_run_advance", runbook.AdvanceRequest{RunID: "run-shutdown", StepNumber: 1, Confirmed: true})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("live run did not start executing")
	}

	if err := application.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-advance:
		if state := decodeExecBatchTest[runbook.RunState](t, response); state.Status != runbook.RunCanceled {
			t.Fatalf("live run state after production shutdown = %+v", state)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("production shutdown did not cancel the live run")
	}
}

func TestRunbookIPCPostTerminalOperationsKeepSlotReleasedAndEvictable(t *testing.T) {
	service := newRunbookIPCService(newRunbookIPCExecutor())
	service.maxActiveRunsPerOwner = 1
	service.maxRetainedRunsPerOwner = 0
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}
	owner := ipc.WithUserID(t.Context(), "owner-a")

	runbookIPCStartRun(t, dispatcher, owner, "run-r1", "r1")
	if state := runbookIPCCompleteRun(t, dispatcher, owner, "run-r1"); state.Status != runbook.RunSuccess {
		t.Fatalf("run-r1 final state = %+v", state)
	}

	postTerminal := dispatchExecBatchTest(dispatcher, owner, "runbook_run_prepare", runbook.PrepareRequest{
		RunID: "run-r1", StepNumber: 1,
		Commands: []runbook.RenderedCommand{{SessionID: "session-1", Command: "do"}},
	})
	if postTerminal.OK || postTerminal.Error == nil || postTerminal.Error.Code != ipc.CodeBadParam {
		t.Fatalf("post-terminal prepare = %+v, want invalid-state error", postTerminal)
	}
	postAdvance := dispatchExecBatchTest(dispatcher, owner, "runbook_run_advance", runbook.AdvanceRequest{RunID: "run-r1", StepNumber: 1, Confirmed: true})
	if postAdvance.OK || postAdvance.Error == nil || postAdvance.Error.Code != ipc.CodeBadParam {
		t.Fatalf("post-terminal advance = %+v, want invalid-state error", postAdvance)
	}

	decodeExecBatchTest[runbook.RunState](t, runbookIPCStartRun(t, dispatcher, owner, "run-r2", "r2"))
	requireRunbookIPCNotFound(t, runbookIPCGetRun(t, dispatcher, owner, "run-r1"))
	if state := runbookIPCCompleteRun(t, dispatcher, owner, "run-r2"); state.Status != runbook.RunSuccess {
		t.Fatalf("run-r2 final state = %+v", state)
	}
}

func TestRunbookIPCTerminalStateSurvivesStaleAndFailedResponses(t *testing.T) {
	service := newRunbookIPCService(newRunbookIPCExecutor())
	ownerID, runID := "owner-a", "run-mono"
	service.mu.Lock()
	entry := &runbookRunEntry{runs: runbook.New(service.executor), lastAccess: time.Now()}
	service.runs[runbookRunKey{ownerID: ownerID, runID: runID}] = entry
	service.mu.Unlock()

	terminal := func(context.Context, *runbook.Service) (runbook.RunState, error) {
		return runbook.RunState{Status: runbook.RunSuccess}, nil
	}
	if _, err := service.withRun(t.Context(), ownerID, runID, terminal); err != nil {
		t.Fatal(err)
	}

	stale := func(context.Context, *runbook.Service) (runbook.RunState, error) {
		return runbook.RunState{Status: runbook.RunAwaitingRender}, nil
	}
	if _, err := service.withRun(t.Context(), ownerID, runID, stale); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	observed := entry.terminal
	service.mu.Unlock()
	if !observed {
		t.Fatal("stale non-terminal response reverted a terminal entry to active")
	}

	failed := func(context.Context, *runbook.Service) (runbook.RunState, error) {
		return runbook.RunState{}, fmt.Errorf("injected post-terminal failure")
	}
	if _, err := service.withRun(t.Context(), ownerID, runID, failed); err == nil {
		t.Fatal("expected injected failure")
	}
	service.mu.Lock()
	observed = entry.terminal
	service.mu.Unlock()
	if !observed {
		t.Fatal("failed post-terminal response reverted a terminal entry to active")
	}
}

func TestRunbookIPCTerminalStateConcurrentResponsesNeverRevert(t *testing.T) {
	service := newRunbookIPCService(newRunbookIPCExecutor())
	ownerID, runID := "owner-a", "run-race"
	service.mu.Lock()
	entry := &runbookRunEntry{runs: runbook.New(service.executor), lastAccess: time.Now()}
	service.runs[runbookRunKey{ownerID: ownerID, runID: runID}] = entry
	service.mu.Unlock()

	responses := []func(context.Context, *runbook.Service) (runbook.RunState, error){
		func(context.Context, *runbook.Service) (runbook.RunState, error) {
			return runbook.RunState{Status: runbook.RunSuccess}, nil
		},
		func(context.Context, *runbook.Service) (runbook.RunState, error) {
			return runbook.RunState{Status: runbook.RunAwaitingRender}, nil
		},
		func(context.Context, *runbook.Service) (runbook.RunState, error) {
			return runbook.RunState{}, fmt.Errorf("injected racing failure")
		},
		func(context.Context, *runbook.Service) (runbook.RunState, error) {
			return runbook.RunState{Status: runbook.RunCanceled}, nil
		},
	}
	var workers sync.WaitGroup
	for _, response := range responses {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, _ = service.withRun(t.Context(), ownerID, runID, response)
		}()
	}
	workers.Wait()
	service.mu.Lock()
	observed := entry.terminal
	service.mu.Unlock()
	if !observed {
		t.Fatal("concurrent responses reverted a terminal entry to active")
	}
}

// A reservation pins the entry pointers: after the reserved entry is replaced
// (same owner/runID re-registered), the stale commit must not delete or cancel
// the replacement or its active batch.
func TestRunbookIPCPrepareOwnerCleanupSkipsReplacedRuns(t *testing.T) {
	executor := newRunbookIPCExecutor("run-aba:s01:g01")
	service := newRunbookIPCService(executor)
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}
	ownerA := ipc.WithUserID(t.Context(), "owner-a")

	runbookIPCStartRun(t, dispatcher, ownerA, "run-aba", "aba")
	staleCommit, _ := service.PrepareOwnerCleanup(t.Context(), "owner-a", "event-stale", nil)

	// The reserved entry is replaced: a second reservation commits and cancels it.
	freshCommit, _ := service.PrepareOwnerCleanup(t.Context(), "owner-a", "event-fresh", nil)
	freshCommit()

	// A new run with the same ID starts and reaches an active (blocked) batch.
	runbookIPCStartRun(t, dispatcher, ownerA, "run-aba", "aba")
	advance := runbookIPCAdvanceAsync(dispatcher, ownerA, "run-aba")
	select {
	case <-executor.started:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement run did not start executing")
	}

	// The stale reservation must not touch the replacement or its batch.
	staleCommit()
	decodeExecBatchTest[runbook.RunState](t, runbookIPCGetRun(t, dispatcher, ownerA, "run-aba"))
	select {
	case response := <-advance:
		state := decodeExecBatchTest[runbook.RunState](t, response)
		t.Fatalf("stale commit canceled the replacement run: %+v", state)
	case <-time.After(50 * time.Millisecond):
	}

	finalCommit, _ := service.PrepareOwnerCleanup(t.Context(), "owner-a", "event-final", nil)
	finalCommit()
	select {
	case response := <-advance:
		if state := decodeExecBatchTest[runbook.RunState](t, response); state.Status != runbook.RunCanceled {
			t.Fatalf("replacement run final state = %+v, want canceled", state)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replacement run did not finish")
	}
}

// The cutoff carries millisecond resolution; a run started 500µs into the
// cutoff millisecond belongs to the terminal generation and is canceled.
func TestRunbookIPCPrepareOwnerCleanupSameMillisecondInclusive(t *testing.T) {
	base := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	service := newRunbookIPCService(newRunbookIPCExecutor())
	service.now = func() time.Time { return base.Add(500 * time.Microsecond) }
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	runbookIPCStartRun(t, dispatcher, ownerA, "run-sub-ms", "sub-ms")

	cutoff := base
	commit, _ := service.PrepareOwnerCleanup(t.Context(), "owner-a", "event", &cutoff)
	commit()
	requireRunbookIPCNotFound(t, runbookIPCGetRun(t, dispatcher, ownerA, "run-sub-ms"))
}
