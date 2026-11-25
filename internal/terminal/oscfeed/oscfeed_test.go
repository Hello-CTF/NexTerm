package oscfeed

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

type recorder struct {
	parser  *ansi.Parser
	feeder  *Feeder
	printed []rune
	oscs    []string
	cmds    []int
}

func newRecorder(limit int) *recorder {
	r := &recorder{parser: ansi.NewParser()}
	r.parser.SetHandler(ansi.Handler{Print: func(ch rune) { r.printed = append(r.printed, ch) }})
	r.feeder = New(r.parser, limit, func(cmd int, data []byte) {
		r.cmds = append(r.cmds, cmd)
		r.oscs = append(r.oscs, string(data))
	})
	return r
}

// "看" is E7 9C 8B: its middle byte is the 8-bit string terminator.
const titleWithST = "2;发布看板"

func TestUTF8ContinuationByteDoesNotEndTheString(t *testing.T) {
	for name, terminator := range map[string]string{"BEL": "\x07", "ESC \\": "\x1b\\", "8-bit ST": "\x9c"} {
		r := newRecorder(1024)
		r.feeder.Write([]byte("a\x1b]" + titleWithST + terminator + "b"))
		if len(r.oscs) != 1 || r.oscs[0] != titleWithST || r.cmds[0] != 2 {
			t.Errorf("%s: OSC strings = %q, commands = %v; want one %q with command 2", name, r.oscs, r.cmds, titleWithST)
		}
		if got := string(r.printed); got != "ab" {
			t.Errorf("%s: printed %q, want %q", name, got, "ab")
		}
	}
}

func TestStringSplitAcrossWritesInsideACharacter(t *testing.T) {
	r := newRecorder(1024)
	raw := []byte("\x1b]" + titleWithST + "\x07")
	for i := range raw {
		r.feeder.Write(raw[i : i+1])
	}
	if len(r.oscs) != 1 || r.oscs[0] != titleWithST {
		t.Fatalf("OSC strings = %q, want %q", r.oscs, titleWithST)
	}
}

func TestCancelledStringIsDroppedAndEscapeStartsTheNextSequence(t *testing.T) {
	r := newRecorder(1024)
	r.feeder.Write([]byte("\x1b]2;dropped\x18x\x1b]0;kept\x1b[1mz"))
	if len(r.oscs) != 1 || r.oscs[0] != "0;kept" || r.cmds[0] != 0 {
		t.Fatalf("OSC strings = %q, commands = %v; want only %q", r.oscs, r.cmds, "0;kept")
	}
	if got := string(r.printed); got != "xz" {
		t.Fatalf("printed %q, want %q: the CSI after the ESC must be parsed, not printed", got, "xz")
	}
}

func TestDataPastTheLimitIsDropped(t *testing.T) {
	r := newRecorder(4)
	r.feeder.Write([]byte("\x1b]2;abcdef\x07"))
	if len(r.oscs) != 1 || r.oscs[0] != "2;ab" {
		t.Fatalf("OSC strings = %q, want the first 4 bytes", r.oscs)
	}
}
