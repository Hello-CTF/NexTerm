package terminal

import "testing"

func TestPromptMarksFollowTheShellPrompt(t *testing.T) {
	e := NewEmulator(40, 4)
	e.Write([]byte("old output\r\n\x1b]133;A\x1b\\┌──(u㉿k)-[~]\r\n└─$ \x1b]133;B\x07brew"))
	snap := e.Snapshot()
	if snap.PromptStart != (ScreenMark{Row: 1, Col: 0, Set: true}) {
		t.Fatalf("prompt start = %+v", snap.PromptStart)
	}
	if snap.InputStart != (ScreenMark{Row: 2, Col: 4, Set: true}) {
		t.Fatalf("input start = %+v", snap.InputStart)
	}

	// Scrolling moves the marks with their rows.
	e.Write([]byte("\r\n\r\n"))
	snap = e.Snapshot()
	if snap.PromptStart != (ScreenMark{Row: 0, Col: 0, Set: true}) || snap.InputStart != (ScreenMark{Row: 1, Col: 4, Set: true}) {
		t.Fatalf("after scroll: prompt %+v input %+v", snap.PromptStart, snap.InputStart)
	}

	// A scrolled-off row takes its mark with it.
	e.Write([]byte("\r\n"))
	if snap = e.Snapshot(); snap.PromptStart.Set {
		t.Fatalf("scrolled-off prompt mark = %+v", snap.PromptStart)
	}

	// Command output starts: the input line is over.
	e.Write([]byte("\x1b]133;C\x1b\\"))
	if snap = e.Snapshot(); snap.PromptStart.Set || snap.InputStart.Set {
		t.Fatalf("marks after C: prompt %+v input %+v", snap.PromptStart, snap.InputStart)
	}
}

func TestPromptMarksIgnoreTheAlternateScreenAndClear(t *testing.T) {
	e := NewEmulator(40, 4)
	e.Write([]byte("\x1b]133;A\x1b\\$ \x1b]133;B\x1b\\"))
	e.Write([]byte("\x1b[?1049h"))
	if snap := e.Snapshot(); snap.InputStart.Set {
		t.Fatalf("alternate screen input mark = %+v", snap.InputStart)
	}
	e.Write([]byte("\x1b[?1049l"))
	if snap := e.Snapshot(); !snap.InputStart.Set {
		t.Fatal("main screen mark lost after leaving the alternate screen")
	}
	e.Write([]byte("\x1b[2J"))
	if snap := e.Snapshot(); snap.InputStart.Set || snap.PromptStart.Set {
		t.Fatalf("marks after erase: %+v", snap)
	}
	e.Write([]byte("\x1b]133;Bx\x1b\\"))
	if snap := e.Snapshot(); snap.InputStart.Set {
		t.Fatalf("malformed mark accepted: %+v", snap.InputStart)
	}
}
