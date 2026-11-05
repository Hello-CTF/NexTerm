//go:build unix

package supervisor

import (
	"context"
	"testing"
	"time"
)

func waitPushedSize(t *testing.T, stream *Stream, cols, rows uint32) {
	t.Helper()
	deadline := time.After(testReadTimeout)
	for {
		if gotCols, gotRows, ok := stream.PushedSize(); ok && gotCols == cols && gotRows == rows {
			return
		}
		select {
		case <-stream.SizeUpdates():
		case <-deadline:
			gotCols, gotRows, ok := stream.PushedSize()
			t.Fatalf("pushed size = %dx%d (%t), want %dx%d", gotCols, gotRows, ok, cols, rows)
		}
	}
}

// Two desktops attached to one session through their own connections each
// learn the size the other gave the PTY, so neither keeps drawing the output
// at its own size. The client that resized is not told, and a connection
// whose hello did not ask for size pushes never receives the new frame.
func TestSizePushReachesOtherAttachedClients(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	session := testScriptSession(t, supervisor, `sleep 30`)
	large, err := testClient(t, server).Attach(context.Background(), session.ID(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = large.Close() }()
	small, err := testClient(t, server).Attach(context.Background(), session.ID(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = small.Close() }()
	legacy := rawHello(t, supervisor, server, false)
	rawAttachSession(t, legacy, attachMsg{ID: session.ID()})

	if err := large.Resize(context.Background(), 160, 50); err != nil {
		t.Fatal(err)
	}
	waitPushedSize(t, small, 160, 50)
	if _, _, ok := large.PushedSize(); ok {
		t.Fatal("the client that resized was told its own size")
	}

	if err := small.Resize(context.Background(), 90, 30); err != nil {
		t.Fatal(err)
	}
	waitPushedSize(t, large, 90, 30)

	payload, err := marshalFrame(frameGetVersions, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(legacy, frameGetVersions, payload); err != nil {
		t.Fatal(err)
	}
	for {
		kind, _, err := readFrame(legacy)
		if err != nil {
			t.Fatal(err)
		}
		if kind == frameSize {
			t.Fatal("a connection that did not ask for size pushes received one")
		}
		if kind == frameVersions || kind == frameError {
			break
		}
	}
}
