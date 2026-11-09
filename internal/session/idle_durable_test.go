package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type idleDurableCase struct {
	name     string
	provider base.DurableProvider
	wantKept bool
}

// A persistent terminal outlives its tabs, so an idle sweep keeps a session
// connected while its host still holds one; sessions without any are reaped.
func TestIdleSweepKeepsSessionsWithPersistentTerminals(t *testing.T) {
	withTerminal := newFakeDurableProvider()
	withTerminal.records["pt-1"] = &fakeDurableRecord{identity: 1}
	cases := []idleDurableCase{
		{name: "persistent-terminal-on-host", provider: withTerminal, wantKept: true},
		{name: "list-fails", provider: &failingListDurableProvider{fakeDurableProvider: newFakeDurableProvider(), err: errors.New("helper unreachable")}, wantKept: true},
		{name: "provider-without-terminals", provider: newFakeDurableProvider(), wantKept: false},
		{name: "no-provider", provider: nil, wantKept: false},
	}
	clock := newSessionTestClock()
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), Now: clock.Now, IdleTimeout: time.Minute,
	})
	t.Cleanup(func() { _ = manager.Close() })
	sessions := make(map[string]*Session, len(cases))
	for _, tc := range cases {
		session, err := manager.Connect(context.Background(), Asset{ID: tc.name, Kind: KindSSH})
		if err != nil {
			t.Fatal(err)
		}
		if tc.provider != nil {
			session.mu.Lock()
			generation, transport := session.generation, session.transport
			session.mu.Unlock()
			session.durableMu.Lock()
			session.durableProvider, session.durableGen, session.durableTransport = tc.provider, generation, transport
			session.durableMu.Unlock()
		}
		sessions[tc.name] = session
	}
	clock.Advance(2 * time.Minute)
	if err := manager.SweepIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		got := sessions[tc.name].Info().Status
		if kept := got == StatusConnected; kept != tc.wantKept {
			t.Errorf("%s: status = %s, kept = %v, want kept = %v", tc.name, got, kept, tc.wantKept)
		}
	}
}
