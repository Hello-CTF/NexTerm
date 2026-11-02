package execbatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	// maxPromptsPerTarget bounds how many prompts one target's script may raise in one command.
	maxPromptsPerTarget = 32
	// directiveBurst and directiveRefillPerSecond rate limit the directives one target may print.
	directiveBurst           = 40
	directiveRefillPerSecond = 20
	// maxRateWarnings keeps a flooding script from producing a warning per dropped line.
	maxRateWarnings = 1
)

var (
	// ErrInputTimeout is returned by Interactor.Ask when the operator did not answer in time.
	ErrInputTimeout = errors.New("等待操作员输入超时")
	// ErrInputUnavailable is returned when no operator can answer (for example the run view is gone).
	ErrInputUnavailable = errors.New("无法向操作员请求输入")
)

// Question is one prompt a script raised. Everything in it is untrusted
// script output; Interactors must render it as plain text and attribute it to
// the script and host.
type Question struct {
	SessionID   string
	SessionName string
	Kind        DirectiveKind // input, secret or confirm
	Variable    string
	Prompt      string
	Default     string
}

// Answer is the operator's reply. Value is the single stdin line for input
// and secret prompts; confirm prompts use "yes" or "no".
type Answer struct {
	Value string
}

// Event is a non-blocking directive: step, progress, message or a warning
// the driver itself raises.
type Event struct {
	SessionID   string
	SessionName string
	Kind        DirectiveKind
	Text        string
	Progress    int
	Level       MessageLevel
}

// Interactor receives the directives of scripts run with a Request.Interactor.
// Notify must not block. Ask blocks until the operator answers, ctx ends or
// the Interactor gives up; it must never retain or log a secret answer.
type Interactor interface {
	Notify(Event)
	Ask(ctx context.Context, question Question) (Answer, error)
}

// AnswerRecord describes an answered prompt for auditing. It never carries
// the value of a secret.
type AnswerRecord struct {
	SessionID string
	Kind      DirectiveKind
	Variable  string
	Secret    bool
}

// execStdinSupported reports whether the transport's exec channel accepts stdin.
func execStdinSupported(transport base.Transport) bool {
	if _, ok := transport.(base.ExecStreamTransport); !ok {
		return false
	}
	return transport.Kind() == "ssh"
}

// pausableDeadline is the exec timeout of an interactive target. It does not
// run while the target waits for the operator, so a slow human does not turn
// into a command timeout.
type pausableDeadline struct {
	mu        sync.Mutex
	timer     *time.Timer
	remaining time.Duration
	resumedAt time.Time
	stopped   bool
	fire      func()
}

func newPausableDeadline(total time.Duration, fire func()) *pausableDeadline {
	d := &pausableDeadline{remaining: total, fire: fire, resumedAt: time.Now()}
	d.timer = time.AfterFunc(total, fire)
	return d
}

func (d *pausableDeadline) Pause() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	if d.timer.Stop() {
		d.remaining -= time.Since(d.resumedAt)
		if d.remaining < 0 {
			d.remaining = 0
		}
	}
}

func (d *pausableDeadline) Resume() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	d.resumedAt = time.Now()
	d.timer.Reset(d.remaining)
}

func (d *pausableDeadline) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	d.timer.Stop()
}

// interactState drives the directives of one target's execution.
type interactState struct {
	interactor Interactor
	target     Target
	stdin      func(line string) error // nil when the exec cannot take stdin
	stdinWhy   string
	deadline   *pausableDeadline
	answered   func(AnswerRecord)
	askCtx     context.Context

	now        func() time.Time
	tokens     float64
	lastRefill time.Time
	rateWarned int
	prompts    int
	// pendingProgress holds the latest PROGRESS value a rate limit held back.
	pendingProgress *int
}

func (s *interactState) event(kind DirectiveKind, level MessageLevel, text string) {
	s.interactor.Notify(Event{
		SessionID: s.target.SessionID, SessionName: s.target.SessionName,
		Kind: kind, Level: level, Text: text,
	})
}

func (s *interactState) warn(text string) { s.event(DirectiveMessage, LevelWarn, text) }

func (s *interactState) allow() bool {
	now := s.now()
	if s.lastRefill.IsZero() {
		s.lastRefill = now
		s.tokens = directiveBurst
	}
	s.tokens += now.Sub(s.lastRefill).Seconds() * directiveRefillPerSecond
	if s.tokens > directiveBurst {
		s.tokens = directiveBurst
	}
	s.lastRefill = now
	if s.tokens < 1 {
		return false
	}
	s.tokens--
	return true
}

func (s *interactState) malformed(reason string) {
	if !s.allow() {
		return
	}
	s.warn("脚本输出了格式错误的指令，已按普通输出显示：" + reason)
}

// flushProgress delivers a PROGRESS value that the rate limit coalesced.
func (s *interactState) flushProgress() {
	if s.pendingProgress == nil {
		return
	}
	value := *s.pendingProgress
	s.pendingProgress = nil
	s.interactor.Notify(Event{
		SessionID: s.target.SessionID, SessionName: s.target.SessionName,
		Kind: DirectiveProgress, Progress: value,
	})
}

func (s *interactState) rateLimited() {
	if s.rateWarned < maxRateWarnings {
		s.rateWarned++
		s.warn("脚本输出指令过于频繁，已忽略部分指令")
	}
}

// handle processes one parsed directive. A returned error aborts the target.
// STEP, PROGRESS and MESSAGE are rate limited (PROGRESS keeps its latest value);
// prompts never are, because the script blocks on the answer, and are bounded by
// maxPromptsPerTarget instead.
func (s *interactState) handle(d Directive) error {
	switch d.Kind {
	case DirectiveStep, DirectiveProgress, DirectiveMessage:
		if !s.allow() {
			if d.Kind == DirectiveProgress {
				value := d.Progress
				s.pendingProgress = &value
			}
			s.rateLimited()
			return nil
		}
		if d.Kind != DirectiveProgress {
			s.flushProgress()
		} else {
			s.pendingProgress = nil
		}
		s.interactor.Notify(Event{
			SessionID: s.target.SessionID, SessionName: s.target.SessionName,
			Kind: d.Kind, Text: d.Text, Progress: d.Progress, Level: d.Level,
		})
		return nil
	}
	s.flushProgress()
	if s.stdin == nil {
		s.warn("无法向脚本传入交互输入（" + s.stdinWhy + "），已忽略输入请求，脚本将读到空输入")
		return nil
	}
	if s.prompts >= maxPromptsPerTarget {
		return fmt.Errorf("脚本请求输入的次数超过上限（%d 次），已终止该主机的执行", maxPromptsPerTarget)
	}
	s.prompts++
	question := Question{
		SessionID: s.target.SessionID, SessionName: s.target.SessionName,
		Kind: d.Kind, Variable: d.Variable, Prompt: d.Text, Default: d.Default,
	}
	if s.askCtx.Err() != nil {
		s.warn("脚本已结束或运行已取消，丢弃了一个输入请求")
		return nil
	}
	if s.deadline != nil {
		s.deadline.Pause()
	}
	answer, err := s.interactor.Ask(s.askCtx, question)
	if s.deadline != nil {
		s.deadline.Resume()
	}
	if err != nil {
		if s.askCtx.Err() != nil {
			// The script already ended or the run was canceled: the target outcome
			// decides, and the rest of its output must still be forwarded.
			s.warn("脚本已结束或运行已取消，丢弃了一个输入请求")
			return nil
		}
		return err
	}
	if !ValidAnswer(answer.Value) {
		return errors.New("operator answer is not a single line")
	}
	if err := s.stdin(answer.Value + "\n"); err != nil {
		return fmt.Errorf("write script input: %w", err)
	}
	if s.answered != nil {
		s.answered(AnswerRecord{
			SessionID: s.target.SessionID, Kind: d.Kind, Variable: d.Variable,
			Secret: d.Kind == DirectiveSecret,
		})
	}
	return nil
}

func stdinUnavailableReason(transport base.Transport) string {
	if !execStdinSupported(transport) {
		return "连接类型 " + transport.Kind() + " 的执行通道没有标准输入，仅 SSH 支持"
	}
	return "标准输入不可用"
}
