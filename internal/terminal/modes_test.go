package terminal

import "testing"

func TestModeTrackerBracketedPasteIncludingSplit(t *testing.T) {
	m := NewModeTracker()
	m.Feed([]byte("\x1b[?2004hhello"))
	if !m.BracketedPaste() {
		t.Fatal("bracketed paste should be on")
	}
	m.Feed([]byte("\x1b[?2004lworld"))
	if m.BracketedPaste() {
		t.Fatal("bracketed paste should be off")
	}

	seq := []byte("\x1b[?2004h")
	for split := 1; split < len(seq); split++ {
		m := NewModeTracker()
		m.Feed(seq[:split])
		m.Feed(seq[split:])
		if !m.BracketedPaste() {
			t.Fatalf("split=%d: bracketed paste not detected", split)
		}
	}
}

func TestModeTrackerCursorAndApplicationModes(t *testing.T) {
	m := NewModeTracker()
	if !m.CursorVisible() || m.ApplicationCursor() {
		t.Fatal("bad power-on defaults")
	}
	m.Feed([]byte("\x1b[?25l"))
	if m.CursorVisible() {
		t.Fatal("?25l should hide the cursor")
	}
	m.Feed([]byte("\x1b[?1h"))
	if !m.ApplicationCursor() {
		t.Fatal("?1h should enable application cursor")
	}
	m.Feed([]byte("\x1b[?1;25h"))
	if !m.ApplicationCursor() || !m.CursorVisible() {
		t.Fatal("combined DECSET should set both")
	}
	m.Feed([]byte("\x1b[?1l"))
	if m.ApplicationCursor() {
		t.Fatal("?1l should reset application cursor")
	}
}

func TestModeTrackerIgnoresNonPrivateAndStrings(t *testing.T) {
	m := NewModeTracker()
	m.Feed([]byte("\x1b[25l"))
	m.Feed([]byte("\x1b[>2004h"))
	m.Feed([]byte("\x1b]0;title[?2004h\x07"))
	m.Feed([]byte("\x1bP1;2z[?25l\x1b\\"))
	if !m.CursorVisible() || m.BracketedPaste() {
		t.Fatalf("false mode trigger: visible=%v paste=%v", m.CursorVisible(), m.BracketedPaste())
	}
	m.Feed([]byte("\x1b[?2004h"))
	if !m.BracketedPaste() {
		t.Fatal("real DECSET after strings should still work")
	}
}

func TestModeTrackerRISAndAbort(t *testing.T) {
	m := NewModeTracker()
	m.Feed([]byte("\x1b[?25l\x1b[?2004h\x1b[?1h"))
	m.Feed([]byte("\x1bc"))
	if !m.CursorVisible() || m.BracketedPaste() || m.ApplicationCursor() {
		t.Fatal("RIS should reset modes")
	}
	m.Feed([]byte("\x1b[?200"))
	m.Feed([]byte{0x18})
	m.Feed([]byte("4h"))
	if m.BracketedPaste() {
		t.Fatal("aborted sequence must not set modes")
	}
}
