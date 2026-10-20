package tools

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// The tools session adapter must never elevate an identity-less caller to
// open access in server account mode: a gateway request without an identity
// keeps requester-less reads denied. Desktop (open manager config), the
// trusted open mark (owner.WithOpenAccess), and identified owners keep their
// existing behavior.

func gateFixture(t *testing.T) *atomicSessionFixture {
	t.Helper()
	return newAtomicSessionFixture(t, false)
}

func TestSessionAdapterGatewayIdentityLessDenied(t *testing.T) {
	fixture := gateFixture(t)
	adapter := SessionTerminal(fixture.manager)
	ctx := context.Background()

	if _, err := adapter.Snapshot(ctx, fixture.tabID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("snapshot err = %v", err)
	}
	if _, err := adapter.(WaitSnapshotTerminal).WaitSnapshot(ctx, fixture.tabID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("wait snapshot err = %v", err)
	}
	if err := adapter.Write(ctx, fixture.tabID, []byte("ls")); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("write err = %v", err)
	}
	if err := adapter.(PasteTerminal).WritePaste(ctx, fixture.tabID, "ls"); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("write paste err = %v", err)
	}
	if _, err := adapter.(CommandStateTerminal).CommandState(ctx, fixture.tabID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("command state err = %v", err)
	}
	if _, _, _, err := adapter.(OutputSinceTerminal).OutputSince(ctx, fixture.tabID, 0, 10); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("output since err = %v", err)
	}
	if _, err := adapter.(AssetKindTerminal).SessionAssetKind(ctx, fixture.sessionID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("session asset kind err = %v", err)
	}
	if got := fixture.channel.attemptCount(); got != 0 {
		t.Fatalf("identity-less write reached the channel %d times", got)
	}

	registry := fixture.registry()
	for _, call := range []Call{
		{ID: "r", Name: "read_screen"},
		{ID: "k", Name: "send_keys", Args: []byte(`{"keys":"ls"}`)},
		{ID: "w", Name: "wait_for", Args: []byte(`{"pattern":"x","timeout_ms":10}`)},
	} {
		if result := registry.Execute(ctx, "job", fixture.scope(), call, nil); result.OK {
			t.Fatalf("%s succeeded for identity-less gateway caller", call.Name)
		}
	}
}

func TestSessionAdapterTrustedMarkAllowed(t *testing.T) {
	fixture := gateFixture(t)
	adapter := SessionTerminal(fixture.manager)
	ctx := owner.WithOpenAccess(context.Background())

	if _, err := adapter.Snapshot(ctx, fixture.tabID); err != nil {
		t.Fatalf("snapshot err = %v", err)
	}
	if _, err := adapter.(WaitSnapshotTerminal).WaitSnapshot(ctx, fixture.tabID); err != nil {
		t.Fatalf("wait snapshot err = %v", err)
	}
	if _, err := adapter.(CommandStateTerminal).CommandState(ctx, fixture.tabID); err != nil {
		t.Fatalf("command state err = %v", err)
	}
	if _, _, _, err := adapter.(OutputSinceTerminal).OutputSince(ctx, fixture.tabID, 0, 10); err != nil {
		t.Fatalf("output since err = %v", err)
	}
	if kind, err := adapter.(AssetKindTerminal).SessionAssetKind(ctx, fixture.sessionID); err != nil || kind != session.KindSSH {
		t.Fatalf("session asset kind = %q err=%v", kind, err)
	}
	if err := adapter.Write(ctx, fixture.tabID, []byte("ls")); err != nil {
		t.Fatalf("write err = %v", err)
	}
	if got := fixture.channel.attemptCount(); got != 1 {
		t.Fatalf("trusted write attempts = %d, want 1", got)
	}

	registry := fixture.registry()
	if result := registry.Execute(ctx, "job", fixture.scope(), Call{ID: "r", Name: "read_screen"}, nil); !result.OK {
		t.Fatalf("read_screen for trusted caller = %+v", result)
	}
}

// newOpenGateManager builds a desktop-shaped manager: open access comes from
// the manager configuration itself, so identity-less callers work without
// any context elevation.
func newOpenGateManager(t *testing.T) (*session.Manager, string, string) {
	t.Helper()
	transport := newAtomicPTYTransport()
	manager := session.NewManager(session.Config{
		OpenAccess: true,
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return transport, nil
		}),
		Terminals: session.TerminalFactoryFunc(func(config session.TerminalConfig) (session.TerminalState, error) {
			return &hookTerminal{Tab: terminal.NewTab(config.TabID, config.SessionID, int(config.Cols), int(config.Rows), terminal.UTF8)}, nil
		}),
	})
	connected, err := manager.Connect(context.Background(), session.Asset{ID: "asset-open", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := manager.OpenTab(context.Background(), session.OpenTabOptions{
		SessionID: connected.ID, ClientID: "client", ChannelID: "channel", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = manager.Close()
		_ = transport.Close()
	})
	return manager, connected.ID, opened.ID
}

func TestSessionAdapterOpenManagerCompat(t *testing.T) {
	manager, sessionID, tabID := newOpenGateManager(t)
	adapter := SessionTerminal(manager)
	ctx := context.Background()

	screen, err := adapter.Snapshot(ctx, tabID)
	if err != nil {
		t.Fatalf("snapshot err = %v", err)
	}
	if screen.Cols != 80 {
		t.Fatalf("screen = %+v", screen)
	}
	if kind, err := adapter.(AssetKindTerminal).SessionAssetKind(ctx, sessionID); err != nil || kind != session.KindSSH {
		t.Fatalf("session asset kind = %q err=%v", kind, err)
	}
	registry := NewRegistry(WithSession(Dependencies{PollInterval: time.Millisecond}, manager))
	if result := registry.Execute(ctx, "job", Scope{SessionID: sessionID, TabID: tabID, AssetID: "asset-open"}, Call{ID: "r", Name: "read_screen"}, nil); !result.OK {
		t.Fatalf("open-manager read_screen = %+v", result)
	}
}

func TestSessionAdapterCrossAccountDenied(t *testing.T) {
	fixture := gateFixture(t)
	adapter := SessionTerminal(fixture.manager)
	ownerCtx := ipc.WithUserID(context.Background(), "u1")
	otherCtx := ipc.WithUserID(context.Background(), "u2")

	connected, err := fixture.manager.Connect(ownerCtx, session.Asset{ID: "asset-u1", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := fixture.manager.OpenTab(context.Background(), session.OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-u1", ChannelID: "channel-u1", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Another account never reads or writes u1's tab.
	if _, err := adapter.Snapshot(otherCtx, opened.ID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("cross-account snapshot err = %v", err)
	}
	if _, err := adapter.(CommandStateTerminal).CommandState(otherCtx, opened.ID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("cross-account command state err = %v", err)
	}
	if err := adapter.Write(otherCtx, opened.ID, []byte("ls")); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("cross-account write err = %v", err)
	}
	// The owner reads it.
	if _, err := adapter.Snapshot(ownerCtx, opened.ID); err != nil {
		t.Fatalf("owner snapshot err = %v", err)
	}
	// Identity-less callers never read u1's tab, and an ownerless session
	// denies every identified requester (fail closed both ways).
	if _, err := adapter.Snapshot(context.Background(), opened.ID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("identity-less snapshot err = %v", err)
	}
	if _, err := adapter.Snapshot(ownerCtx, fixture.tabID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("ownerless session snapshot err = %v", err)
	}
}

func TestSessionAdapterGateRace(t *testing.T) {
	fixture := gateFixture(t)
	adapter := SessionTerminal(fixture.manager)
	marked := owner.WithOpenAccess(context.Background())
	ownerCtx := ipc.WithUserID(context.Background(), "u1")
	connected, err := fixture.manager.Connect(ownerCtx, session.Asset{ID: "asset-race", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := fixture.manager.OpenTab(context.Background(), session.OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-race", ChannelID: "channel-race", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}

	const racers = 8
	begin := make(chan struct{})
	var wg sync.WaitGroup
	denied := make(chan error, racers*2)
	allowed := make(chan error, racers*3)
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-begin
			if _, err := adapter.Snapshot(context.Background(), opened.ID); err != nil {
				denied <- err
			}
			if _, err := adapter.Snapshot(context.Background(), fixture.tabID); err != nil {
				denied <- err
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-begin
			if _, err := adapter.Snapshot(marked, opened.ID); err != nil {
				allowed <- err
			}
			if _, err := adapter.Snapshot(marked, fixture.tabID); err != nil {
				allowed <- err
			}
			if _, err := adapter.Snapshot(ownerCtx, opened.ID); err != nil {
				allowed <- err
			}
		}()
	}
	close(begin)
	wg.Wait()
	close(denied)
	close(allowed)
	for err := range denied {
		if !errors.Is(err, session.ErrForbidden) {
			t.Fatalf("identity-less racer snapshot err = %v", err)
		}
	}
	for err := range allowed {
		t.Fatalf("trusted racer snapshot err = %v", err)
	}
}

func TestSessionRequestContextUnit(t *testing.T) {
	base := context.Background()
	if got := sessionRequestContext(base); got != base {
		t.Fatal("identity-less unmarked context must not be elevated")
	}
	marked := owner.WithOpenAccess(base)
	if got := sessionRequestContext(marked); got == marked {
		t.Fatal("trusted mark must elevate to open access")
	}
	identified := ipc.WithUserID(base, "u1")
	if got := sessionRequestContext(identified); got != identified {
		t.Fatal("identified context must pass through unchanged")
	}
}
