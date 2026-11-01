package runbook

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
)

const (
	// DefaultInputTimeout is how long a script prompt waits for the operator.
	DefaultInputTimeout = 5 * time.Minute
	maxLiveMessages     = 20
	maxAnswerNotes      = 256
	maxPendingPrompts   = execbatch.MaxTargets
)

type PromptKind string

const (
	PromptInput   PromptKind = "input"
	PromptSecret  PromptKind = "secret"
	PromptConfirm PromptKind = "confirm"
)

// Prompt is a question a script raised on one host. All text fields are
// untrusted script output: clients render them as plain text, attributed to
// the script and host. A secret prompt never carries a default.
type Prompt struct {
	ID          string     `json:"id"`
	StepNumber  int32      `json:"stepNumber"`
	SessionID   string     `json:"sessionId"`
	HostName    string     `json:"hostName"`
	Kind        PromptKind `json:"kind"`
	Variable    string     `json:"variable,omitempty"`
	Text        string     `json:"text"`
	Default     string     `json:"default,omitempty"`
	CanApplyAll bool       `json:"canApplyAll"`
	// ExpiresAt is the unix millisecond deadline after which the host's run fails.
	ExpiresAt int64 `json:"expiresAt"`
}

// HostMessage is a script message line, shown as plain text.
type HostMessage struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// HostLive is the script-reported state of one host in the current step.
type HostLive struct {
	SessionID string `json:"sessionId"`
	HostName  string `json:"hostName"`
	// SubStep is the title from the latest STEP directive.
	SubStep string `json:"subStep,omitempty"`
	// Progress is 0-100 from PROGRESS directives, or -1 when none was reported.
	Progress int           `json:"progress"`
	Messages []HostMessage `json:"messages,omitempty"`
}

// AnswerNote records a non-secret answer given to a script. Secret answers
// are never recorded anywhere.
type AnswerNote struct {
	StepNumber int32      `json:"stepNumber"`
	SessionID  string     `json:"sessionId"`
	HostName   string     `json:"hostName"`
	Kind       PromptKind `json:"kind"`
	Variable   string     `json:"variable,omitempty"`
	Value      string     `json:"value"`
	// Auto is true when the answer was reused from an earlier 应用到全部 choice.
	Auto bool `json:"auto,omitempty"`
}

// RespondRequest answers one pending prompt. Value is "yes" or "no" for a
// confirm prompt. ApplyAll reuses a non-secret answer for the other hosts'
// matching prompts of the same step.
type RespondRequest struct {
	RunID    string `json:"runId"`
	PromptID string `json:"promptId"`
	Value    string `json:"value"`
	ApplyAll bool   `json:"applyAll,omitempty"`
}

// String, GoString and LogValue keep the answer, which may be a secret, out of logs.
func (r RespondRequest) String() string {
	return fmt.Sprintf("RespondRequest{RunID:%s PromptID:%s Value:[redacted]}", r.RunID, r.PromptID)
}

func (r RespondRequest) GoString() string { return r.String() }

func (r RespondRequest) LogValue() slog.Value {
	return slog.GroupValue(slog.String("runId", r.RunID), slog.String("promptId", r.PromptID))
}

type pendingPrompt struct {
	prompt Prompt
	reply  chan promptReply
}

type promptReply struct {
	value string
	auto  bool
}

type applyKey struct {
	step     int32
	kind     PromptKind
	variable string
	text     string
}

// interaction is the per-run state behind the execbatch.Interactor methods.
type interaction struct {
	timeout   time.Duration
	prompts   map[string]*pendingPrompt
	seq       int
	live      map[string]*HostLive
	liveOrder []string
	answers   []AnswerNote
	applyAll  map[applyKey]string
	abort     chan struct{}
	abortOnce sync.Once
}

func newInteraction(timeout time.Duration) *interaction {
	if timeout <= 0 {
		timeout = DefaultInputTimeout
	}
	return &interaction{
		timeout: timeout, prompts: make(map[string]*pendingPrompt),
		live: make(map[string]*HostLive), applyAll: make(map[applyKey]string),
		abort: make(chan struct{}),
	}
}

func (r *run) resetLiveLocked() {
	r.interact.live = make(map[string]*HostLive)
	r.interact.liveOrder = nil
	r.interact.applyAll = make(map[applyKey]string)
}

func (r *run) abortPrompts() {
	r.interact.abortOnce.Do(func() { close(r.interact.abort) })
}

func (r *run) liveHostLocked(sessionID, name string) *HostLive {
	host := r.interact.live[sessionID]
	if host == nil {
		host = &HostLive{SessionID: sessionID, HostName: name, Progress: -1}
		r.interact.live[sessionID] = host
		r.interact.liveOrder = append(r.interact.liveOrder, sessionID)
	}
	return host
}

// Notify implements execbatch.Interactor.
func (r *run) Notify(event execbatch.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.interact.live) >= execbatch.MaxTargets && r.interact.live[event.SessionID] == nil {
		return
	}
	host := r.liveHostLocked(event.SessionID, event.SessionName)
	switch event.Kind {
	case execbatch.DirectiveStep:
		host.SubStep = event.Text
	case execbatch.DirectiveProgress:
		host.Progress = event.Progress
	case execbatch.DirectiveMessage:
		host.Messages = append(host.Messages, HostMessage{Level: string(event.Level), Text: event.Text})
		if excess := len(host.Messages) - maxLiveMessages; excess > 0 {
			host.Messages = append([]HostMessage(nil), host.Messages[excess:]...)
		}
	}
}

func promptKindOf(kind execbatch.DirectiveKind) PromptKind {
	switch kind {
	case execbatch.DirectiveSecret:
		return PromptSecret
	case execbatch.DirectiveConfirm:
		return PromptConfirm
	default:
		return PromptInput
	}
}

// Ask implements execbatch.Interactor. It blocks until the owner answers
// through Respond, the timeout elapses, the run is canceled or ctx ends, and
// always removes its prompt on the way out.
func (r *run) Ask(ctx context.Context, question execbatch.Question) (execbatch.Answer, error) {
	kind := promptKindOf(question.Kind)
	r.mu.Lock()
	if terminalRun(r.state.Status) || r.cancelRequested {
		r.mu.Unlock()
		return execbatch.Answer{}, context.Canceled
	}
	step := r.state.CurrentStep
	key := applyKey{step: step, kind: kind, variable: question.Variable, text: question.Prompt}
	if kind != PromptSecret {
		if value, ok := r.interact.applyAll[key]; ok {
			r.recordAnswerLocked(step, question, kind, value, true)
			r.mu.Unlock()
			return execbatch.Answer{Value: value}, nil
		}
	}
	if len(r.interact.prompts) >= maxPendingPrompts {
		r.mu.Unlock()
		return execbatch.Answer{}, execbatch.ErrInputUnavailable
	}
	r.interact.seq++
	id := "p" + strconv.Itoa(r.interact.seq)
	timeout := r.interact.timeout
	pending := &pendingPrompt{
		reply: make(chan promptReply, 1),
		prompt: Prompt{
			ID: id, StepNumber: step, SessionID: question.SessionID, HostName: question.SessionName,
			Kind: kind, Variable: question.Variable, Text: question.Prompt,
			CanApplyAll: kind != PromptSecret, ExpiresAt: time.Now().Add(timeout).UnixMilli(),
		},
	}
	if kind != PromptSecret {
		pending.prompt.Default = question.Default
	}
	r.interact.prompts[id] = pending
	abort := r.interact.abort
	r.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var failure error
	select {
	case reply := <-pending.reply:
		return r.takeReply(step, question, kind, id, reply), nil
	case <-ctx.Done():
		failure = ctx.Err()
	case <-abort:
		failure = context.Canceled
	case <-timer.C:
		failure = execbatch.ErrInputTimeout
	}
	// Leave under the lock: once the prompt is gone Respond can no longer send, so
	// an answer that won the race is still taken and a successful Respond always
	// means the script receives it.
	r.mu.Lock()
	delete(r.interact.prompts, id)
	select {
	case reply := <-pending.reply:
		r.mu.Unlock()
		return r.takeReply(step, question, kind, id, reply), nil
	default:
	}
	r.mu.Unlock()
	return execbatch.Answer{}, failure
}

func (r *run) takeReply(step int32, question execbatch.Question, kind PromptKind, id string, reply promptReply) execbatch.Answer {
	r.mu.Lock()
	delete(r.interact.prompts, id)
	r.recordAnswerLocked(step, question, kind, reply.value, reply.auto)
	r.mu.Unlock()
	return execbatch.Answer{Value: reply.value}
}

func (r *run) recordAnswerLocked(step int32, question execbatch.Question, kind PromptKind, value string, auto bool) {
	if kind == PromptSecret {
		return
	}
	r.interact.answers = append(r.interact.answers, AnswerNote{
		StepNumber: step, SessionID: question.SessionID, HostName: question.SessionName,
		Kind: kind, Variable: question.Variable, Value: value, Auto: auto,
	})
	if excess := len(r.interact.answers) - maxAnswerNotes; excess > 0 {
		r.interact.answers = append([]AnswerNote(nil), r.interact.answers[excess:]...)
	}
}

// Respond answers one pending prompt of an owned run.
func (s *Service) Respond(_ context.Context, ownerID string, input RespondRequest) (RunState, error) {
	r, err := s.ownerRun(ownerID, input.RunID)
	if err != nil {
		return RunState{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.interact.abort:
		return RunState{}, fmt.Errorf("%w: run %s was canceled", ErrConflict, input.RunID)
	default:
	}
	if terminalRun(r.state.Status) || r.cancelRequested {
		return RunState{}, fmt.Errorf("%w: run %s is not accepting answers", ErrConflict, input.RunID)
	}
	pending := r.interact.prompts[input.PromptID]
	if pending == nil {
		return RunState{}, fmt.Errorf("%w: prompt %s is not pending", ErrNotFound, input.PromptID)
	}
	switch pending.prompt.Kind {
	case PromptConfirm:
		if input.Value != "yes" && input.Value != "no" {
			return RunState{}, fmt.Errorf("%w: a confirm prompt is answered yes or no", ErrInvalid)
		}
	default:
		if !execbatch.ValidAnswer(input.Value) {
			return RunState{}, fmt.Errorf("%w: answer must be one line of at most %d bytes", ErrInvalid, execbatch.MaxAnswerBytes)
		}
	}
	if input.ApplyAll && pending.prompt.Kind == PromptSecret {
		return RunState{}, fmt.Errorf("%w: a secret answer cannot be applied to all hosts", ErrInvalid)
	}
	delete(r.interact.prompts, input.PromptID)
	pending.reply <- promptReply{value: input.Value}
	if input.ApplyAll {
		key := applyKey{step: pending.prompt.StepNumber, kind: pending.prompt.Kind, variable: pending.prompt.Variable, text: pending.prompt.Text}
		r.interact.applyAll[key] = input.Value
		for id, other := range r.interact.prompts {
			otherKey := applyKey{step: other.prompt.StepNumber, kind: other.prompt.Kind, variable: other.prompt.Variable, text: other.prompt.Text}
			if otherKey == key {
				delete(r.interact.prompts, id)
				other.reply <- promptReply{value: input.Value, auto: true}
			}
		}
	}
	return r.snapshotLocked(), nil
}

func (r *run) promptsLocked() []Prompt {
	if len(r.interact.prompts) == 0 {
		return nil
	}
	prompts := make([]Prompt, 0, len(r.interact.prompts))
	for _, pending := range r.interact.prompts {
		prompts = append(prompts, pending.prompt)
	}
	sortPrompts(prompts)
	return prompts
}

func (r *run) liveLocked() []HostLive {
	if len(r.interact.liveOrder) == 0 {
		return nil
	}
	hosts := make([]HostLive, 0, len(r.interact.liveOrder))
	for _, sessionID := range r.interact.liveOrder {
		host := *r.interact.live[sessionID]
		host.Messages = append([]HostMessage(nil), host.Messages...)
		hosts = append(hosts, host)
	}
	return hosts
}

func sortPrompts(prompts []Prompt) {
	sort.Slice(prompts, func(i, j int) bool {
		if len(prompts[i].ID) != len(prompts[j].ID) {
			return len(prompts[i].ID) < len(prompts[j].ID)
		}
		return prompts[i].ID < prompts[j].ID
	})
}
