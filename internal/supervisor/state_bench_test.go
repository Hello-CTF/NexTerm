package supervisor

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/terminal/shape"
)

func benchBuildLog() []byte {
	var b strings.Builder
	for i := 0; b.Len() < 32<<10; i++ {
		fmt.Fprintf(&b, "\x1b[32m   Compiling\x1b[0m module-%04d v0.%d.0 (/home/user/project/crates/module-%04d)\r\n", i, i%10, i)
	}
	return []byte(b.String())
}

func benchTUIRedraw() []byte {
	var b strings.Builder
	frames := "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
	for i := 0; b.Len() < 32<<10; i++ {
		frame := []rune(frames)[i%10]
		fmt.Fprintf(&b, "\x1b[?2026h\x1b[3A\r\x1b[J%c Working... (%ds, esc to interrupt)\r\n\x1b[2m  tokens %d\x1b[0m\r\n> \x1b[?2026l", frame, i/10, i*37)
	}
	return []byte(b.String())
}

// BenchmarkStateOutput is the per-chunk cost detection adds to the helper's
// output path: the signal scanner and the screen model. The prompt detector
// does not run here; it runs once per pause (BenchmarkStateInspect).
func BenchmarkStateOutput(b *testing.B) {
	for name, chunk := range map[string][]byte{"build-log": benchBuildLog(), "tui-redraw": benchTUIRedraw()} {
		b.Run(name, func(b *testing.B) {
			tracker := newStateTracker(stateOptions{cols: 120, rows: 40}, stateEpoch0)
			now := stateEpoch0
			b.SetBytes(int64(len(chunk)))
			b.ReportAllocs()
			for b.Loop() {
				now = now.Add(time.Millisecond)
				tracker.output(chunk, now)
			}
		})
	}
}

// BenchmarkHistoryAppend is the existing recording cost of the same chunks,
// the baseline the state overhead compares against.
func BenchmarkHistoryAppend(b *testing.B) {
	chunk := benchBuildLog()
	recording, err := createHistoryRows(filepath.Join(b.TempDir()), MaxHistoryLimit, 40)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = recording.closeWriter() }()
	b.SetBytes(int64(len(chunk)))
	b.ReportAllocs()
	for b.Loop() {
		if err := recording.append(chunk); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkStateInspect is one idle transition: snapshot plus detector.
func BenchmarkStateInspect(b *testing.B) {
	for _, size := range [][2]int{{80, 24}, {200, 60}} {
		b.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(b *testing.B) {
			tracker := newStateTracker(stateOptions{cols: size[0], rows: size[1]}, stateEpoch0)
			tracker.output(benchBuildLog(), stateEpoch0)
			tracker.output([]byte("\r\nPick one:\r\n❯ 1. Alpha\r\n  2. Beta\r\n  3. Gamma\r\n"), stateEpoch0)
			b.ReportAllocs()
			for b.Loop() {
				tracker.mu.Lock()
				tracker.inspectLocked()
				tracker.mu.Unlock()
			}
			if tracker.prompt.Kind != shape.KindChoice {
				b.Fatalf("detected %s", tracker.prompt.Kind)
			}
		})
	}
}

// BenchmarkStateTrackerMemory reports the memory one session's tracker
// holds after a screenful of output (B/op).
func BenchmarkStateTrackerMemory(b *testing.B) {
	for _, size := range [][2]int{{80, 24}, {200, 60}} {
		b.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(b *testing.B) {
			chunk := benchBuildLog()
			b.ReportAllocs()
			for b.Loop() {
				tracker := newStateTracker(stateOptions{cols: size[0], rows: size[1]}, stateEpoch0)
				tracker.output(chunk, stateEpoch0)
			}
		})
	}
}
