package hitl

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
)

type fakeCheckpoints struct {
	mu        sync.Mutex
	values    map[string][]byte
	getErr    error
	deleteErr error
	deletes   map[string]int
}

func newFakeCheckpoints() *fakeCheckpoints {
	return &fakeCheckpoints{values: make(map[string][]byte), deletes: make(map[string]int)}
}

func (s *fakeCheckpoints) Get(ctx context.Context, id string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, false, s.getErr
	}
	value, ok := s.values[id]
	return append([]byte(nil), value...), ok, nil
}

func (s *fakeCheckpoints) Set(ctx context.Context, id string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.values[id] = append([]byte(nil), value...)
	s.mu.Unlock()
	return nil
}

func (s *fakeCheckpoints) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.values, id)
	s.deletes[id]++
	return nil
}

func (s *fakeCheckpoints) put(id string, value []byte) {
	s.mu.Lock()
	s.values[id] = append([]byte(nil), value...)
	s.mu.Unlock()
}

func (s *fakeCheckpoints) remove(id string) {
	s.mu.Lock()
	delete(s.values, id)
	s.mu.Unlock()
}

func (s *fakeCheckpoints) deleteCount(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deletes[id]
}

type resumeCall struct {
	ctx          context.Context
	checkpointID string
	params       *adk.ResumeParams
	ctxErr       error
}

type fakeResumer struct {
	mu      sync.Mutex
	calls   []resumeCall
	err     error
	entered chan struct{}
	release chan struct{}
}

func (r *fakeResumer) ResumeWithParams(ctx context.Context, checkpointID string, params *adk.ResumeParams, _ ...adk.AgentRunOption) (*adk.AsyncIterator[*adk.AgentEvent], error) {
	r.mu.Lock()
	if r.entered != nil {
		select {
		case <-r.entered:
		default:
			close(r.entered)
		}
	}
	release := r.release
	targets := make(map[string]any, len(params.Targets))
	for key, value := range params.Targets {
		targets[key] = value
	}
	r.calls = append(r.calls, resumeCall{ctx: ctx, checkpointID: checkpointID, params: &adk.ResumeParams{Targets: targets}, ctxErr: ctx.Err()})
	err := r.err
	r.mu.Unlock()
	if release != nil {
		<-release
	}
	if err != nil {
		return nil, err
	}
	iterator, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Close()
	return iterator, nil
}

func (r *fakeResumer) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *fakeResumer) call(index int) resumeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[index]
}

type harness struct {
	manager *Manager
	store   *fakeCheckpoints
	resumer *fakeResumer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store := newFakeCheckpoints()
	store.put("checkpoint", []byte("checkpoint-one"))
	var nonce atomic.Uint64
	manager, err := NewManager(Config{
		Checkpoints: store,
		TTL:         time.Hour,
		NewNonce: func() (string, error) {
			return fmt.Sprintf("nonce-%d", nonce.Add(1)), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	return &harness{manager: manager, store: store, resumer: &fakeResumer{}}
}

func (h *harness) interrupt(t *testing.T, targetID, callID string, parameters string) Interrupt {
	t.Helper()
	request, err := h.manager.Interrupt(context.Background(), &adk.InterruptCtx{ID: targetID, IsRootCause: true}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: callID, Tool: "exec_commands", Kind: KindConfirm, Parameters: []byte(parameters),
	})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func answerFor(id string, request Interrupt) Answer {
	return Answer{
		ID: id, RunID: request.RunID, RequestID: request.ID, CheckpointID: request.CheckpointID,
		TargetID: request.TargetID, CallID: request.CallID, Nonce: request.Nonce,
		Parameters: request.Parameters, Decision: DecisionAllow,
	}
}

func TestConfirmationLifecycleAndStableReplay(t *testing.T) {
	h := newHarness(t)
	request := h.interrupt(t, "target-one", "call-one", `{ "commands":["go","test"], "timeout":30 }`)
	if request.Sequence != 1 || request.Attempt != 1 || request.ParameterHash == "" || request.CheckpointHash == "" {
		t.Fatalf("unexpected request: %+v", request)
	}
	replayed, err := h.manager.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-one", IsRootCause: true}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "exec_commands", Kind: KindConfirm,
		Parameters: []byte(`{"timeout":30,"commands":["go","test"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != request.ID || replayed.Nonce != request.Nonce || replayed.Sequence != request.Sequence {
		t.Fatalf("unstable replay: first=%+v second=%+v", request, replayed)
	}
	answer := answerFor("answer-one", request)
	answer.Parameters = []byte(`{"timeout":30,"commands":["go","test"]}`)
	resume, iterator, err := h.manager.Resume(context.Background(), h.resumer, answer)
	if err != nil {
		t.Fatal(err)
	}
	if iterator == nil || resume.ID == "" || resume.Attempt != 2 || resume.Sequence != 2 {
		t.Fatalf("unexpected resume: %+v iterator=%v", resume, iterator)
	}
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answer); !errors.Is(err, ErrRequestConsumed) {
		t.Fatalf("duplicate error = %v", err)
	}
	if h.resumer.count() != 1 {
		t.Fatalf("resume calls = %d", h.resumer.count())
	}
	call := h.resumer.call(0)
	if call.checkpointID != "checkpoint" || call.ctxErr != nil || call.params.Targets["target-one"] != "allow" || len(call.params.Targets) != 1 {
		t.Fatalf("unexpected Eino resume: %+v", call)
	}
	sequence, err := h.manager.NextSequence("run")
	if err != nil || sequence != 3 {
		t.Fatalf("next sequence = %d, %v", sequence, err)
	}
	terminal, err := h.manager.Finish("run", TerminalCompleted, nil)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Sequence != 4 || terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %+v", terminal)
	}
	again, err := h.manager.Finish("run", TerminalCompleted, nil)
	if err != nil || again.Sequence != terminal.Sequence {
		t.Fatalf("duplicate terminal = %+v, %v", again, err)
	}
	if h.store.deleteCount("checkpoint") != 1 {
		t.Fatalf("checkpoint deletes = %d", h.store.deleteCount("checkpoint"))
	}
	events, err := h.manager.Events("run", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Reason != TerminalInterrupted || events[1].Kind != EventResumed || events[2].Kind != EventTerminal {
		t.Fatalf("events = %+v", events)
	}
	replay, err := h.manager.Events("run", 2)
	if err != nil || len(replay) != 1 || replay[0].Sequence != 4 {
		t.Fatalf("replayed events = %+v, %v", replay, err)
	}
}

func TestRejectChangedParametersNonceAndCrossCallBinding(t *testing.T) {
	h := newHarness(t)
	request := h.interrupt(t, "target-one", "call-one", `{"path":"/tmp/a","content":"one"}`)
	answer := answerFor("answer-one", request)
	answer.Parameters = []byte(`{"path":"/tmp/a","content":"two"}`)
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answer); !errors.Is(err, ErrParametersChanged) {
		t.Fatalf("changed parameters error = %v", err)
	}
	answer = answerFor("answer-one", request)
	answer.Nonce = "wrong"
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answer); !errors.Is(err, ErrInvalidNonce) {
		t.Fatalf("wrong nonce error = %v", err)
	}
	answer = answerFor("answer-one", request)
	answer.TargetID = "target-two"
	answer.CallID = "call-two"
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answer); !errors.Is(err, ErrRequestStale) {
		t.Fatalf("cross-call error = %v", err)
	}
	if h.resumer.count() != 0 {
		t.Fatalf("rejected answers resumed %d times", h.resumer.count())
	}
	answer = answerFor("answer-one", request)
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answer); err != nil {
		t.Fatalf("valid answer rejected after invalid attempts: %v", err)
	}
}

func TestNewCheckpointAndToolCallInvalidateLateAnswers(t *testing.T) {
	h := newHarness(t)
	first := h.interrupt(t, "target-one", "call-one", `{"value":1}`)
	h.store.put("checkpoint", []byte("checkpoint-two"))
	second := h.interrupt(t, "target-two", "call-two", `{"value":2}`)
	if second.ID == first.ID || second.Nonce == first.Nonce || second.Sequence != 2 {
		t.Fatalf("second request = %+v first = %+v", second, first)
	}
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("late", first)); !errors.Is(err, ErrRequestStale) {
		t.Fatalf("late answer error = %v", err)
	}
	wrongNonce := answerFor("second-answer", second)
	wrongNonce.Nonce = first.Nonce
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, wrongNonce); !errors.Is(err, ErrInvalidNonce) {
		t.Fatalf("cross-call nonce error = %v", err)
	}
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("second-answer", second)); err != nil {
		t.Fatal(err)
	}
	if h.resumer.count() != 1 || h.resumer.call(0).params.Targets["target-two"] != "allow" {
		t.Fatalf("resume calls = %+v", h.resumer.calls)
	}
}

func TestAnswerIDCannotBeReusedAcrossRequests(t *testing.T) {
	h := newHarness(t)
	first := h.interrupt(t, "target-one", "call-one", `{}`)
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("shared-answer", first)); err != nil {
		t.Fatal(err)
	}
	h.store.put("checkpoint", []byte("checkpoint-two"))
	second := h.interrupt(t, "target-two", "call-two", `{}`)
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("shared-answer", second)); !errors.Is(err, ErrAnswerReused) {
		t.Fatalf("answer reuse error = %v", err)
	}
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("new-answer", second)); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointValidationIsRetryableButCheckpointChangeIsNot(t *testing.T) {
	h := newHarness(t)
	request := h.interrupt(t, "target-one", "call-one", `{}`)
	h.store.remove("checkpoint")
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("answer-one", request)); !errors.Is(err, ErrCheckpointNotFound) {
		t.Fatalf("missing checkpoint error = %v", err)
	}
	h.store.put("checkpoint", []byte("checkpoint-one"))
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("answer-one", request)); err != nil {
		t.Fatalf("restored checkpoint did not resume: %v", err)
	}

	other := newHarness(t)
	otherRequest := other.interrupt(t, "target-one", "call-one", `{}`)
	other.store.put("checkpoint", []byte("changed"))
	if _, _, err := other.manager.Resume(context.Background(), other.resumer, answerFor("answer-one", otherRequest)); !errors.Is(err, ErrCheckpointChanged) {
		t.Fatalf("changed checkpoint error = %v", err)
	}
	if other.resumer.count() != 0 {
		t.Fatalf("changed checkpoint resumed %d times", other.resumer.count())
	}
}

func TestExpirationTerminatesAndRejectsLateAnswer(t *testing.T) {
	store := newFakeCheckpoints()
	store.put("checkpoint", []byte("checkpoint-one"))
	now := time.Unix(100, 0)
	manager, err := NewManager(Config{Checkpoints: store, TTL: time.Minute, Now: func() time.Time { return now }, NewNonce: func() (string, error) { return "nonce", nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	request, err := manager.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target"}, InterruptInput{RunID: "run", CheckpointID: "checkpoint", CallID: "call", Tool: "exec_commands", Kind: KindConfirm})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, _, err := manager.Resume(context.Background(), &fakeResumer{}, answerFor("answer", request)); !errors.Is(err, ErrRequestExpired) {
		t.Fatalf("expired answer error = %v", err)
	}
	snapshot, err := manager.Snapshot("run")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != RunStatusExpired || snapshot.Terminal == nil || snapshot.Terminal.Reason != TerminalExpired || snapshot.Terminal.Sequence != 2 {
		t.Fatalf("expired snapshot = %+v", snapshot)
	}
	if store.deleteCount("checkpoint") != 1 {
		t.Fatalf("checkpoint deletes = %d", store.deleteCount("checkpoint"))
	}
}

func TestExpirationTimerTerminatesWithoutSubmission(t *testing.T) {
	store := newFakeCheckpoints()
	store.put("checkpoint", []byte("checkpoint-one"))
	manager, err := NewManager(Config{Checkpoints: store, TTL: 20 * time.Millisecond, NewNonce: func() (string, error) { return "nonce", nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target"}, InterruptInput{RunID: "run", CheckpointID: "checkpoint", CallID: "call", Tool: "exec_commands", Kind: KindConfirm}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, err := manager.Snapshot("run")
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Status == RunStatusExpired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("request did not expire: %+v", snapshot)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCancellationIsExplicitTerminalAndIdempotent(t *testing.T) {
	h := newHarness(t)
	request := h.interrupt(t, "target-one", "call-one", `{}`)
	terminal, err := h.manager.Cancel("run")
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Reason != TerminalCanceled || terminal.Sequence != 2 {
		t.Fatalf("terminal = %+v", terminal)
	}
	again, err := h.manager.Cancel("run")
	if err != nil || again.Sequence != terminal.Sequence {
		t.Fatalf("duplicate cancel = %+v, %v", again, err)
	}
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("late", request)); !errors.Is(err, ErrRunFinished) {
		t.Fatalf("late answer error = %v", err)
	}
	if h.store.deleteCount("checkpoint") != 1 {
		t.Fatalf("checkpoint deletes = %d", h.store.deleteCount("checkpoint"))
	}
}

func TestTransportContextCancellationDoesNotCancelExecution(t *testing.T) {
	h := newHarness(t)
	request := h.interrupt(t, "target-one", "call-one", `{}`)
	resumeContext, cancelResume := context.WithCancel(context.Background())
	_, _, err := h.manager.Resume(resumeContext, h.resumer, answerFor("answer", request))
	if err != nil {
		t.Fatal(err)
	}
	cancelResume()
	executionContext := h.resumer.call(0).ctx
	select {
	case <-executionContext.Done():
		t.Fatal("transport cancellation propagated into execution")
	default:
	}
	snapshot, err := h.manager.Snapshot("run")
	if err != nil || snapshot.Status != RunStatusRunning {
		t.Fatalf("snapshot = %+v, %v", snapshot, err)
	}
	if _, err := h.manager.Cancel("run"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-executionContext.Done():
	case <-time.After(time.Second):
		t.Fatal("explicit cancellation did not reach execution")
	}
}

func TestConcurrentDuplicateAnswersConsumeOnce(t *testing.T) {
	h := newHarness(t)
	h.resumer.entered = make(chan struct{})
	h.resumer.release = make(chan struct{})
	request := h.interrupt(t, "target-one", "call-one", `{}`)
	answer := answerFor("answer", request)
	const submissions = 16
	start := make(chan struct{})
	errorsSeen := make(chan error, submissions)
	successes := make(chan struct{}, submissions)
	var wg sync.WaitGroup
	for range submissions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, err := h.manager.Resume(context.Background(), h.resumer, answer)
			if err != nil {
				errorsSeen <- err
			} else {
				successes <- struct{}{}
			}
		}()
	}
	close(start)
	<-h.resumer.entered
	time.Sleep(10 * time.Millisecond)
	close(h.resumer.release)
	wg.Wait()
	close(errorsSeen)
	close(successes)
	if len(successes) != 1 || len(errorsSeen) != submissions-1 {
		t.Fatalf("successes=%d errors=%d", len(successes), len(errorsSeen))
	}
	for err := range errorsSeen {
		if !errors.Is(err, ErrRequestConsumed) {
			t.Fatalf("duplicate error = %v", err)
		}
	}
	if h.resumer.count() != 1 {
		t.Fatalf("resume calls = %d", h.resumer.count())
	}
	events, err := h.manager.Events("run", 0)
	if err != nil || len(events) != 2 || events[1].Kind != EventResumed {
		t.Fatalf("events = %+v, %v", events, err)
	}
}

func TestCancelAndResumeRaceHasOneTerminalAndAtMostOneResume(t *testing.T) {
	for iteration := range 40 {
		t.Run(fmt.Sprintf("iteration-%d", iteration), func(t *testing.T) {
			h := newHarness(t)
			request := h.interrupt(t, "target-one", "call-one", `{}`)
			answer := answerFor("answer", request)
			start := make(chan struct{})
			var resumeErr error
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, _, resumeErr = h.manager.Resume(context.Background(), h.resumer, answer)
			}()
			var cancelErr error
			go func() {
				defer wg.Done()
				<-start
				_, cancelErr = h.manager.Cancel("run")
			}()
			close(start)
			wg.Wait()
			if cancelErr != nil {
				t.Fatalf("cancel error = %v", cancelErr)
			}
			if resumeErr != nil && !errors.Is(resumeErr, ErrRunFinished) {
				t.Fatalf("resume error = %v", resumeErr)
			}
			if resumeErr == nil && h.resumer.count() != 1 || resumeErr != nil && h.resumer.count() != 0 {
				t.Fatalf("resume error=%v calls=%d", resumeErr, h.resumer.count())
			}
			snapshot, err := h.manager.Snapshot("run")
			if err != nil || snapshot.Status != RunStatusCanceled || snapshot.Terminal == nil {
				t.Fatalf("snapshot = %+v, %v", snapshot, err)
			}
			events, err := h.manager.Events("run", 0)
			if err != nil {
				t.Fatal(err)
			}
			terminals := 0
			for _, event := range events {
				if event.Kind == EventTerminal {
					terminals++
					if event.Reason != TerminalCanceled {
						t.Fatalf("terminal = %+v", event)
					}
				}
			}
			if terminals != 1 {
				t.Fatalf("terminal count = %d events = %+v", terminals, events)
			}
		})
	}
}

func TestCancellationReasonSurvivesLaterCompletion(t *testing.T) {
	h := newHarness(t)
	request := h.interrupt(t, "target-one", "call-one", `{}`)
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("answer", request)); err != nil {
		t.Fatal(err)
	}
	terminal, err := h.manager.Cancel("run")
	if err != nil {
		t.Fatal(err)
	}
	late, err := h.manager.Finish("run", TerminalCompleted, nil)
	if !errors.Is(err, ErrRunFinished) || late.Reason != TerminalCanceled || late.Sequence != terminal.Sequence {
		t.Fatalf("late completion = %+v, %v", late, err)
	}
}

func TestQuestionAnswerUsesSameBinding(t *testing.T) {
	h := newHarness(t)
	request, err := h.manager.Interrupt(context.Background(), &adk.InterruptCtx{ID: "question-target"}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "question-call", Tool: "ask_user", Kind: KindQuestion,
		Question: &Question{Text: "Which environment?", Options: []string{"dev", "prod"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Question == nil || request.Question.ID != request.ID {
		t.Fatalf("question binding = %+v request = %q", request.Question, request.ID)
	}
	answer := answerFor("answer", request)
	answer.Decision = ""
	answer.Text = "prod"
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answer); err != nil {
		t.Fatal(err)
	}
	if h.resumer.call(0).params.Targets["question-target"] != "prod" {
		t.Fatalf("resume params = %+v", h.resumer.call(0).params)
	}
}
