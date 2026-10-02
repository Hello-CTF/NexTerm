//go:build darwin || linux

package session

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/hub"
	"github.com/ProbiusOfficial/NexTerm/internal/terminalgrid"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/local"
)

func TestRealLocalPTYGridRuntimeResizeAndHubClients(t *testing.T) {
	events := make(chan ControlEvent, 16)
	connector := ConnectorFunc(func(_ context.Context, _ Asset, _ uint64) (base.Transport, error) {
		return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
	})
	manager := NewManager(Config{
		Connector: connector,
		Emitter: EmitterFunc(func(_ context.Context, event Event) error {
			if event.Topic == TopicTerminalControl {
				events <- event.Payload.(ControlEvent)
			}
			return nil
		}),
	})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "real-grid-local", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1", ReplayBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	a := bindTestReceiver(t, manager, "a-1")
	b := bindTestReceiver(t, manager, "b-1")
	if err := manager.Resize(context.Background(), tab.ID, "client-a", 100, 30); err != nil {
		t.Fatal(err)
	}
	var gridEvent ControlEvent
	for gridEvent.GridRevision == 0 {
		select {
		case gridEvent = <-events:
		case <-time.After(time.Second):
			t.Fatal("committed grid event was not emitted")
		}
	}
	if gridEvent.Cols != 100 || gridEvent.Rows != 30 || gridEvent.Subscribers != 2 || gridEvent.Viewers != 2 {
		t.Fatalf("real local grid event = %+v", gridEvent)
	}
	if err := manager.Write(context.Background(), tab.ID, "client-a", []byte("stty size; printf 'nexterm-grid-done\\n'\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		dump := tab.terminal.Dump(1 << 20)
		return bytes.Contains(dump, []byte("30 100")) && bytes.Contains(dump, []byte("nexterm-grid-done"))
	})
	for channelID, receiver := range map[string]*hub.Receiver{"a-1": a, "b-1": b} {
		output := readLocalGridOutput(t, receiver)
		if !bytes.Contains(output, []byte("30 100")) || !bytes.Contains(output, []byte("nexterm-grid-done")) {
			t.Fatalf("%s Hub output missing resized shell result: %q", channelID, output)
		}
	}
	terminalSnapshot, err := manager.TerminalSnapshot(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if terminalSnapshot.Cols != 100 || terminalSnapshot.Rows != 30 {
		t.Fatalf("real terminal snapshot = %+v", terminalSnapshot)
	}
	gridSnapshot, err := manager.GridSnapshot(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gridSnapshot.CommittedGrid != (terminalgrid.Grid{Cols: 100, Rows: 30}) {
		t.Fatalf("real grid snapshot = %+v", gridSnapshot)
	}
	if stats := manager.Hub().Stats(); stats.LiveChannels != 2 {
		t.Fatalf("Hub live channels = %+v", stats)
	}
}

func readLocalGridOutput(t *testing.T, receiver *hub.Receiver) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output []byte
	var previous uint64
	for {
		frame, err := receiver.Next(ctx)
		if err != nil {
			t.Fatalf("Hub receive failed before shell completion: %v (output %q)", err, output)
		}
		if frame.Sequence <= previous {
			t.Fatalf("Hub sequence regressed from %d to %d", previous, frame.Sequence)
		}
		previous = frame.Sequence
		output = append(output, frame.Data...)
		if bytes.Contains(output, []byte("30 100")) && bytes.Contains(output, []byte("nexterm-grid-done")) {
			return output
		}
	}
}
