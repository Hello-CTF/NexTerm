package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

// WritePasteSubmitFor writes the paste and, after the submit delay, the Enter
// key under one and the same write lock: the bytes land in paste-then-Enter
// order with nothing in between.
func TestWritePasteSubmitForPastesThenSubmitsUnderOneLock(t *testing.T) {
	manager, connector, terminals, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	term := terminals.terminal(info.ID)

	term.setBracketedPaste(true)
	if err := manager.WritePasteSubmitFor(context.Background(), info.ID, "echo hi", true, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if got, want := string(channel.written()), "\x1b[200~echo hi\x1b[201~\r"; got != want {
		t.Fatalf("written = %q, want %q", got, want)
	}
}

// submit=false writes only the paste; no Enter follows.
func TestWritePasteSubmitForWithoutSubmitWritesPasteOnly(t *testing.T) {
	manager, connector, terminals, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	term := terminals.terminal(info.ID)

	term.setBracketedPaste(true)
	if err := manager.WritePasteSubmitFor(context.Background(), info.ID, "echo hi", false, 0); err != nil {
		t.Fatal(err)
	}
	if got, want := string(channel.written()), "\x1b[200~echo hi\x1b[201~"; got != want {
		t.Fatalf("written = %q, want %q", got, want)
	}
}

// The literal-newline guard holds inside the submit lock: with bracketed
// paste off, multi-line content is refused before any byte is written.
func TestWritePasteSubmitForRefusesLineBreaksWhenUnbracketed(t *testing.T) {
	manager, connector, terminals, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	term := terminals.terminal(info.ID)

	term.setBracketedPaste(false)
	err = manager.WritePasteSubmitFor(context.Background(), info.ID, "line1\nline2", true, 0)
	if !errors.Is(err, ErrPasteGuarded) {
		t.Fatalf("paste submit = %v, want ErrPasteGuarded", err)
	}
	if got := channel.written(); len(got) != 0 {
		t.Fatalf("guard wrote %q, want zero bytes", got)
	}
}

// waitForWritten polls the channel's written bytes until predicate holds or
// the deadline expires; it keeps paste-submit interleavings deterministic
// without wall-clock sleeps.
func waitForWritten(t *testing.T, channel *fakeChannel, predicate func([]byte) bool) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		written := channel.written()
		if predicate(written) {
			return written
		}
		if time.Now().After(deadline) {
			t.Fatalf("written bytes never satisfied the predicate: %q", written)
		}
		time.Sleep(time.Millisecond)
	}
}

// A user Write issued while the submit delay is still pending must not
// interleave between the paste and the Enter: it lands after the Enter
// because the whole paste+delay+Enter sequence holds the write lock.
func TestWritePasteSubmitForHoldsLockAcrossDelay(t *testing.T) {
	manager, connector, terminals, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	term := terminals.terminal(info.ID)

	term.setBracketedPaste(true)
	submitDone := make(chan error, 1)
	go func() {
		submitDone <- manager.WritePasteSubmitFor(context.Background(), info.ID, "echo hi", true, 150*time.Millisecond)
	}()
	// Wait for the paste to land (delay running), then race a user Write.
	paste := "\x1b[200~echo hi\x1b[201~"
	waitForWritten(t, channel, func(written []byte) bool { return len(written) >= len(paste) })
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- manager.WriteInternal(context.Background(), info.ID, []byte("user"))
	}()
	select {
	case err := <-submitDone:
		if err != nil {
			t.Fatalf("paste submit = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("paste submit never finished")
	}
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("user Write = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("user Write never finished")
	}
	if got, want := string(channel.written()), paste+"\ruser"; got != want {
		t.Fatalf("written = %q, want %q", got, want)
	}
}

// Cancelling the context during the submit delay stops the sequence before
// the Enter: the error reports the partial state instead of silently
// submitting.
func TestWritePasteSubmitForCancelDuringDelaySkipsEnter(t *testing.T) {
	manager, connector, terminals, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	term := terminals.terminal(info.ID)

	term.setBracketedPaste(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.WritePasteSubmitFor(ctx, info.ID, "echo hi", true, time.Hour)
	}()
	// Wait for the paste to land (delay running), then cancel.
	paste := "\x1b[200~echo hi\x1b[201~"
	waitForWritten(t, channel, func(written []byte) bool { return len(written) >= len(paste) })
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("paste submit = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("paste submit never observed the cancellation")
	}
	if got := string(channel.written()); got != paste {
		t.Fatalf("written = %q, want paste without Enter %q", got, paste)
	}
}
