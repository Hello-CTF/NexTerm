package shellintegr

import (
	"bytes"
	"strconv"
)

// CommandState is a snapshot of the command lifecycle derived from OSC 133
// sequences. It is the only command state later AI and frontend slices need.
type CommandState struct {
	// Running reports whether a command is executing, i.e. an OSC 133;B start
	// has been seen without a matching 133;D finish or 133;A prompt.
	Running bool
	// Sequence counts commands started since the tracker was created. It
	// stays 0 until the first command start, so a fresh or 133-free shell
	// reports 0.
	Sequence uint64
	// LastExitCode is the exit code from the most recent OSC 133;D report
	// that carried one.
	LastExitCode int
	// HasLastExitCode reports whether any 133;D report has supplied an exit
	// code yet.
	HasLastExitCode bool
}

// CommandTracker extracts OSC 133 command lifecycle reports from a terminal
// output stream as it is fed through the recording path. It is passive: it
// works for remote shells that emit OSC 133 without any injected
// configuration, and ignores malformed or foreign sequences. Observe only
// reads its input; the recorded and replayed bytes stay untouched.
//
// CommandTracker is not safe for concurrent use; feed it from the single
// goroutine that owns the output stream.
type CommandTracker struct {
	pending []byte
	state   CommandState
}

func NewCommandTracker() *CommandTracker { return &CommandTracker{} }

// State returns the current command lifecycle snapshot.
func (t *CommandTracker) State() CommandState { return t.state }

// Observe scans p for OSC 133 sequences without modifying p. It returns the
// state after scanning and whether the state changed within p; sequences
// split across chunk boundaries are picked up once the completing chunk
// arrives.
func (t *CommandTracker) Observe(p []byte) (CommandState, bool) {
	before := t.state
	tail := scanOSC(pendingJoin(t.pending, p), func(payload []byte) {
		letter, exitCode, hasExit, ok := parseOSC133(payload)
		if !ok {
			return
		}
		t.apply(letter, exitCode, hasExit)
	})
	t.pending = append(t.pending[:0], tail...)
	return t.state, t.state != before
}

func (t *CommandTracker) apply(letter byte, exitCode int, hasExit bool) {
	switch letter {
	case 'A':
		t.state.Running = false
	case 'B':
		if !t.state.Running {
			t.state.Running = true
			t.state.Sequence++
		}
	case 'D':
		t.state.Running = false
		if hasExit {
			t.state.LastExitCode = exitCode
			t.state.HasLastExitCode = true
		}
	}
}

// parseOSC133 parses an OSC payload of the form 133;<letter>[;<exit>],
// returning the lifecycle letter, the optional exit code, and whether the
// payload was a well-formed OSC 133 sequence at all.
func parseOSC133(payload []byte) (letter byte, exitCode int, hasExit bool, ok bool) {
	code, rest, found := bytes.Cut(payload, []byte(";"))
	if !found || string(code) != "133" {
		return 0, 0, false, false
	}
	letterBytes, params, _ := bytes.Cut(rest, []byte(";"))
	if len(letterBytes) != 1 {
		return 0, 0, false, false
	}
	if len(params) > 0 {
		if value, err := strconv.Atoi(string(params)); err == nil && value >= 0 {
			return letterBytes[0], value, true, true
		}
	}
	return letterBytes[0], 0, false, true
}
