package runbook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
)

type askOutcome struct {
	answer execbatch.Answer
	err    error
}

// promptingExecutor plays the exec batch service: for every session of a
// request it asks the run's Interactor in its own goroutine, like one script
// per host, and returns once all of them were answered or failed.
type promptingExecutor struct {
	fakeExecutor
	kind     execbatch.DirectiveKind
	mu2      sync.Mutex
	outcomes map[string]askOutcome
	asking   chan string
}

func newPromptingExecutor(kind execbatch.DirectiveKind, sessions ...string) *promptingExecutor {
	return &promptingExecutor{kind: kind, outcomes: map[string]askOutcome{}, asking: make(chan string, len(sessions)+4)}
}

func (e *promptingExecutor) Execute(ctx context.Context, owner string, request execbatch.Request) (execbatch.Batch, error) {
	var wg sync.WaitGroup
	for _, sessionID := range request.SessionIDs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			request.Interactor.Notify(execbatch.Event{SessionID: sessionID, SessionName: "name-" + sessionID, Kind: execbatch.DirectiveStep, Text: "Configure"})
			e.asking <- sessionID
			answer, err := request.Interactor.Ask(ctx, execbatch.Question{
				SessionID: sessionID, SessionName: "name-" + sessionID, Kind: e.kind,
				Variable: "port", Prompt: "Which port?", Default: "8080",
			})
			e.mu2.Lock()
			e.outcomes[sessionID] = askOutcome{answer, err}
			e.mu2.Unlock()
		}()
	}
	wg.Wait()
	batch := successfulBatch(request)
	e.mu2.Lock()
	defer e.mu2.Unlock()
	for index, result := range batch.Results {
		if outcome := e.outcomes[result.SessionID]; outcome.err != nil {
			batch.Results[index].Status = execbatch.StatusTransportError
			batch.Results[index].Error = outcome.err.Error()
		}
	}
	return batch, nil
}

func (e *promptingExecutor) outcome(sessionID string) askOutcome {
	e.mu2.Lock()
	defer e.mu2.Unlock()
	return e.outcomes[sessionID]
}

func (e *promptingExecutor) Cancel(ctx context.Context, owner, batchID string) (execbatch.CancelResult, error) {
	return e.fakeExecutor.Cancel(ctx, owner, batchID)
}

type advanceResult struct {
	state RunState
	err   error
}

// startPrompting starts a one-step run and advances it in the background.
func startPrompting(t *testing.T, executor *promptingExecutor, timeout time.Duration, sessions ...string) (*Service, chan advanceResult) {
	t.Helper()
	service := New(executor)
	service.inputTimeout = timeout
	signal := make(chan struct{}, 1)
	service.promptSignal = signal
	service.onPromptsChanged = func() {
		select {
		case signal <- struct{}{}:
		default:
		}
	}
	startTestRun(t, service, []Step{{ID: "s1", Name: "one", Command: "run", Interactive: true}}, sessions, ConfirmProductionOnly, nil)
	commands := make([]RenderedCommand, len(sessions))
	for index, sessionID := range sessions {
		commands[index] = RenderedCommand{SessionID: sessionID, Command: "run"}
	}
	prepareTestStep(t, service, 1, commands...)
	done := make(chan advanceResult, 1)
	go func() {
		state, err := service.Advance(context.Background(), "owner-a", AdvanceRequest{RunID: "run-1", StepNumber: 1, Confirmed: true})
		done <- advanceResult{state, err}
	}()
	for range sessions {
		select {
		case <-executor.asking:
		case <-time.After(5 * time.Second):
			t.Fatal("hosts did not start asking")
		}
	}
	return service, done
}

func waitPrompts(t *testing.T, service *Service, count int) []Prompt {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		state, err := service.Get(context.Background(), "owner-a", "run-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Prompts) == count {
			return state.Prompts
		}
		select {
		case <-service.promptSignal:
		case <-timeout:
			t.Fatalf("prompts = %+v, want %d", state.Prompts, count)
		}
	}
}

func awaitAdvance(t *testing.T, done chan advanceResult) RunState {
	t.Helper()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		return result.state
	case <-time.After(5 * time.Second):
		t.Fatal("advance did not finish")
		return RunState{}
	}
}

func TestPromptIsAnsweredByOwnerAndRecorded(t *testing.T) {
	executor := newPromptingExecutor(execbatch.DirectiveInput, "h1")
	service, done := startPrompting(t, executor, time.Minute, "h1")
	prompts := waitPrompts(t, service, 1)
	prompt := prompts[0]
	if prompt.SessionID != "h1" || prompt.HostName != "name-h1" || prompt.Kind != PromptInput || prompt.Text != "Which port?" ||
		prompt.Default != "8080" || !prompt.CanApplyAll || prompt.ExpiresAt == 0 {
		t.Fatalf("prompt = %+v", prompt)
	}
	state, _ := service.Get(context.Background(), "owner-a", "run-1")
	if len(state.Live) != 1 || state.Live[0].SubStep != "Configure" || state.Live[0].Progress != -1 {
		t.Fatalf("live = %+v", state.Live)
	}
	if _, err := service.Respond(context.Background(), "owner-a", RespondRequest{RunID: "run-1", PromptID: prompt.ID, Value: "9090"}); err != nil {
		t.Fatal(err)
	}
	final := awaitAdvance(t, done)
	if final.Status != RunSuccess || len(final.Prompts) != 0 {
		t.Fatalf("final = %+v", final)
	}
	if got := executor.outcome("h1").answer.Value; got != "9090" {
		t.Fatalf("script received %q", got)
	}
	if len(final.Answers) != 1 || final.Answers[0].Value != "9090" || final.Answers[0].Variable != "port" || final.Answers[0].HostName != "name-h1" {
		t.Fatalf("answers = %+v", final.Answers)
	}
}

func TestSecretAnswerIsNeverStoredOrExposed(t *testing.T) {
	const secret = "hunter2-very-secret"
	executor := newPromptingExecutor(execbatch.DirectiveSecret, "h1")
	service, done := startPrompting(t, executor, time.Minute, "h1")
	prompt := waitPrompts(t, service, 1)[0]
	if prompt.Kind != PromptSecret || prompt.CanApplyAll || prompt.Default != "" {
		t.Fatalf("secret prompt = %+v", prompt)
	}
	request := RespondRequest{RunID: "run-1", PromptID: prompt.ID, Value: secret}
	for _, rendered := range []string{request.String(), fmt.Sprintf("%v %+v %#v", request, request, request)} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("request formatting leaks the secret: %s", rendered)
		}
	}
	var logged strings.Builder
	logger := slog.New(slog.NewTextHandler(&logged, nil))
	logger.Info("respond", "request", request)
	if strings.Contains(logged.String(), secret) {
		t.Fatalf("slog output leaks the secret: %s", logged.String())
	}
	state, err := service.Respond(context.Background(), "owner-a", request)
	if err != nil {
		t.Fatal(err)
	}
	final := awaitAdvance(t, done)
	if executor.outcome("h1").answer.Value != secret {
		t.Fatal("script did not receive the secret")
	}
	for _, snapshot := range []RunState{state, final} {
		encoded, _ := json.Marshal(snapshot)
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("run state leaks the secret: %s", encoded)
		}
		if len(snapshot.Answers) != 0 {
			t.Fatalf("secret answer recorded: %+v", snapshot.Answers)
		}
	}
	if _, err := service.Get(context.Background(), "owner-a", "run-1"); err != nil {
		t.Fatal(err)
	}
	history, _ := service.Get(context.Background(), "owner-a", "run-1")
	if encoded, _ := json.Marshal(history); strings.Contains(string(encoded), secret) {
		t.Fatal("later snapshot leaks the secret")
	}
}

func TestSecretPromptRejectsApplyAll(t *testing.T) {
	executor := newPromptingExecutor(execbatch.DirectiveSecret, "h1", "h2")
	service, done := startPrompting(t, executor, time.Minute, "h1", "h2")
	prompts := waitPrompts(t, service, 2)
	if _, err := service.Respond(context.Background(), "owner-a", RespondRequest{RunID: "run-1", PromptID: prompts[0].ID, Value: "x", ApplyAll: true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	for _, prompt := range prompts {
		if _, err := service.Respond(context.Background(), "owner-a", RespondRequest{RunID: "run-1", PromptID: prompt.ID, Value: "per-" + prompt.SessionID}); err != nil {
			t.Fatal(err)
		}
	}
	awaitAdvance(t, done)
	if executor.outcome("h1").answer.Value != "per-h1" || executor.outcome("h2").answer.Value != "per-h2" {
		t.Fatalf("answers = %+v %+v", executor.outcome("h1"), executor.outcome("h2"))
	}
}

func TestMultiHostPromptsAreAnsweredSeparately(t *testing.T) {
	executor := newPromptingExecutor(execbatch.DirectiveInput, "h1", "h2")
	service, done := startPrompting(t, executor, time.Minute, "h1", "h2")
	prompts := waitPrompts(t, service, 2)
	hosts := map[string]string{}
	for _, prompt := range prompts {
		hosts[prompt.SessionID] = prompt.HostName
	}
	if hosts["h1"] != "name-h1" || hosts["h2"] != "name-h2" {
		t.Fatalf("prompts do not identify their hosts: %+v", prompts)
	}
	first := prompts[0]
	if _, err := service.Respond(context.Background(), "owner-a", RespondRequest{RunID: "run-1", PromptID: first.ID, Value: "one"}); err != nil {
		t.Fatal(err)
	}
	remaining := waitPrompts(t, service, 1)
	if remaining[0].SessionID == first.SessionID {
		t.Fatalf("answered host still pending: %+v", remaining)
	}
	if _, err := service.Respond(context.Background(), "owner-a", RespondRequest{RunID: "run-1", PromptID: remaining[0].ID, Value: "two"}); err != nil {
		t.Fatal(err)
	}
	awaitAdvance(t, done)
	if executor.outcome(first.SessionID).answer.Value != "one" || executor.outcome(remaining[0].SessionID).answer.Value != "two" {
		t.Fatal("one host's answer was applied to the other")
	}
}

func TestApplyAllReusesNonSecretAnswerForMatchingPrompts(t *testing.T) {
	executor := newPromptingExecutor(execbatch.DirectiveInput, "h1", "h2", "h3")
	service, done := startPrompting(t, executor, time.Minute, "h1", "h2", "h3")
	prompts := waitPrompts(t, service, 3)
	if _, err := service.Respond(context.Background(), "owner-a", RespondRequest{RunID: "run-1", PromptID: prompts[0].ID, Value: "shared", ApplyAll: true}); err != nil {
		t.Fatal(err)
	}
	final := awaitAdvance(t, done)
	autos := 0
	for _, host := range []string{"h1", "h2", "h3"} {
		if executor.outcome(host).answer.Value != "shared" {
			t.Fatalf("host %s answer = %+v", host, executor.outcome(host))
		}
	}
	for _, note := range final.Answers {
		if note.Auto {
			autos++
		}
	}
	if len(final.Answers) != 3 || autos != 2 {
		t.Fatalf("answers = %+v", final.Answers)
	}
}

func TestOtherOwnerCannotSeeOrAnswerPrompts(t *testing.T) {
	executor := newPromptingExecutor(execbatch.DirectiveInput, "h1")
	service, done := startPrompting(t, executor, time.Minute, "h1")
	prompt := waitPrompts(t, service, 1)[0]
	if _, err := service.Get(context.Background(), "owner-b", "run-1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("get err = %v", err)
	}
	if _, err := service.Respond(context.Background(), "owner-b", RespondRequest{RunID: "run-1", PromptID: prompt.ID, Value: "evil"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("respond err = %v", err)
	}
	if _, err := service.Respond(context.Background(), "owner-a", RespondRequest{RunID: "run-1", PromptID: prompt.ID, Value: "ok"}); err != nil {
		t.Fatal(err)
	}
	awaitAdvance(t, done)
	if executor.outcome("h1").answer.Value != "ok" {
		t.Fatalf("outcome = %+v", executor.outcome("h1"))
	}
}

func TestRespondValidation(t *testing.T) {
	executor := newPromptingExecutor(execbatch.DirectiveConfirm, "h1")
	service, done := startPrompting(t, executor, time.Minute, "h1")
	prompt := waitPrompts(t, service, 1)[0]
	if prompt.Kind != PromptConfirm {
		t.Fatalf("prompt = %+v", prompt)
	}
	for _, value := range []string{"", "maybe", "YES", "yes\n"} {
		if _, err := service.Respond(context.Background(), "owner-a", RespondRequest{RunID: "run-1", PromptID: prompt.ID, Value: value}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("value %q err = %v", value, err)
		}
	}
	if _, err := service.Respond(context.Background(), "owner-a", RespondRequest{RunID: "run-1", PromptID: "nope", Value: "yes"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown prompt err = %v", err)
	}
	if _, err := service.Respond(context.Background(), "owner-a", RespondRequest{RunID: "run-1", PromptID: prompt.ID, Value: "no"}); err != nil {
		t.Fatal(err)
	}
	awaitAdvance(t, done)
	if _, err := service.Respond(context.Background(), "owner-a", RespondRequest{RunID: "run-1", PromptID: prompt.ID, Value: "no"}); err == nil {
		t.Fatalf("answered twice err = %v", err)
	}
}

func TestPromptTimesOutAndFailsHostVisibly(t *testing.T) {
	executor := newPromptingExecutor(execbatch.DirectiveInput, "h1")
	_, done := startPrompting(t, executor, 30*time.Millisecond, "h1")
	final := awaitAdvance(t, done)
	if final.Status != RunFailed || len(final.Prompts) != 0 {
		t.Fatalf("final = %+v", final)
	}
	if err := executor.outcome("h1").err; !errors.Is(err, execbatch.ErrInputTimeout) {
		t.Fatalf("ask err = %v", err)
	}
	if got := final.Steps[0].Results[0].Error; !strings.Contains(got, execbatch.ErrInputTimeout.Error()) {
		t.Fatalf("result error = %q", got)
	}
}

func TestCancelUnblocksPendingPrompts(t *testing.T) {
	executor := newPromptingExecutor(execbatch.DirectiveInput, "h1", "h2")
	service, done := startPrompting(t, executor, time.Hour, "h1", "h2")
	waitPrompts(t, service, 2)
	state, err := service.Cancel(context.Background(), "owner-a", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	_ = state
	final := awaitAdvance(t, done)
	if final.Status != RunCanceled && final.Status != RunFailed {
		t.Fatalf("final = %+v", final)
	}
	for _, host := range []string{"h1", "h2"} {
		if err := executor.outcome(host).err; !errors.Is(err, context.Canceled) {
			t.Fatalf("host %s ask err = %v", host, err)
		}
	}
	after, _ := service.Get(context.Background(), "owner-a", "run-1")
	if len(after.Prompts) != 0 {
		t.Fatalf("prompts after cancel = %+v", after.Prompts)
	}
}

func TestContextCancelUnblocksPrompt(t *testing.T) {
	executor := newPromptingExecutor(execbatch.DirectiveInput, "h1")
	service := New(executor)
	startTestRun(t, service, []Step{{ID: "s1", Name: "one", Command: "run", Interactive: true}}, []string{"h1"}, ConfirmProductionOnly, nil)
	prepareTestStep(t, service, 1, RenderedCommand{SessionID: "h1", Command: "run"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan advanceResult, 1)
	go func() {
		state, err := service.Advance(ctx, "owner-a", AdvanceRequest{RunID: "run-1", StepNumber: 1, Confirmed: true})
		done <- advanceResult{state, err}
	}()
	<-executor.asking
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("advance did not end after its context was canceled")
	}
}

func TestExecutorRequestsCarryTheRunAsInteractor(t *testing.T) {
	executor := &fakeExecutor{}
	service := New(executor)
	startTestRun(t, service, []Step{{ID: "s1", Name: "one", Command: "run", Interactive: true}}, []string{"h1"}, ConfirmProductionOnly, nil)
	prepareTestStep(t, service, 1, RenderedCommand{SessionID: "h1", Command: "run"})
	if _, err := service.Advance(context.Background(), "owner-a", AdvanceRequest{RunID: "run-1", StepNumber: 1, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	requests := executor.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d", len(requests))
	}
	r, _ := service.ownerRun("owner-a", "run-1")
	if requests[0].Interactor == nil || requests[0].Interactor != execbatch.Interactor(r) {
		t.Fatalf("interactor = %v", requests[0].Interactor)
	}
}

func TestInteractorOnlyForOptedInSteps(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		executor := &fakeExecutor{}
		service := New(executor)
		startTestRun(t, service, []Step{{ID: "s1", Name: "one", Command: "cat f", Interactive: interactive}}, []string{"h1"}, ConfirmProductionOnly, nil)
		prepareTestStep(t, service, 1, RenderedCommand{SessionID: "h1", Command: "cat f"})
		if _, err := service.Advance(context.Background(), "owner-a", AdvanceRequest{RunID: "run-1", StepNumber: 1, Confirmed: true}); err != nil {
			t.Fatal(err)
		}
		if got := executor.Requests()[0].Interactor != nil; got != interactive {
			t.Fatalf("interactive=%v but request has interactor=%v", interactive, got)
		}
	}
}

func TestRespondAfterCancelIsRejected(t *testing.T) {
	executor := newPromptingExecutor(execbatch.DirectiveInput, "h1")
	service, done := startPrompting(t, executor, time.Hour, "h1")
	prompt := waitPrompts(t, service, 1)[0]
	if _, err := service.Cancel(context.Background(), "owner-a", "run-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Respond(context.Background(), "owner-a", RespondRequest{RunID: "run-1", PromptID: prompt.ID, Value: "late"}); err == nil {
		t.Fatal("respond after cancel succeeded")
	}
	awaitAdvance(t, done)
}

func TestSuccessfulRespondIsAlwaysDeliveredEvenWhenTimeoutRaces(t *testing.T) {
	for range 50 {
		executor := newPromptingExecutor(execbatch.DirectiveInput, "h1")
		service, done := startPrompting(t, executor, 5*time.Millisecond, "h1")
		var delivered bool
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			state, _ := service.Get(context.Background(), "owner-a", "run-1")
			if len(state.Prompts) == 1 {
				_, err := service.Respond(context.Background(), "owner-a", RespondRequest{RunID: "run-1", PromptID: state.Prompts[0].ID, Value: "v"})
				delivered = err == nil
				break
			}
			if state.Status != RunRunning {
				break
			}
		}
		awaitAdvance(t, done)
		outcome := executor.outcome("h1")
		if delivered && (outcome.err != nil || outcome.answer.Value != "v") {
			t.Fatalf("respond succeeded but script got %+v", outcome)
		}
	}
}

func TestLiveStateIsBoundedAgainstHostileEvents(t *testing.T) {
	service := New(&fakeExecutor{})
	startTestRun(t, service, []Step{{ID: "s1", Name: "one", Command: "run", Interactive: true}}, []string{"h1"}, ConfirmProductionOnly, nil)
	r, _ := service.ownerRun("owner-a", "run-1")
	for index := range 5000 {
		r.Notify(execbatch.Event{SessionID: "h1", SessionName: "web", Kind: execbatch.DirectiveMessage, Level: execbatch.LevelInfo, Text: fmt.Sprint("m", index)})
		r.Notify(execbatch.Event{SessionID: fmt.Sprint("fake-", index), SessionName: "x", Kind: execbatch.DirectiveStep, Text: "s"})
	}
	state, _ := service.Get(context.Background(), "owner-a", "run-1")
	if len(state.Live) > execbatch.MaxTargets {
		t.Fatalf("live hosts = %d", len(state.Live))
	}
	for _, host := range state.Live {
		if len(host.Messages) > maxLiveMessages {
			t.Fatalf("messages = %d", len(host.Messages))
		}
	}
	if last := state.Live[0].Messages[len(state.Live[0].Messages)-1].Text; last != "m4999" {
		t.Fatalf("latest message = %q", last)
	}
}
