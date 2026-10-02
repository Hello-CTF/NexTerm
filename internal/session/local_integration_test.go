//go:build darwin || linux

package session

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/local"
)

func TestRealLocalPTYAttachDetachReplayAndReap(t *testing.T) {
	connector := ConnectorFunc(func(_ context.Context, _ Asset, _ uint64) (base.Transport, error) {
		return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
	})
	manager := NewManager(Config{Connector: connector})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "real-local", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	if err := manager.Write(context.Background(), tab.ID, "client-a", []byte("printf 'nexterm-local-ok\\n'\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(tab.terminal.Dump(1<<20), []byte("nexterm-local-ok")) })
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1", ReplayBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	receiver := bindTestReceiver(t, manager, "b-1")
	assertFrameData(t, receiver, replayClear)
	deadline := time.Now().Add(3 * time.Second)
	for {
		frame := receiveTestFrame(t, receiver)
		if bytes.Contains(frame.Data, []byte("nexterm-local-ok")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("real local replay did not contain shell output")
		}
	}
	if _, err := manager.Claim(tab.ID, "client-b"); err != nil {
		t.Fatal(err)
	}
	if err := manager.DetachClient(tab.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Write(context.Background(), tab.ID, "client-b", []byte("printf 'nexterm-after-detach\\n'\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(tab.terminal.Dump(1<<20), []byte("nexterm-after-detach")) })
	if err := manager.Disconnect(session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Session(session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("real local session survived disconnect: %v", err)
	}
}
