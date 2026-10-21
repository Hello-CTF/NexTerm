package production

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type accountCleanupChannel struct {
	id         string
	generation uint64
	closed     chan struct{}
	closeOnce  sync.Once
	closeCount atomic.Int32
}

func newAccountCleanupChannel(generation uint64) *accountCleanupChannel {
	return &accountCleanupChannel{id: "account-cleanup", generation: generation, closed: make(chan struct{})}
}

func (c *accountCleanupChannel) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *accountCleanupChannel) Write(data []byte) (int, error) { return len(data), nil }

func (c *accountCleanupChannel) Close() error {
	c.closeOnce.Do(func() {
		c.closeCount.Add(1)
		close(c.closed)
	})
	return nil
}

func (c *accountCleanupChannel) Stderr() io.Reader                            { return nil }
func (c *accountCleanupChannel) Resize(context.Context, uint32, uint32) error { return nil }
func (c *accountCleanupChannel) Wait(ctx context.Context) error {
	select {
	case <-c.closed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *accountCleanupChannel) CloseWrite() error  { return nil }
func (c *accountCleanupChannel) ID() string         { return c.id }
func (c *accountCleanupChannel) Generation() uint64 { return c.generation }

type accountCleanupTransport struct {
	kind       string
	generation uint64
	alive      atomic.Bool
	closeCount atomic.Int32
	mu         sync.Mutex
	channels   []*accountCleanupChannel
	execHook   func(context.Context) (base.ExecResult, error)
}

func newAccountCleanupTransport(kind string, generation uint64) *accountCleanupTransport {
	transport := &accountCleanupTransport{kind: kind, generation: generation}
	transport.alive.Store(true)
	return transport
}

func (t *accountCleanupTransport) Kind() string       { return t.kind }
func (t *accountCleanupTransport) Generation() uint64 { return t.generation }
func (t *accountCleanupTransport) IsAlive() bool      { return t.alive.Load() }
func (t *accountCleanupTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}
func (t *accountCleanupTransport) Exec(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
	t.mu.Lock()
	hook := t.execHook
	t.mu.Unlock()
	if hook != nil {
		return hook(ctx)
	}
	return base.ExecResult{}, nil
}
func (t *accountCleanupTransport) Close() error {
	if !t.alive.Swap(false) {
		return nil
	}
	t.closeCount.Add(1)
	t.mu.Lock()
	channels := append([]*accountCleanupChannel(nil), t.channels...)
	t.mu.Unlock()
	for _, channel := range channels {
		_ = channel.Close()
	}
	return nil
}
func (t *accountCleanupTransport) OpenPTY(context.Context, base.PTYOptions) (base.Channel, error) {
	channel := newAccountCleanupChannel(t.generation)
	t.mu.Lock()
	t.channels = append(t.channels, channel)
	t.mu.Unlock()
	return channel, nil
}

type accountCleanupClock struct {
	now atomic.Int64
}

func newAccountCleanupClock() *accountCleanupClock {
	clock := &accountCleanupClock{}
	clock.now.Store(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC).UnixMilli())
	return clock
}

func (c *accountCleanupClock) Now() time.Time {
	return time.UnixMilli(c.now.Load())
}

func (c *accountCleanupClock) NowMS() int64 {
	return c.now.Load()
}

func (c *accountCleanupClock) Advance(duration time.Duration) {
	c.now.Add(duration.Milliseconds())
}

type accountCleanupRig struct {
	manager    *session.Manager
	mu         sync.Mutex
	transports map[string]*accountCleanupTransport
	execHooks  map[string]func(context.Context) (base.ExecResult, error)
}

func newAccountCleanupRig(t *testing.T, configure ...func(*session.Config)) *accountCleanupRig {
	t.Helper()
	rig := &accountCleanupRig{transports: make(map[string]*accountCleanupTransport), execHooks: make(map[string]func(context.Context) (base.ExecResult, error))}
	config := session.Config{
		Connector: session.ConnectorFunc(func(_ context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
			transport := newAccountCleanupTransport(asset.Kind, generation)
			rig.mu.Lock()
			transport.execHook = rig.execHooks[asset.ID]
			rig.transports[asset.ID] = transport
			rig.mu.Unlock()
			return transport, nil
		}),
		Terminals: session.TerminalFactoryFunc(func(config session.TerminalConfig) (session.TerminalState, error) {
			return terminal.NewTab(config.TabID, config.SessionID, int(config.Cols), int(config.Rows), terminal.UTF8), nil
		}),
	}
	for _, apply := range configure {
		apply(&config)
	}
	rig.manager = session.NewManager(config)
	t.Cleanup(func() { _ = rig.manager.Close() })
	return rig
}

// setExecHook installs (or replaces) the Exec behavior for an asset; call it
// before the asset connects.
func (r *accountCleanupRig) setExecHook(assetID string, hook func(context.Context) (base.ExecResult, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.execHooks[assetID] = hook
	if transport := r.transports[assetID]; transport != nil {
		transport.mu.Lock()
		transport.execHook = hook
		transport.mu.Unlock()
	}
}

func (r *accountCleanupRig) open(t *testing.T, ctx context.Context, asset session.Asset) (*session.Session, session.TabInfo) {
	t.Helper()
	connected, err := r.manager.ConnectFor(ctx, asset)
	if err != nil {
		t.Fatal(err)
	}
	tab, err := r.manager.OpenTabFor(ctx, session.OpenTabOptions{
		SessionID: connected.ID, ClientID: "account-cleanup", ChannelID: "channel-" + asset.ID,
		Cols: 80, Rows: 24, Ephemeral: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return connected, tab
}

func (r *accountCleanupRig) transport(assetID string) *accountCleanupTransport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.transports[assetID]
}

func requireAccountCleanupTab(t *testing.T, ctx context.Context, manager *session.Manager, tabID string, present bool) {
	t.Helper()
	_, err := manager.TabFor(ctx, tabID)
	if present && err != nil {
		t.Fatalf("tab %s should remain: %v", tabID, err)
	}
	if !present && !errors.Is(err, session.ErrTabNotFound) {
		t.Fatalf("tab %s should be cleaned, err = %v", tabID, err)
	}
}

func requireAccountCleanupStatus(t *testing.T, ctx context.Context, manager *session.Manager, sessionID string, want session.Status) {
	t.Helper()
	connected, err := manager.SessionFor(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := connected.Info().Status; got != want {
		t.Fatalf("session %s status = %s, want %s", sessionID, got, want)
	}
}

func TestProductionAccountCleanupDeviceSessionsIdempotentAndOwnerScoped(t *testing.T) {
	rig := newAccountCleanupRig(t)
	application := &Production{Services: ProductionServices{Sessions: rig.manager}}
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	ownerB := ipc.WithUserID(t.Context(), "owner-b")
	deviceA, tabA := rig.open(t, ownerA, session.Asset{ID: "device/a", Kind: session.KindDevice})
	_, tabB := rig.open(t, ownerB, session.Asset{ID: "device/b", Kind: session.KindDevice})
	sshA, tabSSH := rig.open(t, ownerA, session.Asset{ID: "asset-ssh", Kind: session.KindSSH})

	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			application.CleanupAccount(context.Background(), "owner-a", "login-a", "login-a")
		}()
	}
	close(start)
	wg.Wait()

	requireAccountCleanupTab(t, ownerA, rig.manager, tabA.ID, false)
	requireAccountCleanupStatus(t, ownerA, rig.manager, deviceA.ID, session.StatusDisconnected)
	requireAccountCleanupTab(t, ownerB, rig.manager, tabB.ID, true)
	requireAccountCleanupTab(t, ownerA, rig.manager, tabSSH.ID, true)
	requireAccountCleanupStatus(t, ownerA, rig.manager, sshA.ID, session.StatusConnected)
	if got := rig.transport("device/a").channels[0].closeCount.Load(); got != 1 {
		t.Fatalf("owner A terminal close count = %d, want 1", got)
	}
	if got := rig.transport("device/a").closeCount.Load(); got != 1 {
		t.Fatalf("owner A transport close count = %d, want 1", got)
	}
	if got := rig.transport("device/b").closeCount.Load(); got != 0 {
		t.Fatalf("owner B transport close count = %d, want 0", got)
	}
}

func TestProductionAccountCleanupPreparedSnapshotExcludesNewSessions(t *testing.T) {
	rig := newAccountCleanupRig(t)
	application := &Production{Services: ProductionServices{Sessions: rig.manager}}
	owner := ipc.WithUserID(t.Context(), "owner-a")
	oldSession, oldTab := rig.open(t, owner, session.Asset{ID: "device/old", Kind: session.KindDevice})
	commit, _, err := application.PrepareAccountCleanup(context.Background(), "owner-a", "logout-all:login-1")
	if err != nil {
		t.Fatal(err)
	}
	newSession, newTab := rig.open(t, owner, session.Asset{ID: "device/new", Kind: session.KindDevice})

	commit("login-1", "login-2")
	application.CleanupAccount(context.Background(), "owner-a", "login-2")
	requireAccountCleanupTab(t, owner, rig.manager, oldTab.ID, false)
	requireAccountCleanupStatus(t, owner, rig.manager, oldSession.ID, session.StatusDisconnected)
	requireAccountCleanupTab(t, owner, rig.manager, newTab.ID, true)
	requireAccountCleanupStatus(t, owner, rig.manager, newSession.ID, session.StatusConnected)

	application.CleanupAccount(context.Background(), "owner-a", "login-new")
	requireAccountCleanupTab(t, owner, rig.manager, newTab.ID, false)
	requireAccountCleanupStatus(t, owner, rig.manager, newSession.ID, session.StatusDisconnected)
}

func TestProductionAccountCleanupRollbackAllowsRetry(t *testing.T) {
	rig := newAccountCleanupRig(t)
	application := &Production{Services: ProductionServices{Sessions: rig.manager}}
	owner := ipc.WithUserID(t.Context(), "owner-a")
	connected, tab := rig.open(t, owner, session.Asset{ID: "device/retry", Kind: session.KindDevice})
	commit, rollback, err := application.PrepareAccountCleanup(context.Background(), "owner-a", "login-retry")
	if err != nil {
		t.Fatal(err)
	}
	rollback()
	commit("login-retry")
	requireAccountCleanupTab(t, owner, rig.manager, tab.ID, true)
	requireAccountCleanupStatus(t, owner, rig.manager, connected.ID, session.StatusConnected)

	application.CleanupAccount(context.Background(), "owner-a", "login-retry")
	requireAccountCleanupTab(t, owner, rig.manager, tab.ID, false)
	requireAccountCleanupStatus(t, owner, rig.manager, connected.ID, session.StatusDisconnected)
}

func TestProductionAccountCleanupSnapshotReadFailureRollsBackAndRetries(t *testing.T) {
	injected := errors.New("injected snapshot read failure")
	for _, test := range []struct {
		name   string
		inject func(state *accountCleanupState)
		clear  func(state *accountCleanupState)
	}{
		{
			name: "generation bound read",
			inject: func(state *accountCleanupState) {
				state.cutoffBound = func(context.Context, string, string) (*time.Time, error) {
					return nil, injected
				}
			},
			clear: func(state *accountCleanupState) { state.cutoffBound = nil },
		},
		{
			name: "owner device listing",
			inject: func(state *accountCleanupState) {
				state.listOwnerSessions = func(context.Context, string) ([]session.SessionInfo, error) {
					return nil, injected
				}
			},
			clear: func(state *accountCleanupState) { state.listOwnerSessions = nil },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			rig := newAccountCleanupRig(t)
			application := &Production{Services: ProductionServices{Sessions: rig.manager}}
			owner := ipc.WithUserID(t.Context(), "owner-a")
			connected, tab := rig.open(t, owner, session.Asset{ID: "device/a", Kind: session.KindDevice})

			test.inject(&application.accountCleanup)
			// The failed prepare must return the read failure explicitly, must
			// not commit an event, and must not touch any device.
			if err := application.CleanupAccount(context.Background(), "owner-a", "login-a", "login-a"); err == nil {
				t.Fatal("CleanupAccount swallowed the snapshot read failure")
			}
			requireAccountCleanupTab(t, owner, rig.manager, tab.ID, true)
			requireAccountCleanupStatus(t, owner, rig.manager, connected.ID, session.StatusConnected)

			// The same event retries with a fresh snapshot once the read recovers.
			test.clear(&application.accountCleanup)
			application.CleanupAccount(context.Background(), "owner-a", "login-a", "login-a")
			requireAccountCleanupTab(t, owner, rig.manager, tab.ID, false)
			requireAccountCleanupStatus(t, owner, rig.manager, connected.ID, session.StatusDisconnected)
		})
	}
}

func TestProductionAccountCleanupSnapshotFailureNeverCleansUnbounded(t *testing.T) {
	rig := newAccountCleanupRig(t)
	application := &Production{Services: ProductionServices{Sessions: rig.manager}}
	owner := ipc.WithUserID(t.Context(), "owner-a")
	connected, tab := rig.open(t, owner, session.Asset{ID: "device/a", Kind: session.KindDevice})
	application.accountCleanup.listOwnerSessions = func(context.Context, string) ([]session.SessionInfo, error) {
		return nil, errors.New("persistent listing failure")
	}
	for range 3 {
		application.CleanupAccount(context.Background(), "owner-a", "login-a", "login-a")
	}
	// A persistent read failure leaves every device untouched; the cleanup must
	// not degrade into an unbounded full sweep.
	requireAccountCleanupTab(t, owner, rig.manager, tab.ID, true)
	requireAccountCleanupStatus(t, owner, rig.manager, connected.ID, session.StatusConnected)
}
