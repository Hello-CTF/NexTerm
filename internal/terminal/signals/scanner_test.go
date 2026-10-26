package signals

import (
	"encoding/json"
	"math/rand"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

type wantEvent struct {
	Kind    string `json:"kind"`
	Source  string `json:"source"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Exit    int    `json:"exit"`
	HasExit bool   `json:"has_exit"`
}

type goldenCase struct {
	Name   string      `json:"name"`
	Input  string      `json:"input"`
	Events []wantEvent `json:"events"`
}

func loadCases(t *testing.T) []goldenCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []goldenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

// project reduces events to comparable form, dropping the timestamp.
func project(events []Event) []wantEvent {
	out := make([]wantEvent, 0, len(events))
	for _, e := range events {
		out = append(out, wantEvent{
			Kind: e.Kind.String(), Source: e.Source, Title: e.Title, Body: e.Body,
			Exit: e.ExitCode, HasExit: e.HasExitCode,
		})
	}
	return out
}

func normalize(w []wantEvent) []wantEvent {
	out := make([]wantEvent, 0, len(w))
	out = append(out, w...)
	return out
}

func feedSplit(input []byte, cuts []int) []Event {
	s := NewScanner(Config{})
	var out []Event
	prev := 0
	for _, c := range append(cuts, len(input)) {
		out = append(out, s.Feed(input[prev:c], t0)...)
		prev = c
	}
	return out
}

func TestGolden(t *testing.T) {
	for _, tc := range loadCases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			input := []byte(tc.Input)
			want := normalize(tc.Events)
			if got := project(feedSplit(input, nil)); !reflect.DeepEqual(got, want) {
				t.Fatalf("whole feed:\n got %+v\nwant %+v", got, want)
			}
			// Every two-way split.
			if len(input) <= 600 {
				for i := 1; i < len(input); i++ {
					if got := project(feedSplit(input, []int{i})); !reflect.DeepEqual(got, want) {
						t.Fatalf("split at %d:\n got %+v\nwant %+v", i, got, want)
					}
				}
			}
			// Byte by byte.
			cuts := make([]int, 0, len(input))
			for i := 1; i < len(input); i++ {
				cuts = append(cuts, i)
			}
			if got := project(feedSplit(input, cuts)); !reflect.DeepEqual(got, want) {
				t.Fatalf("byte-wise:\n got %+v\nwant %+v", got, want)
			}
			// Seeded random multi-way splits.
			rng := rand.New(rand.NewSource(42))
			for range 50 {
				var cs []int
				for pos := 0; ; {
					pos += 1 + rng.Intn(64)
					if pos >= len(input) {
						break
					}
					cs = append(cs, pos)
				}
				if got := project(feedSplit(input, cs)); !reflect.DeepEqual(got, want) {
					t.Fatalf("random split %v:\n got %+v\nwant %+v", cs, got, want)
				}
			}
		})
	}
}

func TestFeedDoesNotModifyInput(t *testing.T) {
	input := []byte("\x1b]9;hello\x07\x07\x1b]133;B\x07\x1b]133;D;0\x1b\\")
	orig := append([]byte(nil), input...)
	NewScanner(Config{}).Feed(input, t0)
	if string(input) != string(orig) {
		t.Fatal("input was modified")
	}
}

func TestInvalidUTF8IsReplaced(t *testing.T) {
	events := NewScanner(Config{}).Feed([]byte("\x1b]9;a\xffb\x07"), t0)
	if len(events) != 1 || events[0].Body != "a\uFFFDb" {
		t.Fatalf("events = %+v", events)
	}
}

func TestBellRateLimit(t *testing.T) {
	s := NewScanner(Config{})
	count := func(at time.Duration) int {
		return len(s.Feed([]byte{0x07}, t0.Add(at)))
	}
	steps := []struct {
		at   time.Duration
		want int
	}{
		{0, 1},
		{200 * time.Millisecond, 0},
		{999 * time.Millisecond, 0},
		{time.Second, 1},
		{1500 * time.Millisecond, 0},
		{2100 * time.Millisecond, 1},
	}
	for _, st := range steps {
		if got := count(st.at); got != st.want {
			t.Fatalf("bell at %v: got %d events, want %d", st.at, got, st.want)
		}
	}
	if got := s.Stats().RateLimited; got != 3 {
		t.Fatalf("RateLimited = %d, want 3", got)
	}
}

func TestBellClockGoingBackwardsIsNotSuppressed(t *testing.T) {
	s := NewScanner(Config{})
	s.Feed([]byte{0x07}, t0.Add(10*time.Second))
	if got := s.Feed([]byte{0x07}, t0); len(got) != 1 {
		t.Fatalf("got %d events", len(got))
	}
}

func TestNotifyRateLimitWindow(t *testing.T) {
	s := NewScanner(Config{NotifyBurst: 2, NotifyWindow: time.Second})
	feed := func(at time.Duration) int {
		return len(s.Feed([]byte("\x1b]9;x\x07"), t0.Add(at)))
	}
	for _, st := range []struct {
		at   time.Duration
		want int
	}{
		{0, 1}, {100 * time.Millisecond, 1}, {200 * time.Millisecond, 0},
		{1050 * time.Millisecond, 1}, {1100 * time.Millisecond, 1}, {1200 * time.Millisecond, 0},
	} {
		if got := feed(st.at); got != st.want {
			t.Fatalf("notify at %v: got %d, want %d", st.at, got, st.want)
		}
	}
}

func TestCommandDuration(t *testing.T) {
	s := NewScanner(Config{})
	s.Feed([]byte("\x1b]133;C\x07"), t0)
	events := s.Feed([]byte("\x1b]133;D;3\x07"), t0.Add(90*time.Second))
	if len(events) != 1 || events[0].Kind != KindCommandEnd {
		t.Fatalf("events = %+v", events)
	}
	e := events[0]
	if e.Duration != 90*time.Second || e.ExitCode != 3 || !e.HasExitCode || !e.At.Equal(t0.Add(90*time.Second)) {
		t.Fatalf("event = %+v", e)
	}
}

func TestOversizeIsCountedAndRecovers(t *testing.T) {
	s := NewScanner(Config{})
	big := "\x1b]9;" + strings.Repeat("x", MaxOSCPayload+10) + "\x07"
	if got := s.Feed([]byte(big), t0); len(got) != 0 {
		t.Fatalf("oversize produced %+v", got)
	}
	if s.Stats().Oversize != 1 {
		t.Fatalf("stats = %+v", s.Stats())
	}
	got := s.Feed([]byte("\x1b]9;fine\x07"), t0)
	if len(got) != 1 || got[0].Body != "fine" {
		t.Fatalf("recovery = %+v", got)
	}
}

func TestAssemblyEviction(t *testing.T) {
	s := NewScanner(Config{NotifyBurst: 100})
	for i := range MaxAssemblies + 1 {
		s.Feed([]byte("\x1b]99;i="+string(rune('a'+i))+":d=0;x\x1b\\"), t0)
	}
	if len(s.assemblies) != MaxAssemblies {
		t.Fatalf("assemblies = %d", len(s.assemblies))
	}
	// The oldest ('a') was evicted, so its final chunk starts afresh.
	got := s.Feed([]byte("\x1b]99;i=a;y\x1b\\"), t0)
	if len(got) != 1 || got[0].Title != "y" {
		t.Fatalf("got %+v", got)
	}
}

func TestCommandDurationStartsAtC(t *testing.T) {
	s := NewScanner(Config{})
	if got := s.Feed([]byte("\x1b]133;A\x07\x1b]133;B\x07"), t0); len(got) != 0 {
		t.Fatalf("prompt produced %+v", got)
	}
	started := s.Feed([]byte("\x1b]133;C\x07"), t0.Add(10*time.Minute))
	if len(started) != 1 || started[0].Kind != KindCommandStart || !started[0].At.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("start = %+v", started)
	}
	ended := s.Feed([]byte("\x1b]133;D;0\x07"), t0.Add(10*time.Minute+time.Second))
	if len(ended) != 1 || ended[0].Duration != time.Second {
		t.Fatalf("end = %+v", ended)
	}
}

func TestCommandWithoutCFallsBackToB(t *testing.T) {
	s := NewScanner(Config{})
	s.Feed([]byte("\x1b]133;B\x07"), t0)
	got := s.Feed([]byte("\x1b]133;D;1\x07"), t0.Add(5*time.Second))
	if len(got) != 2 || got[0].Kind != KindCommandStart || !got[0].At.Equal(t0) ||
		got[1].Kind != KindCommandEnd || got[1].Duration != 5*time.Second {
		t.Fatalf("got %+v", got)
	}
}

func TestRawC1BytesAreIgnored(t *testing.T) {
	// Bare 0x80-0x9F bytes are not UTF-8 C1 controls and must not open or
	// close strings.
	events := NewScanner(Config{}).Feed([]byte("\x9d0;t\x07\x90q\x9c\x07"), t0)
	if len(events) != 1 || events[0].Kind != KindBell {
		t.Fatalf("events = %+v", events)
	}
}

func TestConEmuFilterIsCounted(t *testing.T) {
	s := NewScanner(Config{})
	s.Feed([]byte("\x1b]9;4;1;50\x07\x1b]9;42\x07"), t0)
	if s.Stats().ConEmuFiltered != 2 {
		t.Fatalf("stats = %+v", s.Stats())
	}
}

func TestSuppressedNotifyDoesNotCountMalformed(t *testing.T) {
	s := NewScanner(Config{NotifyBurst: 1})
	s.Feed([]byte("\x1b]9;a\x07\x1b]9;b\x07"), t0)
	if st := s.Stats(); st.RateLimited != 1 || st.Malformed != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestAtPromptFollowsShellLifecycle(t *testing.T) {
	s := NewScanner(Config{})
	if s.AtPrompt() {
		t.Fatal("at prompt before any OSC 133")
	}
	steps := []struct {
		feed string
		want bool
	}{
		{"\x1b]133;A\x07$ ", true},
		{"make", true},
		// The NexTerm integration reports B together with C.
		{"\x1b]133;B\x1b\\\x1b]133;C\x1b\\", false},
		{"output\r\n", false},
		{"\x1b]133;D;0\x1b\\", true},
		{"\x1b]133;A\x07$ \x1b]133;B\x07", true},
		{"\x1b]133;C\x07", false},
	}
	for i, step := range steps {
		s.Feed([]byte(step.feed), t0)
		if s.AtPrompt() != step.want {
			t.Fatalf("step %d (%q): at prompt %v, want %v", i, step.feed, s.AtPrompt(), step.want)
		}
	}
}

func BenchmarkFeedPlainOutput(b *testing.B) {
	data := []byte(strings.Repeat("some ordinary terminal output line\r\n", 100))
	s := NewScanner(Config{})
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		s.Feed(data, t0)
	}
}
