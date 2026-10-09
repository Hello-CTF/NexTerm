package session

import (
	"context"
	"errors"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func TestOpenTabRunsStartupCommandInTerminal(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory()})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "startup", Kind: KindSSH, StartupCommand: "echo ready && cd /tmp"})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	if got := string(connector.transport(0).channel(0).written()); got != "echo ready && cd /tmp\n" {
		t.Fatalf("startup command write = %q", got)
	}
	if info := tab.Info(); info.Exited {
		t.Fatal("startup command marked the tab as exited")
	}
	openTestTab(t, manager, session, "client-a", "a-2")
	if got := string(connector.transport(0).channel(1).written()); got != "echo ready && cd /tmp\n" {
		t.Fatalf("second tab startup command write = %q", got)
	}
}

func TestOpenTabStartupCommandSkippedForUnsupportedKinds(t *testing.T) {
	for _, kind := range []string{KindLocal, KindWinRM} {
		t.Run(kind, func(t *testing.T) {
			connector := newFakeConnector()
			manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory()})
			t.Cleanup(func() { _ = manager.Close() })
			session, err := manager.Connect(context.Background(), Asset{ID: "startup-" + kind, Kind: kind, StartupCommand: "echo hi"})
			if err != nil {
				t.Fatal(err)
			}
			openTestTab(t, manager, session, "client-a", "a-1")
			transport := connector.transport(0)
			for index := 0; index < transport.channelCount(); index++ {
				if got := transport.channel(index).written(); len(got) != 0 {
					t.Fatalf("startup command written for %s: %q", kind, got)
				}
			}
		})
	}
}

func TestOpenTabStartupCommandWriteFailureKeepsTabOpen(t *testing.T) {
	connector := newFakeConnector()
	connector.connect = func(_ context.Context, asset Asset, generation uint64, _ int) (base.Transport, error) {
		transport := newFakeTransport(asset.Kind, generation)
		transport.writeErr = errors.New("channel broken")
		return transport, nil
	}
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory()})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "startup-blocked", Kind: KindSSH, StartupCommand: "echo hi"})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	if info := tab.Info(); info.Exited {
		t.Fatal("failed startup command write marked the tab as exited")
	}
	if got := connector.transport(0).channel(0).written(); len(got) != 0 {
		t.Fatalf("startup command write = %q, want none", got)
	}
}

func TestTabInfoEncodingTracksAssetAndRuntimeSwitch(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "encoding", Kind: KindSSH, Encoding: "gbk"})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	if got := tab.Info().Encoding; got != "gbk" {
		t.Fatalf("tab encoding = %q, want gbk", got)
	}
	if err := manager.SwitchEncoding(tab.ID, "big5"); err != nil {
		t.Fatal(err)
	}
	if got := tab.Info().Encoding; got != "big5" {
		t.Fatalf("tab encoding after switch = %q, want big5", got)
	}

	plain, err := manager.Connect(context.Background(), Asset{ID: "encoding-default", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	plainTab := openTestTab(t, manager, plain, "client-a", "a-2")
	if got := plainTab.Info().Encoding; got != "utf-8" {
		t.Fatalf("default tab encoding = %q, want utf-8", got)
	}
}
