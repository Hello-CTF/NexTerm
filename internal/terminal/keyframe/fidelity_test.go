package keyframe

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// updateFidelity rewrites the fidelity fixture the frontend test replays in
// xterm.js (src/test/features/keyframeFidelity.test.ts).
var updateFidelity = flag.Bool("update-fidelity", false, "rewrite the keyframe fidelity fixture")

const fidelityFixture = "../../../src/test/fixtures/keyframeFidelity.json"

type fidelityStep struct {
	Out    string `json:"out,omitempty"`
	Resize []int  `json:"resize,omitempty"`
}

type fidelityCheckpoint struct {
	After int    `json:"after"`
	Cols  int    `json:"cols"`
	Rows  int    `json:"rows"`
	State string `json:"state"`
}

type fidelityScenario struct {
	Name        string               `json:"name"`
	Cols        int                  `json:"cols"`
	Rows        int                  `json:"rows"`
	Steps       []fidelityStep       `json:"steps"`
	Checkpoints []fidelityCheckpoint `json:"checkpoints"`
}

func out(text string) fidelityStep { return fidelityStep{Out: text} }
func resize(c, r int) fidelityStep { return fidelityStep{Resize: []int{c, r}} }
func lines(prefix string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(prefix)
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString("\r\n")
	}
	return b.String()
}

// fidelityScenarios exercise the state a keyframe must carry. Every step is a
// checkpoint, so the frontend test also continues each restored state with
// the remaining steps.
func fidelityScenarios() []fidelityScenario {
	return []fidelityScenario{
		{Name: "renditions", Cols: 30, Rows: 6, Steps: []fidelityStep{
			out("\x1b[1;31mbold red\x1b[0m \x1b[3;4:3;58;5;196mcurly\x1b[24m \x1b[38;5;208m256\x1b[38;2;10;20;30m rgb\r\n"),
			out("\x1b[7minverse\x1b[27m \x1b[9mstrike\x1b[29m \x1b[2;53mdim over\x1b[0m\r\n\x1b[44mblue bg"),
			out("\x1b[K\r\n\x1b[42m\x1b[2K\x1b[0mafter erase with green"),
			out("\x1b[93;104mbright\x1b[39;49m reset \x1b[1mstill bold"),
		}},
		{Name: "wide-and-combining", Cols: 12, Rows: 5, Steps: []fidelityStep{
			out("中文字符宽度\r\né café ä\r\n"),
			out("1234567890中x\r\n"),
			out("\x1b[2;5H\x1b[31mZ\x1b[0m\x1b[3;11HW"),
		}},
		{Name: "autowrap-and-pending", Cols: 10, Rows: 4, Steps: []fidelityStep{
			out("abcdefghijKLMNOPQRSTuvw"),
			out("\r\n0123456789"),
			out("x\x1b[?7lYYYYYYYYYYZZ\x1b[?7h"),
			out("\r\n\x1b[41m          \x1b[0m"),
		}},
		{Name: "scrollback-and-regions", Cols: 16, Rows: 5, Steps: []fidelityStep{
			out(lines("line", 30)),
			out("\x1b[2;4r\x1b[4;1Hregion1\nregion2\nregion3\x1b[r"),
			out("\x1b[3;5r\x1b[3;1H\x1b[2L\x1b[1M\x1b[2S\x1b[1T\x1bM\x1b[5;1H\x1bD\x1b[r"),
			out("\x1b[3J\x1b[H\x1b[2Jcleared"),
		}},
		{Name: "alt-screen-saved-cursor", Cols: 20, Rows: 6, Steps: []fidelityStep{
			out("\x1b[32mmain line\x1b[0m\r\nsecond\x1b[2;4H"),
			out("\x1b[?1049h\x1b[44m\x1b[2J\x1b[Halt top\x1b[3;3H\x1b[1mbold alt"),
			out("\x1b[2;5r\x1b7\x1b[5;1Hsaved"),
			out("\x1b8X\x1b[r\x1b[?1049lback"),
		}},
		{Name: "resize-reflow", Cols: 20, Rows: 5, Steps: []fidelityStep{
			out(lines("wrapped content that is long ", 8) + "tail of the buffer that wraps around twice here"),
			resize(12, 5),
			resize(30, 7),
			resize(8, 3),
			out("\r\nnew\x1b[31m colored\x1b[0m text that wraps"),
			resize(25, 6),
		}},
		{Name: "resize-rows", Cols: 15, Rows: 6, Steps: []fidelityStep{
			out(lines("r", 12) + "prompt$ "),
			resize(15, 3),
			resize(15, 9),
			out("\x1b[2;1Hmid\x1b[9;1Hbottom"),
			resize(15, 4),
		}},
		{Name: "tabs-charsets-modes", Cols: 24, Rows: 5, Steps: []fidelityStep{
			out("a\tb\tc\x1b[3g\x1b[5G\x1bH\x1b[13G\x1bH\r\nx\ty\tz"),
			out("\r\n\x1b(0lqqk\x1b(B \x1b)0\x0elqk\x0f plain"),
			out("\x1b)0\x0e"),
			out("lqqk\x0f\r\n\x1b[4hINS\x1b[1;1H>\x1b[4l"),
			out("\x1b[?6h\x1b[2;4r\x1b[1;1Horigin\x1b[?6l\x1b[r\x1b[?25l"),
		}},
		{Name: "rep-ech-dch-ich", Cols: 20, Rows: 4, Steps: []fidelityStep{
			out("ab\x1b[3bcd\r\n0123456789\x1b[5G\x1b[3P\x1b[2@\x1b[43m\x1b[2X\x1b[0m"),
			out("\r\n\x1b[45mbg\x1b[0m\x1b[1K end"),
			out("\x1b#8"),
		}},
		{Name: "soft-and-full-reset", Cols: 16, Rows: 4, Steps: []fidelityStep{
			out("\x1b[1;34mstyled\x1b[2;3r\x1b[?6h\x1b[4h"),
			out("\x1b[!pafter soft"),
			out("\x1bcafter full"),
		}},
	}
}

// randomScenario mixes the operations above with a fixed seed, so the fixture
// stays reproducible while covering combinations nobody wrote by hand.
func randomScenario(seed uint64) fidelityScenario {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	cols, rows := 8+rng.IntN(20), 3+rng.IntN(6)
	texts := []string{"hello", "wide中文字", "e\u0301x", "tab\tstop", "a b  c", "1234567890123456789", "\u00e9\u0300\u0301", "\x1b(0lqk\x1b(B"}
	ops := []func() string{
		func() string { return texts[rng.IntN(len(texts))] },
		func() string { return "\r\n" },
		func() string { return "\n" },
		func() string { return fmt.Sprintf("\x1b[%d;%dH", 1+rng.IntN(rows+1), 1+rng.IntN(cols+2)) },
		func() string {
			return fmt.Sprintf("\x1b[%d;%dm", []int{0, 1, 2, 3, 4, 7, 9, 22, 24, 27}[rng.IntN(10)], 30+rng.IntN(8))
		},
		func() string { return fmt.Sprintf("\x1b[%dm", 40+rng.IntN(8)) },
		func() string { return fmt.Sprintf("\x1b[48;5;%dm", rng.IntN(256)) },
		func() string { return "\x1b[0m" },
		func() string { return fmt.Sprintf("\x1b[%dJ", rng.IntN(4)) },
		func() string { return fmt.Sprintf("\x1b[%dK", rng.IntN(3)) },
		func() string { return fmt.Sprintf("\x1b[%d%c", 1+rng.IntN(3), "LMPX@ST"[rng.IntN(7)]) },
		func() string {
			top := 1 + rng.IntN(rows)
			return fmt.Sprintf("\x1b[%d;%dr", top, top+rng.IntN(rows))
		},
		func() string { return "\x1b[r" },
		func() string { return []string{"\x1b7", "\x1b8", "\x1b[s", "\x1b[u"}[rng.IntN(4)] },
		func() string { return []string{"\x1b[?1049h", "\x1b[?1049l", "\x1b[?47h", "\x1b[?47l"}[rng.IntN(4)] },
		func() string {
			return []string{"\x1b[?6h", "\x1b[?6l", "\x1b[?7l", "\x1b[?7h", "\x1b[4h", "\x1b[4l"}[rng.IntN(6)]
		},
		func() string {
			return []string{"\x1bM", "\x1bD", "\x1bE", "\x1bH", "\x1b[g", "\x1b[3g", "\x1b[2I", "\x1b[Z"}[rng.IntN(8)]
		},
		func() string { return []string{"\x1b)0\x0e", "\x0f", "\x1b(A#", "\x1b(B"}[rng.IntN(4)] },
		func() string { return fmt.Sprintf("x\x1b[%db", 1+rng.IntN(4)) },
	}
	sc := fidelityScenario{Name: fmt.Sprintf("random-%d", seed), Cols: cols, Rows: rows}
	for step := 0; step < 10; step++ {
		if rng.IntN(5) == 0 {
			sc.Steps = append(sc.Steps, resize(4+rng.IntN(26), 2+rng.IntN(8)))
			continue
		}
		var b strings.Builder
		for n := 3 + rng.IntN(12); n > 0; n-- {
			b.WriteString(ops[rng.IntN(len(ops))]())
		}
		sc.Steps = append(sc.Steps, out(b.String()))
	}
	return sc
}

func buildFixture(t *testing.T) []fidelityScenario {
	t.Helper()
	scenarios := fidelityScenarios()
	for seed := uint64(1); seed <= 60; seed++ {
		scenarios = append(scenarios, randomScenario(seed))
	}
	for si := range scenarios {
		sc := &scenarios[si]
		screen := New(sc.Cols, sc.Rows)
		for i, step := range sc.Steps {
			if step.Resize != nil {
				screen.Resize(step.Resize[0], step.Resize[1])
			} else {
				screen.Write([]byte(step.Out))
			}
			cols, rows := screen.Size()
			sc.Checkpoints = append(sc.Checkpoints, fidelityCheckpoint{After: i + 1, Cols: cols, Rows: rows, State: screen.State()})
		}
	}
	return scenarios
}

// TestFidelityFixtureIsCurrent keeps the committed fixture in step with the
// model: the frontend test checks the states against xterm.js.
func TestFidelityFixtureIsCurrent(t *testing.T) {
	data, err := json.MarshalIndent(buildFixture(t), "", " ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.FromSlash(fidelityFixture)
	if *updateFidelity {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run go test ./internal/terminal/keyframe -update-fidelity)", path, err)
	}
	if !bytes.Equal(current, data) {
		t.Fatalf("%s is stale: run go test ./internal/terminal/keyframe -update-fidelity", path)
	}
}
