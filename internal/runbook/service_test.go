package runbook

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type fakeExecutor struct {
	mu       sync.Mutex
	requests []execbatch.Request
	cancels  []string
	execute  func(int, execbatch.Request) (execbatch.Batch, error)
	cancel   func(string)
}

func (f *fakeExecutor) Execute(_ context.Context, _ string, request execbatch.Request) (execbatch.Batch, error) {
	f.mu.Lock()
	f.requests = append(f.requests, cloneRequest(request))
	call := len(f.requests)
	fn := f.execute
	f.mu.Unlock()
	if fn != nil {
		return fn(call, request)
	}
	return successfulBatch(request), nil
}

func (f *fakeExecutor) Cancel(_ context.Context, _ string, batchID string) (execbatch.CancelResult, error) {
	f.mu.Lock()
	f.cancels = append(f.cancels, batchID)
	fn := f.cancel
	f.mu.Unlock()
	if fn != nil {
		fn(batchID)
	}
	return execbatch.CancelResult{BatchID: batchID, Canceled: true}, nil
}

func (f *fakeExecutor) Requests() []execbatch.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]execbatch.Request, len(f.requests))
	for index, request := range f.requests {
		result[index] = cloneRequest(request)
	}
	return result
}

func cloneRequest(request execbatch.Request) execbatch.Request {
	request.SessionIDs = append([]string(nil), request.SessionIDs...)
	if request.Runbook != nil {
		metadata := *request.Runbook
		request.Runbook = &metadata
	}
	if request.ProductionConfirmation != nil {
		confirmation := *request.ProductionConfirmation
		request.ProductionConfirmation = &confirmation
	}
	return request
}

func successfulBatch(request execbatch.Request) execbatch.Batch {
	return batchWithStatus(request, execbatch.StatusSuccess)
}

func batchWithStatus(request execbatch.Request, status execbatch.Status) execbatch.Batch {
	results := make([]execbatch.Result, len(request.SessionIDs))
	for index, sessionID := range request.SessionIDs {
		code := 0
		if status == execbatch.StatusExitError {
			code = 17
		}
		results[index] = execbatch.Result{
			SessionID: sessionID, SessionName: "name-" + sessionID, Status: status,
			Stdout: request.Command, ExitCode: &code,
		}
	}
	return execbatch.Batch{BatchID: request.BatchID, Command: request.Command, StartedAt: 1, FinishedAt: 2, Results: results}
}

func testSteps() []Step {
	return []Step{
		{ID: "step-one", Name: "one", Command: "echo {{asset.name}}"},
		{ID: "step-two", Name: "two", Command: "true"},
	}
}

func startTestRun(t *testing.T, service *Service, steps []Step, targets []string, policy ConfirmationPolicy, stop *bool) RunState {
	t.Helper()
	state, err := service.Start(t.Context(), "owner-a", StartRequest{
		RunID: "run-1", RunbookID: "runbook-1", RunbookName: "Deploy", Steps: steps,
		SessionIDs: targets, ConfirmationPolicy: policy, StopOnFailure: stop,
		TimeoutMS: 12000, MaxConcurrent: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func prepareTestStep(t *testing.T, service *Service, step int32, commands ...RenderedCommand) RunState {
	t.Helper()
	state, err := service.Prepare(t.Context(), "owner-a", PrepareRequest{RunID: "run-1", StepNumber: step, Commands: commands})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func advanceTestStep(t *testing.T, service *Service, step int32, confirmed bool) RunState {
	t.Helper()
	state, err := service.Advance(t.Context(), "owner-a", AdvanceRequest{RunID: "run-1", StepNumber: step, Confirmed: confirmed})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestRunRequiresConfirmationAtEveryStepAndPreservesOrder(t *testing.T) {
	executor := &fakeExecutor{}
	service := New(executor)
	state := startTestRun(t, service, testSteps(), []string{"s1", "s2"}, "", nil)
	if state.Status != RunAwaitingRender || state.CurrentStep != 1 || !state.StopOnFailure || state.ConfirmationPolicy != ConfirmEveryStep {
		t.Fatalf("initial state = %+v", state)
	}
	commands := []RenderedCommand{{SessionID: "s1", Command: "echo one"}, {SessionID: "s2", Command: "echo one"}}
	prepareTestStep(t, service, 1, commands...)
	if _, err := service.Advance(t.Context(), "owner-a", AdvanceRequest{RunID: "run-1", StepNumber: 1}); !errors.Is(err, ErrStepConfirmationRequired) {
		t.Fatalf("unconfirmed advance error = %v", err)
	}
	if got := len(executor.Requests()); got != 0 {
		t.Fatalf("executed %d batches before confirmation", got)
	}
	state = advanceTestStep(t, service, 1, true)
	if state.Status != RunAwaitingRender || state.CurrentStep != 2 || state.Steps[0].Status != StepSuccess {
		t.Fatalf("after step one = %+v", state)
	}
	prepareTestStep(t, service, 2, RenderedCommand{SessionID: "s1", Command: "echo two"}, RenderedCommand{SessionID: "s2", Command: "echo two"})
	if _, err := service.Advance(t.Context(), "owner-a", AdvanceRequest{RunID: "run-1", StepNumber: 2}); !errors.Is(err, ErrStepConfirmationRequired) {
		t.Fatalf("second unconfirmed advance error = %v", err)
	}
	state = advanceTestStep(t, service, 2, true)
	if state.Status != RunSuccess || state.Steps[1].Status != StepSuccess {
		t.Fatalf("final state = %+v", state)
	}
	requests := executor.Requests()
	if len(requests) != 2 || requests[0].Command != "echo one" || requests[1].Command != "echo two" {
		t.Fatalf("execution order = %+v", requests)
	}
	for index, request := range requests {
		if request.Runbook == nil || request.Runbook.RunbookID != "runbook-1" || request.Runbook.RunID != "run-1" ||
			request.Runbook.StepNumber != int32(index+1) || request.Runbook.StepCount != 2 {
			t.Fatalf("request %d runbook metadata = %+v", index, request.Runbook)
		}
		if request.TimeoutMS != 12000 || request.MaxConcurrent != 3 {
			t.Fatalf("request %d execution settings = %+v", index, request)
		}
	}
}

func TestPreparedCommandsAreConfirmedVerbatimAndGroupedWithoutLosingAttribution(t *testing.T) {
	executor := &fakeExecutor{}
	service := New(executor)
	startTestRun(t, service, testSteps()[:1], []string{"s1", "s2", "s3"}, ConfirmEveryStep, nil)
	state := prepareTestStep(t, service, 1,
		RenderedCommand{SessionID: "s3", Command: "echo same"},
		RenderedCommand{SessionID: "s2", Command: "echo other"},
		RenderedCommand{SessionID: "s1", Command: "echo same"},
	)
	for index, rendered := range state.Steps[0].RenderedCommands {
		if rendered.SessionID != state.SessionIDs[index] {
			t.Fatalf("rendered order = %+v", state.Steps[0].RenderedCommands)
		}
	}
	state = advanceTestStep(t, service, 1, true)
	requests := executor.Requests()
	if len(requests) != 2 || requests[0].Command != "echo same" || strings.Join(requests[0].SessionIDs, ",") != "s1,s3" ||
		requests[1].Command != "echo other" || strings.Join(requests[1].SessionIDs, ",") != "s2" {
		t.Fatalf("grouped requests = %+v", requests)
	}
	for index, request := range requests {
		if request.Runbook == nil || request.Runbook.GroupNumber != int32(index+1) || request.Runbook.GroupCount != 2 {
			t.Fatalf("group %d metadata = %+v", index, request.Runbook)
		}
	}
	if len(state.Steps[0].Results) != 3 {
		t.Fatalf("results = %+v", state.Steps[0].Results)
	}
	for index, result := range state.Steps[0].Results {
		if result.SessionID != state.SessionIDs[index] {
			t.Fatalf("result attribution order = %+v", state.Steps[0].Results)
		}
		wantCommand := "echo same"
		if result.SessionID == "s2" {
			wantCommand = "echo other"
		}
		if result.Stdout != wantCommand {
			t.Fatalf("result %s came from %q, want %q", result.SessionID, result.Stdout, wantCommand)
		}
	}
}

func TestStopOnFailurePolicy(t *testing.T) {
	for _, test := range []struct {
		name string
		stop bool
	}{
		{name: "stop after failed step", stop: true},
		{name: "continue after failed step", stop: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor := &fakeExecutor{execute: func(_ int, request execbatch.Request) (execbatch.Batch, error) {
				if request.Command == "fail" {
					return batchWithStatus(request, execbatch.StatusExitError), nil
				}
				return successfulBatch(request), nil
			}}
			service := New(executor)
			startTestRun(t, service, testSteps(), []string{"s1", "s2"}, ConfirmEveryStep, &test.stop)
			prepareTestStep(t, service, 1, RenderedCommand{SessionID: "s1", Command: "fail"}, RenderedCommand{SessionID: "s2", Command: "ok"})
			state := advanceTestStep(t, service, 1, true)
			if len(executor.Requests()) != 2 {
				t.Fatalf("step did not collect all command groups: %+v", executor.Requests())
			}
			if state.Steps[0].Results[0].Status != execbatch.StatusExitError || state.Steps[0].Results[1].Status != execbatch.StatusSuccess {
				t.Fatalf("failure attribution = %+v", state.Steps[0].Results)
			}
			if test.stop {
				if state.Status != RunFailed || state.CurrentStep != 1 || state.Steps[1].Status != StepSkipped {
					t.Fatalf("stopped state = %+v", state)
				}
				return
			}
			if state.Status != RunAwaitingRender || state.CurrentStep != 2 {
				t.Fatalf("continued state = %+v", state)
			}
			prepareTestStep(t, service, 2, RenderedCommand{SessionID: "s1", Command: "last"}, RenderedCommand{SessionID: "s2", Command: "last"})
			state = advanceTestStep(t, service, 2, true)
			if state.Status != RunFailed || state.Steps[1].Status != StepSuccess || len(executor.Requests()) != 3 {
				t.Fatalf("completed with earlier failure = %+v, requests = %+v", state, executor.Requests())
			}
		})
	}
}

func TestProductionOnlyPolicyStillPausesForBackendChallengeAndRetriesSameCommand(t *testing.T) {
	challenge := &execbatch.ConfirmationRequiredError{
		BatchID: "run-1:s01:g01", TargetSetHash: strings.Repeat("a", 64), CommandHash: strings.Repeat("b", 64),
		ProductionTargets: []execbatch.ConfirmationTarget{{SessionID: "s1", SessionName: "prod", Environment: "production"}},
	}
	executor := &fakeExecutor{execute: func(call int, request execbatch.Request) (execbatch.Batch, error) {
		if call == 1 {
			return execbatch.Batch{}, challenge
		}
		return successfulBatch(request), nil
	}}
	service := New(executor)
	startTestRun(t, service, testSteps()[:1], []string{"s1"}, ConfirmProductionOnly, nil)
	prepareTestStep(t, service, 1, RenderedCommand{SessionID: "s1", Command: "deploy rendered"})
	state := advanceTestStep(t, service, 1, false)
	if state.Status != RunAwaitingProductionConfirmation || state.ProductionCommand != "deploy rendered" || state.ProductionChallenge == nil {
		t.Fatalf("production pause = %+v", state)
	}
	if _, err := service.Advance(t.Context(), "owner-a", AdvanceRequest{RunID: "run-1", StepNumber: 1}); !errors.Is(err, ErrProductionConfirmationNeeded) {
		t.Fatalf("missing production confirmation error = %v", err)
	}
	confirmation := &execbatch.ProductionConfirmation{
		Confirmed: true, TargetSetHash: challenge.TargetSetHash, CommandHash: challenge.CommandHash,
	}
	state, err := service.Advance(t.Context(), "owner-a", AdvanceRequest{
		RunID: "run-1", StepNumber: 1, ProductionConfirmation: confirmation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != RunSuccess {
		t.Fatalf("confirmed state = %+v", state)
	}
	requests := executor.Requests()
	if len(requests) != 2 || requests[0].BatchID != requests[1].BatchID || requests[0].Command != requests[1].Command ||
		!productionConfirmationMatches(requests[1].ProductionConfirmation, confirmation) {
		t.Fatalf("production retry = %+v", requests)
	}
}

func productionConfirmationMatches(got, want *execbatch.ProductionConfirmation) bool {
	if got == nil || want == nil {
		return got == want
	}
	return got.Confirmed == want.Confirmed && got.TargetSetHash == want.TargetSetHash &&
		got.CommandHash == want.CommandHash && slices.Equal(got.EnvironmentNames, want.EnvironmentNames)
}

func TestCancelStopsCurrentBatchAndAllLaterWork(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	executor := &fakeExecutor{}
	executor.execute = func(_ int, request execbatch.Request) (execbatch.Batch, error) {
		close(started)
		<-release
		return batchWithStatus(request, execbatch.StatusCanceled), nil
	}
	executor.cancel = func(string) {
		releaseOnce.Do(func() { close(release) })
	}
	service := New(executor)
	startTestRun(t, service, testSteps(), []string{"s1", "s2"}, ConfirmEveryStep, nil)
	prepareTestStep(t, service, 1, RenderedCommand{SessionID: "s1", Command: "wait one"}, RenderedCommand{SessionID: "s2", Command: "wait two"})
	type outcome struct {
		state RunState
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		state, err := service.Advance(context.Background(), "owner-a", AdvanceRequest{RunID: "run-1", StepNumber: 1, Confirmed: true})
		done <- outcome{state: state, err: err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("execution did not start")
	}
	if _, err := service.Cancel(t.Context(), "owner-a", "run-1"); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.state.Status != RunCanceled || result.state.Steps[0].Status != StepCanceled || result.state.Steps[1].Status != StepSkipped {
			t.Fatalf("canceled state = %+v", result.state)
		}
		if len(result.state.Steps[0].Results) != 2 {
			t.Fatalf("canceled target matrix = %+v", result.state.Steps[0].Results)
		}
		for _, target := range result.state.Steps[0].Results {
			if target.Status != execbatch.StatusCanceled {
				t.Fatalf("canceled target = %+v", target)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("advance did not finish after cancel")
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.cancels) != 1 || executor.cancels[0] != "run-1:s01:g01" || len(executor.requests) != 1 {
		t.Fatalf("cancel advanced work: cancels = %v, requests = %+v", executor.cancels, executor.requests)
	}
}

func TestCancelWinsWhenExecutionReturnsChallengeOrError(t *testing.T) {
	challenge := &execbatch.ConfirmationRequiredError{
		BatchID: "run-1:s01:g01", TargetSetHash: strings.Repeat("a", 64), CommandHash: strings.Repeat("b", 64),
		ProductionTargets: []execbatch.ConfirmationTarget{{SessionID: "s1", Environment: "production"}},
	}
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "production challenge", err: challenge},
		{name: "execution error", err: errors.New("injected execution error")},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			executor := &fakeExecutor{execute: func(_ int, _ execbatch.Request) (execbatch.Batch, error) {
				close(started)
				<-release
				return execbatch.Batch{}, test.err
			}}
			service := New(executor)
			startTestRun(t, service, testSteps()[:1], []string{"s1"}, ConfirmEveryStep, nil)
			prepareTestStep(t, service, 1, RenderedCommand{SessionID: "s1", Command: "deploy"})
			done := make(chan RunState, 1)
			go func() {
				state, _ := service.Advance(context.Background(), "owner-a", AdvanceRequest{RunID: "run-1", StepNumber: 1, Confirmed: true})
				done <- state
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("execution did not start")
			}
			if _, err := service.Cancel(t.Context(), "owner-a", "run-1"); err != nil {
				t.Fatal(err)
			}
			close(release)
			select {
			case state := <-done:
				if state.Status != RunCanceled || state.ProductionChallenge != nil {
					t.Fatalf("cancel lost to execution outcome: %+v", state)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("advance did not finish")
			}
		})
	}
}

func TestPreflightFailureContinuesGroupsAndStepsWhenStopOnFailureDisabled(t *testing.T) {
	stop := false
	executor := &fakeExecutor{execute: func(_ int, request execbatch.Request) (execbatch.Batch, error) {
		if request.Command == "preflight" {
			return execbatch.Batch{}, &execbatch.PreflightError{Failures: []execbatch.TargetFailure{{SessionID: "s1", Err: execbatch.ErrDisconnected}}}
		}
		return successfulBatch(request), nil
	}}
	service := New(executor)
	startTestRun(t, service, testSteps(), []string{"s1", "s2"}, ConfirmEveryStep, &stop)
	prepareTestStep(t, service, 1, RenderedCommand{SessionID: "s1", Command: "preflight"}, RenderedCommand{SessionID: "s2", Command: "ok"})
	state := advanceTestStep(t, service, 1, true)
	if state.Status != RunAwaitingRender || state.CurrentStep != 2 || state.Steps[0].Status != StepFailed {
		t.Fatalf("preflight did not advance to step two: %+v", state)
	}
	if len(executor.Requests()) != 2 || state.Steps[0].Results[0].Status != execbatch.StatusTransportError ||
		state.Steps[0].Results[1].Status != execbatch.StatusSuccess {
		t.Fatalf("preflight group results = %+v, requests = %+v", state.Steps[0].Results, executor.Requests())
	}
	prepareTestStep(t, service, 2, RenderedCommand{SessionID: "s1", Command: "last"}, RenderedCommand{SessionID: "s2", Command: "last"})
	state = advanceTestStep(t, service, 2, true)
	requests := executor.Requests()
	if state.Status != RunFailed || state.Steps[1].Status != StepSuccess || len(requests) != 3 {
		t.Fatalf("final preflight state = %+v, requests = %+v", state, requests)
	}
	if requests[0].Command != "preflight" || requests[1].Command != "ok" || requests[2].Command != "last" {
		t.Fatalf("execution order = %+v", requests)
	}
}

func TestFatalExecutionErrorStopsEvenWhenStopOnFailureDisabled(t *testing.T) {
	stop := false
	executor := &fakeExecutor{execute: func(_ int, _ execbatch.Request) (execbatch.Batch, error) {
		return execbatch.Batch{}, errors.New("injected fatal error")
	}}
	service := New(executor)
	startTestRun(t, service, testSteps(), []string{"s1"}, ConfirmEveryStep, &stop)
	prepareTestStep(t, service, 1, RenderedCommand{SessionID: "s1", Command: "fatal"})
	state := advanceTestStep(t, service, 1, true)
	if state.Status != RunFailed || state.Steps[1].Status != StepSkipped || len(executor.Requests()) != 1 {
		t.Fatalf("fatal error advanced work: %+v, requests = %+v", state, executor.Requests())
	}
}

type runbookTestTransport struct {
	calls *atomic.Int32
}

func (t runbookTestTransport) Kind() string       { return "ssh" }
func (t runbookTestTransport) Generation() uint64 { return 1 }
func (t runbookTestTransport) IsAlive() bool      { return true }
func (t runbookTestTransport) Close() error       { return nil }
func (t runbookTestTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}
func (t runbookTestTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	t.calls.Add(1)
	code := 0
	return base.ExecResult{ExitCode: &code}, nil
}

func TestDisconnectedTargetContinuesAndPersistsFailureWhenStopOnFailureDisabled(t *testing.T) {
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	var execCalls atomic.Int32
	transport := runbookTestTransport{calls: &execCalls}
	resolver := execbatch.ResolverFunc(func(_ context.Context, sessionID string) (execbatch.Target, error) {
		if sessionID == "bad" {
			return execbatch.Target{}, execbatch.ErrDisconnected
		}
		return execbatch.Target{SessionID: sessionID, SessionName: "name-" + sessionID, Transport: transport, Generation: 1}, nil
	})
	batches := execbatch.New(execbatch.Config{Resolver: resolver, History: database})
	service := New(batches)
	stop := false
	startTestRun(t, service, testSteps(), []string{"bad", "good"}, ConfirmEveryStep, &stop)
	prepareTestStep(t, service, 1, RenderedCommand{SessionID: "bad", Command: "bad-one"}, RenderedCommand{SessionID: "good", Command: "good-one"})
	state := advanceTestStep(t, service, 1, true)
	if state.Status != RunAwaitingRender || state.CurrentStep != 2 || execCalls.Load() != 1 {
		t.Fatalf("disconnected step one = %+v, exec calls = %d", state, execCalls.Load())
	}
	prepareTestStep(t, service, 2, RenderedCommand{SessionID: "bad", Command: "bad-two"}, RenderedCommand{SessionID: "good", Command: "good-two"})
	state = advanceTestStep(t, service, 2, true)
	if state.Status != RunFailed || execCalls.Load() != 2 {
		t.Fatalf("disconnected final state = %+v, exec calls = %d", state, execCalls.Load())
	}
	for _, step := range state.Steps {
		if step.Status != StepFailed || len(step.Results) != 2 || step.Results[0].Status != execbatch.StatusTransportError ||
			step.Results[1].Status != execbatch.StatusSuccess {
			t.Fatalf("disconnected result attribution = %+v", step)
		}
	}
	history, err := database.ExecBatchRunHistoryGet(ipc.WithUserID(context.Background(), "owner-a"), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if history.Status != store.ExecBatchStatusFailed || history.ReachedStep != 2 || len(history.Batches) != 4 {
		t.Fatalf("disconnected history = %+v", history)
	}
	failedBatches := 0
	successfulBatches := 0
	for _, batch := range history.Batches {
		switch batch.Batch.Status {
		case store.ExecBatchStatusFailed:
			failedBatches++
			if len(batch.Results) != 1 || batch.Results[0].SessionID != "bad" || batch.Results[0].ErrorKind != store.ExecBatchErrorTransport {
				t.Fatalf("preflight history results = %+v", batch)
			}
		case store.ExecBatchStatusSuccess:
			successfulBatches++
			if len(batch.Results) != 1 || batch.Results[0].SessionID != "good" {
				t.Fatalf("successful history results = %+v", batch)
			}
		}
	}
	if failedBatches != 2 || successfulBatches != 2 {
		t.Fatalf("history batch statuses = failed %d, success %d", failedBatches, successfulBatches)
	}
}
