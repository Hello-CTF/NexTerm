package base

import (
	"context"
	"time"
)

// Session attention states reported by a durable helper. They are derived
// inside the helper from terminal-standard signals and generic screen
// shapes, independent of any attached client.
const (
	// SessionRunning: output arrived within the idle threshold.
	SessionRunning = "running"
	// SessionNeedsMe: the program rang the bell or sent a notification
	// (confidence signal), or output went idle with a prompt shape near the
	// cursor (confidence heuristic).
	SessionNeedsMe = "needs_me"
	// SessionJustFinished: the shell reported a command end (OSC 133 D).
	SessionJustFinished = "just_finished"
	// SessionIdle: no output and no prompt.
	SessionIdle = "idle"
	// SessionExited: the session process exited.
	SessionExited = "exited"
)

// Confidence of a session state.
const (
	// ConfidenceSignal: the state follows from an explicit terminal signal
	// or an observed fact (output, exit).
	ConfidenceSignal = "signal"
	// ConfidenceHeuristic: the state was inferred from the screen shape.
	ConfidenceHeuristic = "heuristic"
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

// Prompt tap methods and submit modes; see SessionPrompt.
const (
	TapMethodKeys   = "keys"
	TapMethodArrows = "arrows"

	TapSubmitEnter    = "enter"
	TapSubmitSeparate = "separate"
)

// Reasons a detected prompt is not tappable.
const (
	TapBlockedQuestion          = "question"
	TapBlockedShellPrompt       = "shell_prompt"
	TapBlockedShellForeground   = "shell_foreground"
	TapBlockedForegroundUnknown = "foreground_unknown"
	TapBlockedWeakMarker        = "weak_marker"
	TapBlockedNotLineMode       = "not_line_mode"
)

// SessionState is the attention state of one durable session.
type SessionState struct {
	State string    `json:"state"`
	Since time.Time `json:"since"`
	// Confidence is ConfidenceSignal, ConfidenceHeuristic or ConfidenceRule.
	Confidence string `json:"confidence"`
	// Reason names the input that caused the state: bell, notify, prompt,
	// rule, command_end, command_start, output, input, quiet or exit.
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
	// PromptEpoch advances whenever the detected prompt changes or appears
	// or disappears, and on any input written to the session. Together with
	// the helper generation it identifies one prompt instance.
	PromptEpoch uint64 `json:"prompt_epoch"`
	// Prompt is the prompt detected when output last went idle; nil when
	// none was found or output has arrived since.
	Prompt *SessionPrompt `json:"prompt,omitempty"`
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

// SessionPrompt is a detected prompt with what a client may do about it.
type SessionPrompt struct {
	// Kind is yesno, choice or question.
	Kind     string                `json:"kind"`
	Question string                `json:"question,omitempty"`
	Options  []SessionPromptOption `json:"options,omitempty"`
	// CursorIndex is the option carrying the selection marker, or -1.
	CursorIndex int `json:"cursor_index"`
	// Fingerprint is the hexadecimal prompt fingerprint.
	Fingerprint string `json:"fingerprint"`
	Epoch       uint64 `json:"epoch"`
	// Tappable reports that the options can be answered by sending each
	// option's Send bytes; Blocked names the reason when it is false.
	Tappable bool   `json:"tappable"`
	Blocked  string `json:"blocked,omitempty"`
	// Method is TapMethodKeys (type the option keys) or TapMethodArrows
	// (move the selection, then Enter).
	Method string `json:"method,omitempty"`
	// Submit is TapSubmitEnter when Send ends with Enter, or
	// TapSubmitSeparate when the terminal is not known to be in line mode:
	// Send carries only the keys and EnterKey offers Enter as its own action.
	Submit string `json:"submit,omitempty"`
	// EnterKey is the byte sequence of the separate Enter action.
	EnterKey string `json:"enter_key,omitempty"`
}

// SessionPromptOption is one answer of a detected prompt.
type SessionPromptOption struct {
	Label   string `json:"label"`
	Keys    string `json:"keys,omitempty"`
	Default bool   `json:"default,omitempty"`
	// Send is the exact input that selects the option; empty when the
	// prompt is not tappable.
	Send string `json:"send,omitempty"`
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
