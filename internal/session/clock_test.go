package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type sessionTestClock struct {
	mu      sync.Mutex
	current time.Time
}

func newSessionTestClock() *sessionTestClock {
	return &sessionTestClock{current: time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)}
}

func (c *sessionTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *sessionTestClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.current = c.current.Add(duration)
	c.mu.Unlock()
}

func TestInjectedClockControlsSessionIdleSweep(t *testing.T) {
	clock := newSessionTestClock()
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(), Now: clock.Now, IdleTimeout: time.Minute,
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "clock-idle", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if got := connected.Info().CreatedAt; !got.Equal(clock.Now()) {
		t.Fatalf("CreatedAt = %v, want injected time %v", got, clock.Now())
	}
	clock.Advance(30 * time.Second)
	if err := manager.SweepIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := connected.Info().Status; got != StatusConnected {
		t.Fatalf("status before idle deadline = %s, want connected", got)
	}
	clock.Advance(30 * time.Second)
	if err := manager.SweepIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := connected.Info().Status; got != StatusDisconnected {
		t.Fatalf("status at idle deadline = %s, want disconnected", got)
	}
}

func TestInjectedClockControlsRemoteActivity(t *testing.T) {
	clock := newSessionTestClock()
	manager := newRemoteManager(t, func(config *Config) {
		config.Now = clock.Now
		config.IdleTimeout = time.Minute
	})
	info := registerRemote(t, manager, "clock-owner")
	feed, err := manager.AttachRemoteFeed(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	if err := manager.SweepIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(info.ID); err != nil {
		t.Fatalf("active remote feed was reaped: %v", err)
	}
	manager.DetachRemoteFeed(feed)
	clock.Advance(59 * time.Second)
	if err := manager.SweepIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(info.ID); err != nil {
		t.Fatalf("remote session reaped before detached idle deadline: %v", err)
	}
	clock.Advance(time.Second)
	if err := manager.SweepIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(info.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("remote session at detached idle deadline = %v, want ErrTabNotFound", err)
	}
}

func TestInjectedClockControlsGuestLease(t *testing.T) {
	clock := newSessionTestClock()
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), Now: clock.Now})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "clock-guest", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openGuestArbitrationTab(t, manager, connected)
	if _, err := manager.AttachSpectator(context.Background(), tab.ID, SpectateOptions{ClientID: "preview-a", ChannelID: "clock-spec-a"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.WriteGuest(context.Background(), tab.ID, "preview-a", []byte("a"), time.Hour); err != nil {
		t.Fatal(err)
	}
	_, until := guestLeaseState(tab)
	if want := clock.Now().Add(time.Hour); !until.Equal(want) {
		t.Fatalf("guest lease deadline = %v, want %v", until, want)
	}
	if err := manager.DetachChannel("clock-spec-a"); err != nil {
		t.Fatal(err)
	}
	if err := manager.WriteGuest(context.Background(), tab.ID, "preview-b", []byte("b"), time.Hour); !errors.Is(err, ErrGuestControlBusy) {
		t.Fatalf("write during guest lease = %v, want ErrGuestControlBusy", err)
	}
	clock.Advance(time.Hour)
	if err := manager.WriteGuest(context.Background(), tab.ID, "preview-b", []byte("b"), time.Hour); err != nil {
		t.Fatalf("write at guest lease expiry: %v", err)
	}
}
