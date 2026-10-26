//go:build darwin || linux

package production

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
)

// watchDurableProvider is a fake supervisor-daemon boundary: the daemon
// record (identity and output queue) survives manager restarts and closed
// attachments, exactly like a real durable terminal's daemon side. Only the
// daemon boundary is fake — every manager, pump, ring and watch runtime in
// these tests is the production one.
type watchDurableProvider struct {
	mu      sync.Mutex
	records map[string]*watchDurableRecord
}

type watchDurableRecord struct {
	identity base.DurableIdentity
	queue    chan []byte
	killed   bool
}

func newWatchDurableProvider() *watchDurableProvider {
	return &watchDurableProvider{records: map[string]*watchDurableRecord{}}
}

func (p *watchDurableProvider) recordFor(id string) *watchDurableRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.records[id]
}

func (p *watchDurableProvider) emit(t *testing.T, id string, data []byte) {
	t.Helper()
	record := p.recordFor(id)
	if record == nil {
		t.Fatalf("no durable record for %s", id)
	}
	record.queue <- append([]byte(nil), data...)
}

func (p *watchDurableProvider) Create(_ context.Context, options base.DurableCreateOptions) (base.DurableAttachment, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if record := p.records[options.ID]; record != nil && !record.killed {
		return nil, durable.ErrAlreadyExists
	}
	record := &watchDurableRecord{
		identity: base.DurableIdentity{CreatedAt: time.Now().UTC(), Incarnation: "inc-" + options.ID},
		queue:    make(chan []byte, 64),
	}
	p.records[options.ID] = record
	return &watchDurableAttachment{id: options.ID, record: record, closed: make(chan struct{})}, nil
}

func (p *watchDurableProvider) AttachWithIdentity(_ context.Context, id string, identity base.DurableIdentity) (base.DurableAttachment, error) {
	return p.attachFrom(id, identity)
}

func (p *watchDurableProvider) AttachFromWithIdentity(_ context.Context, id string, identity base.DurableIdentity, _ uint64) (base.DurableAttachment, error) {
	return p.attachFrom(id, identity)
}

func (p *watchDurableProvider) attachFrom(id string, identity base.DurableIdentity) (base.DurableAttachment, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	record := p.records[id]
	if record == nil || record.killed {
		return nil, durable.ErrNotFound
	}
	if record.identity.Incarnation != identity.Incarnation {
		return nil, durable.ErrIdentity
	}
	return &watchDurableAttachment{id: id, record: record, closed: make(chan struct{})}, nil
}

type watchDurableAttachment struct {
	id     string
	record *watchDurableRecord
	closed chan struct{}
	once   sync.Once
}

func (a *watchDurableAttachment) Read(buffer []byte) (int, error) {
	select {
	case data, ok := <-a.record.queue:
		if !ok {
			return 0, io.EOF
		}
		return copy(buffer, data), nil
	case <-a.closed:
		return 0, io.EOF
	}
}

func (a *watchDurableAttachment) Write(data []byte) (int, error) { return len(data), nil }
func (a *watchDurableAttachment) Stderr() io.Reader              { return nil }
func (a *watchDurableAttachment) Resize(context.Context, uint32, uint32) error {
	return nil
}
func (a *watchDurableAttachment) Wait(ctx context.Context) error {
	select {
	case <-a.closed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (a *watchDurableAttachment) CloseWrite() error { return nil }
func (a *watchDurableAttachment) ID() string        { return a.id }
func (a *watchDurableAttachment) Generation() uint64 {
	return 1
}
func (a *watchDurableAttachment) Close() error {
	a.once.Do(func() { close(a.closed) })
	return nil
}
func (a *watchDurableAttachment) Kill(context.Context) error {
	a.Close()
	return nil
}
func (a *watchDurableAttachment) Identity() supervisor.Identity {
	return supervisor.Identity{CreatedAt: a.record.identity.CreatedAt, Incarnation: a.record.identity.Incarnation}
}

// watchRecoveryManager builds a session manager whose durable tabs run on
// the fake daemon boundary; every other piece is production.
func watchRecoveryManager(t *testing.T, provider *watchDurableProvider, hook func(context.Context, session.Event)) *session.Manager {
	t.Helper()
	config := session.Config{
		IdleTimeout: -1,
		Durable:     provider,
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
		}),
	}
	if hook != nil {
		config.Emitter = session.EmitterFunc(func(ctx context.Context, event session.Event) error {
			hook(ctx, event)
			return nil
		})
	}
	manager := session.NewManager(config)
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func openWatchDurableTab(t *testing.T, ctx context.Context, manager *session.Manager, connected *session.Session, provider *watchDurableProvider, tabID string, recover bool) session.TabInfo {
	t.Helper()
	options := &session.DurableTabOptions{Command: []string{"/bin/sh"}, Recover: recover}
	if recover {
		if record := provider.recordFor(tabID); record != nil {
			identity := record.identity
			options.Identity = &identity
		}
	}
	info, err := manager.OpenTabFor(ctx, session.OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "a-1", Cols: 80, Rows: 24,
		Durable: options,
	})
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// The P1 regression over a real manager restart: an armed watch on a durable
// tab loses its runner when the manager closes (the daemon survives), the
// watch is never misreported gone while the tab is missing, and recovery with
// the same daemon incarnation re-attaches through the automatic event
// trigger — settling the old watch honestly (the recovered tab is a fresh
// stream with new coordinates) and serving new watches immediately.
func TestProductionWatchDurableRecoveryReattachesAndStaysHonest(t *testing.T) {
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	provider := newWatchDurableProvider()
	ctx := watchOwnerCtx("owner-a")
	events := &watchEventCapture{}

	manager1 := watchRecoveryManager(t, provider, nil)
	connected1, err := manager1.Connect(ctx, session.Asset{ID: "asset-durable", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	tab := openWatchDurableTab(t, ctx, manager1, connected1, provider, "durable-tab-1", false)
	service1, dispatcher1 := newWatchTestService(t, database, manager1, events, 10*time.Millisecond)
	conversationID := provisionWatchConversation(t, database, ctx, tab.ID)

	create := dispatchWatch(t, ctx, dispatcher1, "ai_watch_create", `{"targetId":"`+tab.ID+`","condition":{"type":"contains","pattern":"nexterm-durable-ready"},"repeat":false,"expiresAt":0}`)
	if !create.OK {
		t.Fatalf("ai_watch_create = %+v", create.Error)
	}
	var summary WatchSummary
	if err := json.Unmarshal(create.Data, &summary); err != nil {
		t.Fatal(err)
	}
	identity1 := summaryIdentity(t, ctx, manager1, tab.ID)
	provider.emit(t, tab.ID, []byte("boot\n"))
	waitWatchAnchorAdvance(t, database, ctx, summary.ID, 0)

	// The manager closes; the daemon record survives but the runner dies.
	if err := manager1.Close(); err != nil {
		t.Fatal(err)
	}
	waitWatchRunnerState(t, service1, "owner-a", tab.ID, false)
	row, err := database.AgentWatchGet(ctx, summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.AgentWatchStatusArmed {
		t.Fatalf("watch of a missing tab = %s; want armed, never gone", row.Status)
	}
	targetRow, err := database.AgentWatchTargetGet(ctx, tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if targetRow.Gone {
		t.Fatal("a missing tab must never be recorded gone")
	}

	// Recovery with the same daemon incarnation: a new manager, a fresh
	// service over the same store, and the control-event-driven reconcile.
	attached := make(chan string, 4)
	var service2 *watchService
	manager2 := watchRecoveryManager(t, provider, func(eventCtx context.Context, event session.Event) {
		if service2 != nil {
			service2.onSessionEvent(eventCtx, event)
		}
	})
	service2, dispatcher2 := newWatchTestService(t, database, manager2, events, 10*time.Millisecond)
	service2.attachHook = func(targetID string) { attached <- targetID }
	connected2, err := manager2.Connect(ctx, session.Asset{ID: "asset-durable", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	tab2 := openWatchDurableTab(t, ctx, manager2, connected2, provider, tab.ID, true)
	identity2 := summaryIdentity(t, ctx, manager2, tab2.ID)
	if identity2 == identity1 {
		t.Fatal("a recovered tab must carry a fresh stream epoch in its watch identity")
	}
	select {
	case targetID := <-attached:
		if targetID != tab.ID {
			t.Fatalf("reconcile attached %s; want %s", targetID, tab.ID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the recovery event never triggered the reconcile re-attach")
	}

	// The old watch is settled honestly: the recovered stream's coordinates
	// are fresh, so its pre-recovery condition can never be verified.
	row, err = database.AgentWatchGet(ctx, summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.AgentWatchStatusUnverifiable {
		t.Fatalf("pre-recovery watch = %s; want unverifiable after the identity advanced", row.Status)
	}
	waitWatchRunnerState(t, service2, "owner-a", tab.ID, true)

	// A new watch on the recovered stream fires on new output with the new
	// stream's coordinates.
	create2 := dispatchWatch(t, ctx, dispatcher2, "ai_watch_create", `{"targetId":"`+tab.ID+`","condition":{"type":"contains","pattern":"nexterm-durable-ready"},"repeat":false,"expiresAt":0}`)
	if !create2.OK {
		t.Fatalf("ai_watch_create after recovery = %+v", create2.Error)
	}
	provider.emit(t, tab.ID, []byte("nexterm-durable-ready\n"))
	waitWatchHistoryDeliveries(t, database, ctx, conversationID, 1)
	rows, err := database.MsgList(ctx, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	deliveries := agent.WatchHistoryToolData(rows)
	if deliveries[0].Event.TargetID != tab.ID || deliveries[0].Event.Identity != identity2 {
		t.Fatalf("delivered event after recovery = %+v; want the recovered identity %s", deliveries[0].Event, identity2)
	}
}

func summaryIdentity(t *testing.T, ctx context.Context, manager *session.Manager, tabID string) string {
	t.Helper()
	state, ok := manager.WatchStreamState(ctx, tabID)
	if !ok || !state.EpisodeOK {
		t.Fatalf("watch stream state for %s = %+v, %v", tabID, state, ok)
	}
	return state.WatchIdentity()
}

func waitWatchAnchorAdvance(t *testing.T, database *store.Store, ctx context.Context, watchID string, anchor int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		row, err := database.AgentWatchGet(ctx, watchID)
		if err != nil {
			t.Fatal(err)
		}
		if row.AnchorSeq > anchor {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("watch %s anchor stuck at %d", watchID, row.AnchorSeq)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
