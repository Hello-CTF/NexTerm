package supervisor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/pty"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// The state corpus lives in testdata/states. Each file is JSON lines: a
// header object, then events [seconds, kind, data] in time order, where kind
// is "o" (output), "i" (input written by a client) or "x" (process exit,
// data is the exit code). The header names the expected state sequence
// (consecutive repeats collapsed, the initial idle left out) with
// needs_me qualified by its confidence, and optionally the prompt expected
// at the last heuristic needs_me.
type corpusHeader struct {
	Version     int    `json:"version"`
	Name        string `json:"name"`
	Origin      string `json:"origin"`
	Description string `json:"description"`
	Cols        int    `json:"cols"`
	Rows        int    `json:"rows"`
	// ShellLeader marks a session started as an interactive shell.
	ShellLeader bool `json:"shell_leader,omitempty"`
	// Foreground describes the terminal during the recording: Job reports
	// a program other than the session leader in the foreground, Canonical
	// the line mode (nil when unknown).
	Foreground struct {
		Job       bool  `json:"job"`
		Canonical *bool `json:"canonical"`
	} `json:"foreground"`
	Expect       []string      `json:"expect"`
	ExpectPrompt *corpusPrompt `json:"expect_prompt,omitempty"`
	// ExpectNotable, when set, is whether the last finished command is
	// notable.
	ExpectNotable *bool  `json:"expect_notable,omitempty"`
	KnownMiss     string `json:"known_miss,omitempty"`
}

type corpusPrompt struct {
	Kind     string `json:"kind"`
	Tappable bool   `json:"tappable"`
	Submit   string `json:"submit,omitempty"`
	Blocked  string `json:"blocked,omitempty"`
}

type corpusEvent struct {
	at   time.Duration
	kind string
	data string
	code int
}

func loadCorpus(t testing.TB, path string) (corpusHeader, []corpusEvent) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	var header corpusHeader
	var events []corpusEvent
	line := 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		if line == 1 {
			if err := json.Unmarshal(raw, &header); err != nil {
				t.Fatalf("%s: header: %v", path, err)
			}
			continue
		}
		var fields []json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 3 {
			t.Fatalf("%s:%d: event must be [seconds, kind, data]: %v", path, line, err)
		}
		var seconds float64
		var event corpusEvent
		if err := json.Unmarshal(fields[0], &seconds); err != nil {
			t.Fatalf("%s:%d: %v", path, line, err)
		}
		if err := json.Unmarshal(fields[1], &event.kind); err != nil {
			t.Fatalf("%s:%d: %v", path, line, err)
		}
		event.at = time.Duration(seconds * float64(time.Second))
		switch event.kind {
		case "o", "i":
			err = json.Unmarshal(fields[2], &event.data)
		case "x":
			err = json.Unmarshal(fields[2], &event.code)
		default:
			err = fmt.Errorf("unknown event kind %q", event.kind)
		}
		if err != nil {
			t.Fatalf("%s:%d: %v", path, line, err)
		}
		if n := len(events); n > 0 && event.at < events[n-1].at {
			t.Fatalf("%s:%d: events out of time order", path, line)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if header.Version != 1 || header.Name == "" || len(header.Expect) == 0 {
		t.Fatalf("%s: incomplete header %+v", path, header)
	}
	return header, events
}

// replayResult is what a corpus replay observed.
type replayResult struct {
	states []string
	// prompt is the prompt published at the last heuristic needs_me.
	prompt *SessionPrompt
	// notable is whether the last just_finished was notable.
	notable bool
	// inspections counts prompt detector runs, outputs the output chunks.
	inspections uint64
	outputs     int
}

// replayCorpus feeds a recording to a tracker on a fake clock, running the
// idle transition exactly when the idle threshold elapses between events.
func replayCorpus(header corpusHeader, events []corpusEvent) replayResult {
	start := stateEpoch0
	foreground := pty.Foreground{LeaderPID: 100, ProcessGroup: 100}
	if header.Foreground.Job {
		foreground.ProcessGroup = 200
	}
	if header.Foreground.Canonical != nil {
		foreground.CanonicalKnown = true
		foreground.Canonical = *header.Foreground.Canonical
	}
	var result replayResult
	var tracker *stateTracker
	record := func() {
		state := tracker.snapshot(false, 0)
		label := state.State
		if state.State == base.SessionJustFinished {
			result.notable = state.Notable
		}
		if state.State == base.SessionNeedsMe {
			label += "/" + state.Confidence
			if state.Confidence == base.ConfidenceHeuristic {
				result.prompt = state.Prompt
			}
		}
		if n := len(result.states); n == 0 || result.states[n-1] != label {
			result.states = append(result.states, label)
		}
	}
	tracker = newStateTracker(stateOptions{
		cols: header.Cols, rows: header.Rows,
		shellLeader: header.ShellLeader,
		foreground:  func() (pty.Foreground, error) { return foreground, nil },
		seq:         new(atomic.Uint64),
		onChange:    func() { record() },
	}, start)
	idleUntil := func(now time.Time) {
		for {
			due, ok := tracker.due()
			if !ok || due.After(now) {
				return
			}
			tracker.tick(due)
		}
	}
	var last time.Time
	for _, event := range events {
		now := start.Add(event.at)
		idleUntil(now)
		switch event.kind {
		case "o":
			result.outputs++
			tracker.output([]byte(event.data), now)
		case "i":
			tracker.input(now)
		case "x":
			code := event.code
			tracker.exit(&code, now)
		}
		last = now
	}
	idleUntil(last.Add(time.Hour))
	tracker.mu.Lock()
	result.inspections = tracker.inspections
	tracker.mu.Unlock()
	return result
}

func TestStateCorpusReplay(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "states", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no state corpus files")
	}
	sort.Strings(paths)
	matched, misses := 0, 0
	for _, path := range paths {
		header, events := loadCorpus(t, path)
		t.Run(header.Name, func(t *testing.T) {
			got := replayCorpus(header, events)
			problem := ""
			if strings.Join(got.states, " ") != strings.Join(header.Expect, " ") {
				problem = fmt.Sprintf("states %v, want %v", got.states, header.Expect)
			} else if want := header.ExpectPrompt; want != nil {
				p := got.prompt
				switch {
				case p == nil:
					problem = "no prompt at needs_me"
				case p.Kind != want.Kind || p.Tappable != want.Tappable || p.Submit != want.Submit || p.Blocked != want.Blocked:
					problem = fmt.Sprintf("prompt %s tappable=%v submit=%q blocked=%q, want %+v", p.Kind, p.Tappable, p.Submit, p.Blocked, *want)
				}
			}
			if want := header.ExpectNotable; problem == "" && want != nil && *want != got.notable {
				problem = fmt.Sprintf("last finished command notable %v, want %v", got.notable, *want)
			}
			t.Logf("%s (%s): %d output chunks, %d prompt inspections, states %v", header.Name, header.Origin, got.outputs, got.inspections, got.states)
			switch {
			case problem == "" && header.KnownMiss != "":
				t.Errorf("known miss %q now passes; drop the known_miss mark", header.KnownMiss)
			case problem == "":
				matched++
			case header.KnownMiss != "":
				misses++
				t.Logf("known miss (%s): %s", header.KnownMiss, problem)
			default:
				t.Error(problem)
			}
		})
	}
	t.Logf("state corpus: %d of %d recordings match their labelled state sequence, %d known misses", matched, len(paths), misses)
}

// TestStateCorpusDetectionOnlyOnIdle checks the detection frequency rule on
// every recording: the prompt detector runs exactly once per pause of the
// idle threshold after activity, never per output chunk.
func TestStateCorpusDetectionOnlyOnIdle(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "states", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		header, events := loadCorpus(t, path)
		var pauses uint64
		active, exited := false, false
		var lastActivity time.Duration
		for _, event := range events {
			if exited {
				break
			}
			if active && event.at-lastActivity >= defaultStateIdle {
				pauses++
				active = false
			}
			switch event.kind {
			case "o", "i":
				active, lastActivity = true, event.at
			case "x":
				exited = true
			}
		}
		if active && !exited {
			pauses++
		}
		got := replayCorpus(header, events)
		if got.inspections != pauses {
			t.Errorf("%s: %d inspections for %d output chunks, want one per pause (%d)", header.Name, got.inspections, got.outputs, pauses)
		}
	}
}
