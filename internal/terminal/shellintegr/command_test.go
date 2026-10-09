package shellintegr

import (
	"bytes"
	"testing"
)

func osc133(payload string) []byte {
	return []byte("\x1b]133;" + payload + "\x1b\\")
}

func TestCommandTrackerLifecycle(t *testing.T) {
	tr := NewCommandTracker()
	if state := tr.State(); state.Running || state.Sequence != 0 || state.HasLastExitCode {
		t.Fatalf("fresh State() = %+v; want idle zero state", state)
	}

	// Prompt start marks idle without counting a command.
	tr.Observe(osc133("A"))
	if state := tr.State(); state.Running || state.Sequence != 0 || state.HasLastExitCode {
		t.Fatalf("after A State() = %+v; want idle, sequence 0, no exit", state)
	}

	// Command start marks running and counts the command.
	tr.Observe(osc133("B"))
	if state := tr.State(); !state.Running || state.Sequence != 1 {
		t.Fatalf("after B State() = %+v; want running, sequence 1", state)
	}

	// Command finish clears running and records the exit code.
	tr.Observe(osc133("D;0"))
	if state := tr.State(); state.Running || state.Sequence != 1 || !state.HasLastExitCode || state.LastExitCode != 0 {
		t.Fatalf("after D;0 State() = %+v; want idle, sequence 1, exit 0", state)
	}

	// A second command advances the sequence and overwrites the exit code.
	tr.Observe(osc133("B"))
	tr.Observe(osc133("D;7"))
	if state := tr.State(); state.Running || state.Sequence != 2 || state.LastExitCode != 7 {
		t.Fatalf("after second command State() = %+v; want idle, sequence 2, exit 7", state)
	}
}

func TestCommandTrackerStartupPromptDoesNotCount(t *testing.T) {
	// bash and zsh emit a D from the first precmd before any command has run;
	// it must record an exit code without incrementing the sequence.
	tr := NewCommandTracker()
	tr.Observe(osc133("D;0"))
	tr.Observe(osc133("A"))
	state := tr.State()
	if state.Sequence != 0 {
		t.Fatalf("Sequence = %d after startup prompt; want 0", state.Sequence)
	}
	if !state.HasLastExitCode || state.LastExitCode != 0 {
		t.Fatalf("exit = %d (present=%v); want 0, true", state.LastExitCode, state.HasLastExitCode)
	}
}

func TestCommandTrackerIgnoresForeignAndMalformed(t *testing.T) {
	stream := []byte("\x1b[2J\x1b]0;title\x07 \x1b]7;file://h/tmp\x1b\\ \x1b]52;c;Y2xpcGJvYXJk\x07")
	stream = append(stream, osc133("X")...)      // unknown letter
	stream = append(stream, osc133("133")...)    // wrong code
	stream = append(stream, osc133("13;A")...)   // wrong code
	stream = append(stream, osc133("1333;A")...) // wrong code
	stream = append(stream, osc133("D;abc")...)  // non numeric exit
	stream = append(stream, osc133("D;-3")...)   // negative exit
	stream = append(stream, osc133("BC")...)     // multi byte letter
	tr := NewCommandTracker()
	if _, changed := tr.Observe(stream); changed {
		t.Fatal("Observe() reported a change for a stream with no valid OSC 133")
	}
	if state := tr.State(); state.Running || state.Sequence != 0 || state.HasLastExitCode {
		t.Fatalf("State() = %+v; want idle zero state after noise", state)
	}
}

func TestCommandTrackerFinishWithoutExitCode(t *testing.T) {
	tr := NewCommandTracker()
	tr.Observe(osc133("B"))
	tr.Observe(osc133("D")) // no exit code parameter
	state := tr.State()
	if state.Running {
		t.Fatal("still running after a bare D")
	}
	if state.HasLastExitCode {
		t.Fatalf("HasLastExitCode = true after a bare D; want false")
	}
	if state.Sequence != 1 {
		t.Fatalf("Sequence = %d; want 1", state.Sequence)
	}
}

func TestCommandTrackerConsecutiveStartDeduped(t *testing.T) {
	tr := NewCommandTracker()
	tr.Observe(osc133("B"))
	tr.Observe(osc133("B"))
	tr.Observe(osc133("B"))
	if state := tr.State(); state.Sequence != 1 {
		t.Fatalf("Sequence = %d after repeated B; want 1", state.Sequence)
	}
}

func TestCommandTrackerExitCodeBoundToCommand(t *testing.T) {
	tr := NewCommandTracker()
	tr.Observe(osc133("B"))
	tr.Observe(osc133("D;7"))
	if state := tr.State(); state.ExitCodeSequence != 1 || state.LastExitCode != 7 {
		t.Fatalf("State() = %+v; want exit 7 bound to sequence 1", state)
	}
	// The next command starts: the code stays bound to sequence 1 and must
	// not be attributed to the running command.
	tr.Observe(osc133("B"))
	if state := tr.State(); state.ExitCodeSequence != 1 {
		t.Fatalf("ExitCodeSequence = %d while command 2 runs; want 1", state.ExitCodeSequence)
	}
	// A bare D finishes command 2 without an exit code; the code remains
	// bound to sequence 1 instead of being lent to command 2.
	tr.Observe(osc133("D"))
	if state := tr.State(); state.ExitCodeSequence != 1 {
		t.Fatalf("ExitCodeSequence = %d after a bare D; want 1", state.ExitCodeSequence)
	}
	// Command 3 reports its own code, rebound to its sequence.
	tr.Observe(osc133("B"))
	tr.Observe(osc133("D;3"))
	if state := tr.State(); state.ExitCodeSequence != 3 || state.LastExitCode != 3 {
		t.Fatalf("State() = %+v; want exit 3 bound to sequence 3", state)
	}
}

func TestCommandTrackerAcrossChunkBoundaries(t *testing.T) {
	stream := append(append([]byte("prompt$ "), osc133("B")...), osc133("D;3")...)
	for split := 0; split <= len(stream); split++ {
		tr := NewCommandTracker()
		var state CommandState
		for _, chunk := range [][]byte{stream[:split], stream[split:]} {
			state, _ = tr.Observe(chunk)
		}
		if state.Sequence != 1 || state.Running || !state.HasLastExitCode || state.LastExitCode != 3 {
			t.Fatalf("split %d: State() = %+v; want idle, sequence 1, exit 3", split, state)
		}
	}
}

func TestCommandTrackerByteByByte(t *testing.T) {
	stream := append(append([]byte("\x1b]0;t\x07x"), osc133("B")...), osc133("D;5")...)
	tr := NewCommandTracker()
	for _, b := range stream {
		tr.Observe([]byte{b})
	}
	if state := tr.State(); state.Sequence != 1 || state.Running || state.LastExitCode != 5 {
		t.Fatalf("State() = %+v; want idle, sequence 1, exit 5", state)
	}
}

func TestCommandTrackerOverlongDropped(t *testing.T) {
	tr := NewCommandTracker()
	giant := append([]byte("\x1b]133;B;"), bytes.Repeat([]byte("9"), maxPending+64)...)
	if _, changed := tr.Observe(giant); changed {
		t.Fatal("Observe(overlong) reported a change")
	}
	if state := tr.State(); state.Sequence != 0 || state.Running {
		t.Fatalf("State() = %+v; want idle zero after overlong", state)
	}
	if _, changed := tr.Observe(append([]byte("9\x07"), osc133("D;2")...)); !changed {
		t.Fatal("Observe() after overlong did not pick up the next sequence")
	}
	if state := tr.State(); !state.HasLastExitCode || state.LastExitCode != 2 {
		t.Fatalf("State() = %+v; want exit 2 after resync", state)
	}
}

func TestCommandTrackerDoesNotAlterStream(t *testing.T) {
	stream := append(append([]byte("user@host:~$ "), osc133("B")...), osc133("D;1")...)
	stream = append(stream, []byte("\r\noutput\r\n")...)
	stream = append(stream, osc133("A")...)

	feed := func(t *testing.T, chunks [][]byte) {
		t.Helper()
		tr := NewCommandTracker()
		var recorded bytes.Buffer
		for _, chunk := range chunks {
			tr.Observe(chunk)
			recorded.Write(chunk)
		}
		if !bytes.Equal(recorded.Bytes(), stream) {
			t.Fatal("recorded stream differs from shell output")
		}
		if state := tr.State(); state.Sequence != 1 || state.Running || state.LastExitCode != 1 {
			t.Fatalf("State() = %+v; want idle, sequence 1, exit 1", state)
		}
	}

	t.Run("single chunk", func(t *testing.T) { feed(t, [][]byte{stream}) })
	t.Run("byte by byte", func(t *testing.T) {
		var chunks [][]byte
		for _, b := range stream {
			chunks = append(chunks, []byte{b})
		}
		feed(t, chunks)
	})
	t.Run("fixed chunks", func(t *testing.T) {
		rest := stream
		var chunks [][]byte
		for len(rest) > 0 {
			n := min(7, len(rest))
			chunks = append(chunks, rest[:n])
			rest = rest[n:]
		}
		feed(t, chunks)
	})
}

func TestCommandTrackerWorksWithoutInjection(t *testing.T) {
	// A remote shell that emits OSC 133 on its own, interleaved with output
	// and OSC 7, is tracked with no injected configuration.
	stream := []byte("\x1b]7;file://h/home/u\x1b\\$ ls\r\n")
	stream = append(stream, osc133("A")...)
	stream = append(stream, osc133("B")...)
	stream = append(stream, []byte("file1\r\n")...)
	stream = append(stream, osc133("D;0")...)
	stream = append(stream, osc133("A")...)
	tr := NewCommandTracker()
	tr.Observe(stream)
	state := tr.State()
	if state.Running || state.Sequence != 1 || !state.HasLastExitCode || state.LastExitCode != 0 {
		t.Fatalf("State() = %+v; want idle, sequence 1, exit 0", state)
	}
}

func TestParseOSC133EdgeCases(t *testing.T) {
	for _, payload := range []string{
		"133",
		"133;",
		"133;too-long",
		"7;file://h/tmp",
		"1333;A",
	} {
		if _, _, _, ok := parseOSC133([]byte(payload)); ok {
			t.Errorf("parseOSC133(%q) ok = true; want false", payload)
		}
	}
	if letter, exit, hasExit, ok := parseOSC133([]byte("133;D;42")); !ok || letter != 'D' || !hasExit || exit != 42 {
		t.Errorf("parseOSC133(133;D;42) = %c, %d, %v, %v; want D, 42, true, true", letter, exit, hasExit, ok)
	}
	if letter, _, hasExit, ok := parseOSC133([]byte("133;A")); !ok || letter != 'A' || hasExit {
		t.Errorf("parseOSC133(133;A) = %c, _, %v, %v; want A, _, false, true", letter, hasExit, ok)
	}
}

func TestCommandTrackerObserveReports(t *testing.T) {
	tr := NewCommandTracker()
	stream := append(append([]byte("prompt$ "), osc133("B")...), osc133("B")...)
	stream = append(stream, osc133("D;3")...)
	stream = append(stream, osc133("A")...)
	reports, state, changed := tr.ObserveReports(stream)
	if !changed {
		t.Fatal("ObserveReports() reported no change")
	}
	if len(reports) != 4 {
		t.Fatalf("reports = %+v, want B, deduped B, D;3, A", reports)
	}
	if reports[0].Letter != 'B' || !reports[0].Started || reports[0].Sequence != 1 {
		t.Fatalf("first report = %+v, want started B at sequence 1", reports[0])
	}
	if reports[1].Letter != 'B' || reports[1].Started || reports[1].Sequence != 1 {
		t.Fatalf("second report = %+v, want deduped B at sequence 1", reports[1])
	}
	if reports[2].Letter != 'D' || !reports[2].HasExitCode || reports[2].ExitCode != 3 || reports[2].Sequence != 1 {
		t.Fatalf("third report = %+v, want D exit 3 at sequence 1", reports[2])
	}
	if reports[3].Letter != 'A' || reports[3].HasExitCode || reports[3].Started {
		t.Fatalf("fourth report = %+v, want bare A", reports[3])
	}
	if state.Sequence != 1 || state.Running || state.LastExitCode != 3 {
		t.Fatalf("State() = %+v, want idle, sequence 1, exit 3", state)
	}
}

func TestCommandTrackerObserveReportsAcrossChunkBoundaries(t *testing.T) {
	stream := append(append([]byte("$ "), osc133("B")...), osc133("D;9")...)
	for split := 0; split <= len(stream); split++ {
		tr := NewCommandTracker()
		var reports []Report
		for _, chunk := range [][]byte{stream[:split], stream[split:]} {
			var chunkReports []Report
			chunkReports, _, _ = tr.ObserveReports(chunk)
			reports = append(reports, chunkReports...)
		}
		if len(reports) != 2 || reports[0].Letter != 'B' || !reports[0].Started || reports[1].Letter != 'D' || reports[1].ExitCode != 9 {
			t.Fatalf("split %d: reports = %+v, want started B and D;9", split, reports)
		}
	}
}

func TestCommandTrackerObserveReportsIgnoresNoise(t *testing.T) {
	tr := NewCommandTracker()
	reports, _, changed := tr.ObserveReports([]byte("\x1b[2J\x1b]0;title\x07 \x1b]7;file://h/tmp\x1b\\ \x1b]52;c;Y2xpcGJvYXJk\x07"))
	if changed || len(reports) != 0 {
		t.Fatalf("foreign OSC produced %+v (changed=%v), want no reports", reports, changed)
	}
	// An unknown letter and a malformed exit code still parse as OSC 133
	// reports; the state machine ignores the former and treats the latter as
	// a finish without an exit code, so consumers see them verbatim.
	reports, _, _ = tr.ObserveReports(append(osc133("X"), osc133("D;abc")...))
	if len(reports) != 2 || reports[0].Letter != 'X' || reports[0].Started {
		t.Fatalf("unknown-letter report = %+v, want bare X", reports)
	}
	if reports[1].Letter != 'D' || reports[1].HasExitCode || reports[1].ExitCode != 0 {
		t.Fatalf("malformed-exit report = %+v, want D without exit code", reports[1])
	}
}
