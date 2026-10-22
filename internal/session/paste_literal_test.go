package session

import (
	"context"
	"errors"
	"testing"
)

// The tools r7 counterexample: a precheck sees bracketed paste on, the mode
// flips off before the write lands, and an unguarded write encodes LF as CR,
// executing a multi-line paste line by line. The literal paste guard must
// refuse before any byte is written.
func TestWritePasteLiteralForRefusesLineBreaksAfterModeFlip(t *testing.T) {
	manager, connector, terminals, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	term := terminals.terminal(info.ID)

	term.setBracketedPaste(true)
	if on, _ := term.BracketedPasteMode(); !on {
		t.Fatal("precheck: bracketed paste should be on")
	}
	term.setBracketedPaste(false)

	err = manager.WritePasteLiteralFor(context.Background(), info.ID, "line1\nline2", false)
	if !errors.Is(err, ErrPasteGuarded) {
		t.Fatalf("literal paste = %v, want ErrPasteGuarded", err)
	}
	if got := channel.written(); len(got) != 0 {
		t.Fatalf("guard wrote %q, want zero bytes", got)
	}
}

func TestWritePasteLiteralForWritesBracketedLiteral(t *testing.T) {
	manager, connector, terminals, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	term := terminals.terminal(info.ID)

	term.setBracketedPaste(true)
	if err := manager.WritePasteLiteralFor(context.Background(), info.ID, "line1\nline2", false); err != nil {
		t.Fatal(err)
	}
	if got, want := string(channel.written()), "\x1b[200~line1\nline2\x1b[201~"; got != want {
		t.Fatalf("written = %q, want %q", got, want)
	}
}

func TestWritePasteLiteralForWritesSingleLineWithoutBracketedMode(t *testing.T) {
	manager, connector, terminals, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	term := terminals.terminal(info.ID)

	term.setBracketedPaste(false)
	if err := manager.WritePasteLiteralFor(context.Background(), info.ID, "echo hi", false); err != nil {
		t.Fatal(err)
	}
	if got, want := string(channel.written()), "echo hi"; got != want {
		t.Fatalf("written = %q, want %q", got, want)
	}
}

func TestWritePasteLiteralForForceWritesDespiteMode(t *testing.T) {
	manager, connector, terminals, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	term := terminals.terminal(info.ID)

	term.setBracketedPaste(false)
	if err := manager.WritePasteLiteralFor(context.Background(), info.ID, "line1\nline2", true); err != nil {
		t.Fatal(err)
	}
	if got, want := string(channel.written()), "line1\rline2"; got != want {
		t.Fatalf("written = %q, want %q", got, want)
	}
}

// Interactive user paste keeps its existing behavior: the current mode's
// encoding is written as-is, line breaks included.
func TestWritePasteForKeepsInteractiveEncoding(t *testing.T) {
	manager, connector, terminals, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	term := terminals.terminal(info.ID)

	term.setBracketedPaste(false)
	if err := manager.WritePasteFor(context.Background(), info.ID, "line1\nline2"); err != nil {
		t.Fatal(err)
	}
	if got, want := string(channel.written()), "line1\rline2"; got != want {
		t.Fatalf("written = %q, want %q", got, want)
	}
}
