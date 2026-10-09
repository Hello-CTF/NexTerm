package session

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type transportProvider interface {
	Transport(context.Context, string) (base.Transport, error)
}

var _ transportProvider = (*Manager)(nil)

func TestTransportResolvesReplacementAcrossGenerations(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, ReconnectBackoff: []time.Duration{0}})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := manager.Transport(context.Background(), session.ID)
	if err != nil || initial != connector.transport(0) {
		t.Fatalf("initial transport = %v, err = %v", initial, err)
	}
	if err := manager.Disconnect(session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Transport(context.Background(), session.ID); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("disconnected transport error = %v", err)
	}
	if err := manager.Reconnect(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	replacement, err := manager.Transport(context.Background(), session.ID)
	if err != nil || replacement != connector.transport(1) || replacement == connector.transport(0) {
		t.Fatalf("replacement transport = %v, err = %v", replacement, err)
	}
}

func TestInjectInternalFeedsVTAndSubscribersWithoutTransportWrite(t *testing.T) {
	manager, connector, terminals, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	var preempts atomic.Int32
	manager.SetUserInputHook(func(string) { preempts.Add(1) })
	receiver := bindTestReceiver(t, manager, "a-1")
	banner := []byte("\r\n[AI enter]\r\n")
	if err := manager.InjectInternal(context.Background(), tab.ID, banner); err != nil {
		t.Fatal(err)
	}
	assertFrameData(t, receiver, banner)
	if got := channel.written(); len(got) != 0 {
		t.Fatalf("injection wrote to transport: %q", got)
	}
	if got := terminals.terminal(tab.ID).bytes(); !bytes.Contains(got, banner) {
		t.Fatalf("injection missing from VT scrollback: %q", got)
	}
	if got := preempts.Load(); got != 0 {
		t.Fatalf("injection invoked user preemption %d times", got)
	}
	if err := manager.DetachAll(tab.ID); err != nil {
		t.Fatal(err)
	}
	detachedBanner := []byte("[AI exit while detached]")
	if err := manager.InjectInternal(context.Background(), tab.ID, detachedBanner); err != nil {
		t.Fatal(err)
	}
	if got := terminals.terminal(tab.ID).bytes(); !bytes.Contains(got, detachedBanner) {
		t.Fatalf("detached injection missing from scrollback: %q", got)
	}
}

func TestUserInputHookRunsBeforeWriteMuAndWhenCanceled(t *testing.T) {
	manager, connector, _, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	var calls atomic.Int32
	var lockFailures atomic.Int32
	manager.SetUserInputHook(func(tabID string) {
		calls.Add(1)
		if tabID != tab.ID {
			t.Errorf("hook tab = %q, want %q", tabID, tab.ID)
		}
		if !tab.writeMu.TryLock() {
			lockFailures.Add(1)
			return
		}
		tab.writeMu.Unlock()
	})
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.Write(canceled, tab.ID, "client-a", []byte("canceled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write error = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("canceled input skipped preemption hook: %d calls", got)
	}
	if got := channel.written(); len(got) != 0 {
		t.Fatalf("canceled input reached transport: %q", got)
	}
	if err := manager.Write(context.Background(), tab.ID, "client-a", []byte("ok")); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("user input hook calls = %d, want 2", got)
	}
	if got := lockFailures.Load(); got != 0 {
		t.Fatalf("hook observed writeMu held %d times", got)
	}
	manager.SetUserInputHook(nil)
	if err := manager.WriteInternal(context.Background(), tab.ID, []byte("ai")); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("internal write invoked user hook: %d calls", got)
	}
}

func TestTakeoverLockOrderRaceDoesNotHoldWriteMuDuringHook(t *testing.T) {
	manager, _, _, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	var takeover sync.Mutex
	started := make(chan struct{})
	manager.SetUserInputHook(func(string) {
		close(started)
		takeover.Lock()
		takeover.Unlock()
	})
	takeover.Lock()
	locked := true
	defer func() {
		if locked {
			takeover.Unlock()
		}
	}()
	userDone := make(chan error, 1)
	go func() { userDone <- manager.Write(context.Background(), tab.ID, "client-a", []byte("user")) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("user hook did not start")
	}
	if !tab.writeMu.TryLock() {
		t.Error("user hook ran after acquiring writeMu")
	} else {
		tab.writeMu.Unlock()
	}
	aiDone := make(chan error, 1)
	go func() { aiDone <- manager.WriteInternal(context.Background(), tab.ID, []byte("ai")) }()
	takeover.Unlock()
	locked = false
	for name, done := range map[string]<-chan error{"user": userDone, "AI": aiDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s write: %v", name, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s write deadlocked with takeover lock", name)
		}
	}
}
