package base

import (
	"context"
	"time"
)

// Session attention states reported by a durable helper. They are derived
// inside the helper from terminal-standard signals and user state
// rules, independent of any attached client.
const (
	// SessionRunning: output arrived within the idle threshold.
	SessionRunning = "running"
	// SessionNeedsMe: the program rang the bell, sent a notification or
	// reported a progress error (confidence signal), or a user state rule
	// matched (confidence rule).
	SessionNeedsMe = "needs_me"
	// SessionJustFinished: the shell reported a command end (OSC 133 D).
	SessionJustFinished = "just_finished"
	// SessionIdle: no recent output.
	SessionIdle = "idle"
	// SessionExited: the session process exited.
	SessionExited = "exited"
)

// Confidence of a session state.
const (
	// ConfidenceSignal: the state follows from an explicit terminal signal
	// or an observed fact (output, exit).
	ConfidenceSignal = "signal"
	// ConfidenceRule: a user-defined state rule matched the screen.
	ConfidenceRule = "rule"
)

// Session event kinds.
const (
	SessionEventBell       = "bell"
	SessionEventNotify     = "notify"
	SessionEventCommandEnd = "command_end"
	SessionEventState      = "state"
)

// SessionState is the attention state of one durable session.
type SessionState struct {
	State string    `json:"state"`
	Since time.Time `json:"since"`
	// Confidence is ConfidenceSignal or ConfidenceRule.
	Confidence string `json:"confidence"`
	// Reason names the input that caused the state: bell, notify,
	// progress_error (an OSC 9;4 error report), rule, command_end,
	// command_start, output, input, quiet or exit.
	Reason string `json:"reason,omitempty"`
	// RuleID is the user state rule that set the state (confidence rule).
	// RuleKeys is that rule's optional key sequence, offered while it holds
	// the session in needs_me; the helper never sends it by itself.
	RuleID   string `json:"rule_id,omitempty"`
	RuleKeys string `json:"rule_keys,omitempty"`
	// ExitCode is the command exit code for just_finished (when the shell
	// reported one) and the process exit code for exited.
	ExitCode *int `json:"exit_code,omitempty"`
	// DurationMS is the command run time for just_finished.
	DurationMS int64 `json:"duration_ms,omitempty"`
	// Notable marks a just_finished command that ran at least the helper's
	// notification threshold; shorter commands are not worth a notification.
	Notable bool `json:"notable,omitempty"`
	// Seen reports that a client showed this needs_me or just_finished state
	// to its user. It is shared by every client of the session and cleared by
	// the next state change or a new bell or notification.
	Seen bool `json:"seen,omitempty"`
	// Title and Body are the text of the notification (OSC 9, 777 or 99)
	// that made the state needs_me, cut to the helper limit; empty for any
	// other state or reason.
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	// Progress is the progress the program last reported with OSC 9;4; nil
	// when none is shown. It is independent of State.
	Progress *SessionProgress `json:"progress,omitempty"`
	// LastEventSeq is the sequence of the newest event of this session.
	// Sequences are monotonic across all sessions of one helper generation.
	LastEventSeq uint64 `json:"last_event_seq,omitempty"`
	// Events are the retained recent events after the sequence the caller
	// asked for, oldest first.
	Events []SessionEvent `json:"events,omitempty"`
	// EventsDropped reports that events after the requested sequence were
	// evicted from the helper's ring or left out to bound the reply size.
	EventsDropped bool `json:"events_dropped,omitempty"`
}

// Progress states of a SessionProgress.
const (
	ProgressNormal        = "normal"
	ProgressError         = "error"
	ProgressIndeterminate = "indeterminate"
	ProgressPaused        = "paused"
)

// SessionProgress is a progress report of the program in a session (ConEmu
// OSC 9;4, as Windows Terminal and Ghostty show it). It is cleared by the
// program, by a command start or end (OSC 133 C or D), by the process exit,
// and when the program sends no report for the helper's stale timeout.
type SessionProgress struct {
	// State is normal, error, indeterminate or paused.
	State string `json:"state"`
	// Value is the percentage (0-100); nil when unknown, which is always the
	// case for indeterminate.
	Value *int `json:"value,omitempty"`
}

// Equal reports whether p and other describe the same progress; nil is no
// progress.
func (p *SessionProgress) Equal(other *SessionProgress) bool {
	if p == nil || other == nil {
		return p == other
	}
	if p.State != other.State || (p.Value == nil) != (other.Value == nil) {
		return false
	}
	return p.Value == nil || *p.Value == *other.Value
}

// Clone returns a copy that shares nothing with p.
func (p *SessionProgress) Clone() *SessionProgress {
	if p == nil {
		return nil
	}
	clone := &SessionProgress{State: p.State}
	if p.Value != nil {
		value := *p.Value
		clone.Value = &value
	}
	return clone
}

// DurableStateReporter is implemented by durable attachments whose helper
// reports the session's attention state.
type DurableStateReporter interface {
	// DurableSessionState returns the latest state without events, or nil
	// while the helper has reported none.
	DurableSessionState() *SessionState
	// DurableStateUpdates is signalled after each state change. Signals
	// coalesce; read DurableSessionState after each one.
	DurableStateUpdates() <-chan struct{}
	// MarkDurableStateSeen marks the state that began at since as seen for
	// every client of the session. A state that has moved on is left alone.
	MarkDurableStateSeen(ctx context.Context, since time.Time) error
}

// DurableSizeReporter is implemented by a durable attachment whose helper
// tells it when another client, such as a second desktop attached through its
// own connection, resized the session.
type DurableSizeReporter interface {
	// DurableSizeUpdates is signalled after another client resized the
	// session. Signals coalesce; read DurablePushedSize after each one.
	DurableSizeUpdates() <-chan struct{}
	// DurablePushedSize is the size another client last gave the session;
	// ok is false until one did.
	DurablePushedSize() (cols, rows uint32, ok bool)
}

// SessionEvent is one entry of a session's recent events.
type SessionEvent struct {
	Seq  uint64    `json:"seq"`
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`
	// Source, Title and Body describe a notify event. Text over the helper
	// limit is cut and TextTruncated set.
	Source        string `json:"source,omitempty"`
	Title         string `json:"title,omitempty"`
	Body          string `json:"body,omitempty"`
	TextTruncated bool   `json:"text_truncated,omitempty"`
	// ExitCode, DurationMS and Notable describe a command_end event.
	ExitCode   *int  `json:"exit_code,omitempty"`
	DurationMS int64 `json:"duration_ms,omitempty"`
	Notable    bool  `json:"notable,omitempty"`
	// State, Confidence, Reason and RuleID describe a state event. On a bell event
	// Reason explains why the bell did not ask for attention: after_input
	// (it answered input within a second) or shell_prompt (the shell's line
	// editor rang it).
	State      string `json:"state,omitempty"`
	Confidence string `json:"confidence,omitempty"`
	Reason     string `json:"reason,omitempty"`
	RuleID     string `json:"rule_id,omitempty"`
}
