package execbatch

import (
	"errors"
	"strings"
	"testing"
)

func TestParseDirective(t *testing.T) {
	long := strings.Repeat("a", MaxDirectiveTextRunes+1)
	tests := []struct {
		name        string
		line        string
		want        Directive
		isDirective bool
		malformed   bool
	}{
		{name: "ordinary", line: "hello world"},
		{name: "prefix not at start", line: " @NEXTERM:STEP x"},
		{name: "step", line: "@NEXTERM:STEP Install packages", isDirective: true, want: Directive{Kind: DirectiveStep, Text: "Install packages"}},
		{name: "step crlf", line: "@NEXTERM:STEP Install\r", isDirective: true, want: Directive{Kind: DirectiveStep, Text: "Install"}},
		{name: "step unicode", line: "@NEXTERM:STEP 安装依赖", isDirective: true, want: Directive{Kind: DirectiveStep, Text: "安装依赖"}},
		{name: "progress", line: "@NEXTERM:PROGRESS 42", isDirective: true, want: Directive{Kind: DirectiveProgress, Progress: 42}},
		{name: "progress zero", line: "@NEXTERM:PROGRESS 0", isDirective: true, want: Directive{Kind: DirectiveProgress}},
		{name: "progress max", line: "@NEXTERM:PROGRESS 100", isDirective: true, want: Directive{Kind: DirectiveProgress, Progress: 100}},
		{name: "progress over", line: "@NEXTERM:PROGRESS 101", isDirective: true, malformed: true},
		{name: "progress negative", line: "@NEXTERM:PROGRESS -1", isDirective: true, malformed: true},
		{name: "progress text", line: "@NEXTERM:PROGRESS half", isDirective: true, malformed: true},
		{name: "progress huge", line: "@NEXTERM:PROGRESS 99999999999999999999", isDirective: true, malformed: true},
		{name: "input", line: "@NEXTERM:INPUT port Listen port", isDirective: true, want: Directive{Kind: DirectiveInput, Variable: "port", Text: "Listen port"}},
		{name: "input default", line: "@NEXTERM:INPUT port Listen port\t8080", isDirective: true, want: Directive{Kind: DirectiveInput, Variable: "port", Text: "Listen port", Default: "8080"}},
		{name: "input empty default", line: "@NEXTERM:INPUT port Listen port\t", isDirective: true, want: Directive{Kind: DirectiveInput, Variable: "port", Text: "Listen port"}},
		{name: "input bad variable", line: "@NEXTERM:INPUT 1port Listen", isDirective: true, malformed: true},
		{name: "input dash variable", line: "@NEXTERM:INPUT my-port Listen", isDirective: true, malformed: true},
		{name: "input no prompt", line: "@NEXTERM:INPUT port", isDirective: true, malformed: true},
		{name: "input empty prompt", line: "@NEXTERM:INPUT port  ", isDirective: true, malformed: true},
		{name: "secret", line: "@NEXTERM:SECRET token API token", isDirective: true, want: Directive{Kind: DirectiveSecret, Variable: "token", Text: "API token"}},
		{name: "secret default rejected", line: "@NEXTERM:SECRET token API token\thunter2", isDirective: true, malformed: true},
		{name: "confirm", line: "@NEXTERM:CONFIRM Restart now?", isDirective: true, want: Directive{Kind: DirectiveConfirm, Text: "Restart now?"}},
		{name: "message", line: "@NEXTERM:MESSAGE warn disk is 91% full", isDirective: true, want: Directive{Kind: DirectiveMessage, Level: LevelWarn, Text: "disk is 91% full"}},
		{name: "message bad level", line: "@NEXTERM:MESSAGE fatal boom", isDirective: true, malformed: true},
		{name: "message no text", line: "@NEXTERM:MESSAGE info", isDirective: true, malformed: true},
		{name: "unknown", line: "@NEXTERM:EXEC rm -rf /", isDirective: true, malformed: true},
		{name: "lowercase command", line: "@NEXTERM:step x", isDirective: true, malformed: true},
		{name: "no args", line: "@NEXTERM:STEP", isDirective: true, malformed: true},
		{name: "empty title", line: "@NEXTERM:STEP    ", isDirective: true, malformed: true},
		{name: "escape sequence", line: "@NEXTERM:STEP \x1b[2Jcleared", isDirective: true, malformed: true},
		{name: "embedded cr", line: "@NEXTERM:STEP a\rb", isDirective: true, malformed: true},
		{name: "bidi override", line: "@NEXTERM:CONFIRM ‮enter password", isDirective: true, malformed: true},
		{name: "line separator", line: "@NEXTERM:STEP a b", isDirective: true, malformed: true},
		{name: "invalid utf8", line: "@NEXTERM:STEP \xff\xfe", isDirective: true, malformed: true},
		{name: "too long text", line: "@NEXTERM:STEP " + long, isDirective: true, malformed: true},
		{name: "too long line", line: "@NEXTERM:STEP " + strings.Repeat("a", MaxDirectiveLineBytes), isDirective: true, malformed: true},
		{name: "tab in title", line: "@NEXTERM:STEP a\tb", isDirective: true, malformed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, isDirective, err := ParseDirective(tt.line)
			if isDirective != tt.isDirective {
				t.Fatalf("isDirective = %v", isDirective)
			}
			if tt.malformed {
				if !errors.Is(err, ErrMalformedDirective) {
					t.Fatalf("err = %v, want malformed", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestValidAnswer(t *testing.T) {
	for value, want := range map[string]bool{
		"": true, "ok": true, "多字节": true, "a\nb": false, "a\rb": false, "a\x00": false,
		strings.Repeat("x", MaxAnswerBytes): true, strings.Repeat("x", MaxAnswerBytes+1): false, "\xff": false,
	} {
		if got := ValidAnswer(value); got != want {
			t.Errorf("ValidAnswer(%q) = %v", value, got)
		}
	}
}

type filterRecorder struct {
	out        strings.Builder
	directives []Directive
	malformed  []string
}

func (r *filterRecorder) filter() *directiveFilter {
	return newDirectiveFilter(
		func(chunk []byte) error { r.out.Write(chunk); return nil },
		func(d Directive) error { r.directives = append(r.directives, d); return nil },
		func(reason string) { r.malformed = append(r.malformed, reason) },
	)
}

func feed(t *testing.T, f *directiveFilter, input string, chunk int) {
	t.Helper()
	for start := 0; start < len(input); start += chunk {
		end := min(start+chunk, len(input))
		if err := f.Write([]byte(input[start:end])); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDirectiveFilterSplitsAcrossReads(t *testing.T) {
	input := "before\n@NEXTERM:STEP One\nmid line\r\n@NEXTERM:PROGRESS 50\r\n@NEXTERM:INPUT v Prompt\tdef\nafter\n@NEXT not a directive\n@NEXTERM:bogus x\ntail"
	wantOut := "before\nmid line\r\nafter\n@NEXT not a directive\n@NEXTERM:bogus x\ntail"
	for _, chunk := range []int{1, 2, 3, 7, 9, 10, 64, len(input)} {
		recorder := &filterRecorder{}
		feed(t, recorder.filter(), input, chunk)
		if recorder.out.String() != wantOut {
			t.Fatalf("chunk %d: output = %q", chunk, recorder.out.String())
		}
		if len(recorder.directives) != 3 || recorder.directives[0].Text != "One" || recorder.directives[1].Progress != 50 ||
			recorder.directives[2].Variable != "v" || recorder.directives[2].Default != "def" {
			t.Fatalf("chunk %d: directives = %+v", chunk, recorder.directives)
		}
		if len(recorder.malformed) != 1 {
			t.Fatalf("chunk %d: malformed = %v", chunk, recorder.malformed)
		}
	}
}

func TestDirectiveFilterEndOfStream(t *testing.T) {
	recorder := &filterRecorder{}
	feed(t, recorder.filter(), "ok\n@NEXTERM:STEP last", 4)
	if recorder.out.String() != "ok\n" || len(recorder.directives) != 1 || recorder.directives[0].Text != "last" {
		t.Fatalf("out=%q directives=%+v", recorder.out.String(), recorder.directives)
	}
	recorder = &filterRecorder{}
	feed(t, recorder.filter(), "x\n@NEXTE", 3)
	if recorder.out.String() != "x\n@NEXTE" || len(recorder.directives) != 0 {
		t.Fatalf("partial prefix out=%q", recorder.out.String())
	}
	recorder = &filterRecorder{}
	feed(t, recorder.filter(), "@NEXTERM:STEP", 100)
	if recorder.out.String() != "@NEXTERM:STEP" || len(recorder.malformed) != 1 {
		t.Fatalf("unterminated malformed out=%q malformed=%v", recorder.out.String(), recorder.malformed)
	}
}

func TestDirectiveFilterLongLineStaysBoundedAndVisible(t *testing.T) {
	line := DirectivePrefix + "STEP " + strings.Repeat("a", 50_000)
	input := line + "\n@NEXTERM:STEP after\n"
	recorder := &filterRecorder{}
	f := recorder.filter()
	maxBuffered := 0
	for start := 0; start < len(input); start += 100 {
		end := min(start+100, len(input))
		if err := f.Write([]byte(input[start:end])); err != nil {
			t.Fatal(err)
		}
		maxBuffered = max(maxBuffered, len(f.buf)+len(f.out))
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if maxBuffered > MaxDirectiveLineBytes+256 {
		t.Fatalf("buffered %d bytes", maxBuffered)
	}
	if recorder.out.String() != line+"\n" {
		t.Fatalf("overlong directive was not passed through as ordinary output")
	}
	if len(recorder.malformed) != 1 || len(recorder.directives) != 1 || recorder.directives[0].Text != "after" {
		t.Fatalf("malformed=%v directives=%+v", recorder.malformed, recorder.directives)
	}
}

func TestDirectiveFilterHostileStreamIsBounded(t *testing.T) {
	// A hostile host repeats the prefix without ever ending a line.
	recorder := &filterRecorder{}
	f := recorder.filter()
	chunk := []byte(strings.Repeat(DirectivePrefix, 1000))
	for range 2000 {
		if err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
		if len(f.buf) > MaxDirectiveLineBytes+1 {
			t.Fatalf("buffer grew to %d", len(f.buf))
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if recorder.out.Len() != len(chunk)*2000 {
		t.Fatalf("lost bytes: %d of %d", recorder.out.Len(), len(chunk)*2000)
	}
}

func TestDirectiveFilterHandlerErrorStopsProcessing(t *testing.T) {
	boom := errors.New("boom")
	var out strings.Builder
	f := newDirectiveFilter(
		func(chunk []byte) error { out.Write(chunk); return nil },
		func(Directive) error { return boom },
		func(string) {},
	)
	if err := f.Write([]byte("a\n@NEXTERM:STEP x\nb\n")); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if err := f.Write([]byte("c")); !errors.Is(err, boom) {
		t.Fatalf("later write err = %v", err)
	}
	if out.String() != "a\n" {
		t.Fatalf("out = %q", out.String())
	}
}
