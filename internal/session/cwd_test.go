package session

import (
	"context"
	"testing"
	"time"
)

func TestTabTracksReportedCWD(t *testing.T) {
	connector := newFakeConnector()
	events := make(chan ControlEvent, 8)
	manager := NewManager(Config{
		Connector: connector,
		Terminals: newFakeTerminalFactory(),
		Emitter: EmitterFunc(func(_ context.Context, event Event) error {
			if event.Topic == TopicTerminalControl {
				if control, ok := event.Payload.(ControlEvent); ok && control.Cwd != "" {
					events <- control
				}
			}
			return nil
		}),
	})
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "cwd-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "cwd-channel", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	if err := channel.emit([]byte("\x1b]7;file://buildhost/tmp/some%20dir\x1b\\")); err != nil {
		t.Fatal(err)
	}
	select {
	case control := <-events:
		if control.TabID != info.ID || control.Cwd != "/tmp/some dir" {
			t.Fatalf("control event = %+v", control)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no control event with cwd after an OSC 7 report")
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := tab.Info().Cwd; got != "/tmp/some dir" {
		t.Fatalf("TabInfo.Cwd = %q; want /tmp/some dir", got)
	}
	if err := channel.emit([]byte("plain output without reports")); err != nil {
		t.Fatal(err)
	}
	select {
	case control := <-events:
		t.Fatalf("unexpected control event for report-free output: %+v", control)
	case <-time.After(100 * time.Millisecond):
	}
}
