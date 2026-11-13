package session

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// Output is recorded only while a client watches the tab: output produced in
// the background is left out, marked once when recording resumes, and the
// durable offset still counts it.
func TestTranscriptRecordsOnlyWhileAClientWatches(t *testing.T) {
	provider := newTranscriptDurableProvider()
	recorder := &transcriptRecorder{}
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		Durable: provider, TranscriptOffsets: provider, Transcripts: recorder,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "asset-local", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "channel-a",
		Cols: 80, Rows: 24, Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	emit := func(data string) {
		t.Helper()
		if err := provider.channel(info.ID).emit([]byte(data)); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return bytes.Contains(tab.terminal.Dump(1<<20), []byte(data)) })
	}

	emit("watched-1\r\n")
	if err := manager.BackgroundTab(ctx, info.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	emit("background\r\n")
	if _, err := manager.AttachTab(ctx, info.ID, AttachOptions{ClientID: "client-a", ChannelID: "channel-b", ReplayBytes: -1}); err != nil {
		t.Fatal(err)
	}
	emit("watched-2\r\n")
	waitFor(t, func() bool { return bytes.Contains(recorder.outputBytes(), []byte("watched-2")) })

	if got := recorder.outputBytes(); !bytes.Equal(got, []byte("watched-1\r\nwatched-2\r\n")) {
		t.Fatalf("recorded %q, want only the watched output", got)
	}
	if got, want := recorder.unrecorded(), int64(len("background\r\n")); got != want {
		t.Fatalf("unrecorded = %d, want %d", got, want)
	}
	if got, want := provider.DurableTranscriptOffsetContext(ctx, info.ID), int64(len("watched-1\r\nbackground\r\nwatched-2\r\n")); got != want {
		t.Fatalf("durable offset = %d, want %d", got, want)
	}
}

// A Failed session whose durable terminals are all in the background is not
// retried on its own; the user's retry still reconnects it.
func TestFailedSessionDoesNotRetryForBackgroundTerminals(t *testing.T) {
	connector := newFakeConnector()
	var mu sync.Mutex
	offline := true
	attempts := 0
	connector.connect = func(_ context.Context, asset Asset, _ uint64, call int) (base.Transport, error) {
		mu.Lock()
		defer mu.Unlock()
		if call > 1 && offline {
			attempts++
			return nil, ipc.NewError(ipc.CodeDisconnected, "offline")
		}
		return newFakeTransport(asset.Kind, uint64(call)+1000), nil
	}
	manager, _, connected, info := openReproDurableTab(t, io.EOF, connector, 2)
	manager.failedRetryMax = 40 * time.Millisecond
	ctx := WithOpenAccess(context.Background())
	if err := manager.BackgroundTab(ctx, info.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	if err := manager.StartReconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, manager, connected.ID, StatusFailed)
	mu.Lock()
	atFailure := attempts
	mu.Unlock()
	// Several capped retry periods pass without a dial.
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	later := attempts
	offline = false
	mu.Unlock()
	if later != atFailure {
		t.Fatalf("%d dial attempts after Failed with only background terminals, want none", later-atFailure)
	}
	if _, err := manager.ProbeLivenessFor(ctx, connected.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	probed := attempts
	mu.Unlock()
	if probed != later {
		t.Fatal("an automatic probe reconnected a session nobody watches")
	}
	if _, err := manager.RetryReconnectNowFor(ctx, connected.ID); err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, manager, connected.ID, StatusConnected)
}
