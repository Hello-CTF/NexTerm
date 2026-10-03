package session

import (
	"context"
	"sync"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

// versionedEventLog plays the role of a connected client that keeps its
// EventVersionGate high-water marks across a backend restart: every event a
// manager emits is recorded, and recovery must resume strictly above it.
type versionedEventLog struct {
	mu       sync.Mutex
	controls []ControlEvent
	exits    []ExitEvent
}

func (l *versionedEventLog) emitter() Emitter {
	return EmitterFunc(func(_ context.Context, event Event) error {
		l.mu.Lock()
		defer l.mu.Unlock()
		switch event.Topic {
		case TopicTerminalControl:
			l.controls = append(l.controls, event.Payload.(ControlEvent))
		case TopicTerminalExit:
			l.exits = append(l.exits, event.Payload.(ExitEvent))
		}
		return nil
	})
}

func (l *versionedEventLog) controlsFor(tabID string) []ControlEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	events := make([]ControlEvent, 0, len(l.controls))
	for _, event := range l.controls {
		if event.TabID == tabID {
			events = append(events, event)
		}
	}
	return events
}

func (l *versionedEventLog) exitsFor(tabID string) []ExitEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	events := make([]ExitEvent, 0, len(l.exits))
	for _, event := range l.exits {
		if event.TabID == tabID {
			events = append(events, event)
		}
	}
	return events
}

func (l *versionedEventLog) maxVersion(tabID string) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	var max uint64
	for _, event := range l.controls {
		if event.TabID == tabID && event.Version > max {
			max = event.Version
		}
	}
	for _, event := range l.exits {
		if event.TabID == tabID && event.Version > max {
			max = event.Version
		}
	}
	return max
}

func TestDurableRecoveryResumesVersionsAboveClientHighWater(t *testing.T) {
	provider := newFakeDurableProvider()
	log := &versionedEventLog{}
	first, _, _, firstSession := newDurableTestManagerWithEmitter(t, provider, log.emitter())
	info := openDurableTestTab(t, first, firstSession, "versions-tab", "a-1", false)
	if err := first.Resize(context.Background(), info.ID, "client-a", 100, 30); err != nil {
		t.Fatal(err)
	}
	// The tmux window now really is 100x30; recovery must adopt that size
	// instead of the fabricated 80x24 default.
	provider.setGrid(info.ID, 100, 30)
	if err := first.DetachChannel("a-1"); err != nil {
		t.Fatal(err)
	}
	before := log.controlsFor(info.ID)
	if len(before) < 3 {
		t.Fatalf("control events before restart = %+v", before)
	}
	highWater := log.maxVersion(info.ID)
	if highWater == 0 {
		t.Fatal("no versioned control events before restart")
	}
	lastBefore := before[len(before)-1]
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, _, secondTerminals, secondSession := newDurableTestManagerWithEmitter(t, provider, log.emitter())
	recovered := openDurableTestTab(t, second, secondSession, info.ID, "b-1", true)
	if recovered.Cols != 100 || recovered.Rows != 30 {
		t.Fatalf("recovered grid = %dx%d, want the real 100x30 window", recovered.Cols, recovered.Rows)
	}
	if terminal := secondTerminals.terminal(info.ID); terminal.cols != 100 || terminal.rows != 30 {
		t.Fatalf("recovered terminal grid = %dx%d, want 100x30", terminal.cols, terminal.rows)
	}
	after := log.controlsFor(info.ID)
	if len(after) <= len(before) {
		t.Fatalf("recovery emitted no control event: before=%d after=%d", len(before), len(after))
	}
	firstAfter := after[len(before)]
	if firstAfter.Version <= highWater {
		t.Fatalf("first recovered event version = %d, want > high-water %d", firstAfter.Version, highWater)
	}
	// Unchanged revision + unchanged grid: a client that observed the last
	// pre-restart snapshot must see a no-op, not a resize echo.
	if firstAfter.GridRevision != lastBefore.GridRevision || firstAfter.Cols != lastBefore.Cols || firstAfter.Rows != lastBefore.Rows {
		t.Fatalf("recovery event = %+v, want unchanged grid %+v", firstAfter, lastBefore)
	}
	for _, event := range after[len(before):] {
		if event.Version <= highWater {
			t.Fatalf("post-recovery event version = %d, want > high-water %d", event.Version, highWater)
		}
	}

	if err := second.Resize(context.Background(), info.ID, "client-a", 110, 40); err != nil {
		t.Fatal(err)
	}
	events := log.controlsFor(info.ID)
	resized := events[len(events)-1]
	if resized.GridRevision != lastBefore.GridRevision+1 {
		t.Fatalf("post-recovery revision = %d, want %d", resized.GridRevision, lastBefore.GridRevision+1)
	}
	if resized.Version <= highWater {
		t.Fatalf("post-recovery resize version = %d, want > %d", resized.Version, highWater)
	}

	provider.finish(info.ID, 7)
	waitFor(t, func() bool { return infoExited(second, info.ID) })
	exits := log.exitsFor(info.ID)
	if len(exits) != 1 || exits[0].Version <= highWater {
		t.Fatalf("exit events after recovery = %+v, want one above high-water %d", exits, highWater)
	}
}

func TestDurableRecoveryAfterInProcessReapKeepsVersions(t *testing.T) {
	provider := newFakeDurableProvider()
	log := &versionedEventLog{}
	manager, _, _, connected := newDurableTestManagerWithEmitter(t, provider, log.emitter())
	info := openDurableTestTab(t, manager, connected, "reap-tab", "a-1", false)
	if err := manager.Resize(context.Background(), info.ID, "client-a", 90, 28); err != nil {
		t.Fatal(err)
	}
	highWater := log.maxVersion(info.ID)

	// Local sessions are not reconnectable: Disconnect reaps the session and
	// drops the tab from the manager while the durable identity survives.
	if err := manager.Disconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(info.ID); err == nil {
		t.Fatal("reaped session kept the tab registered")
	}
	if _, _, killed := provider.counts(); killed != 0 {
		t.Fatal("reap killed the durable identity")
	}

	reconnected, err := manager.Connect(context.Background(), Asset{ID: connected.Asset().ID, Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	recovered := openDurableTestTab(t, manager, reconnected, info.ID, "b-1", true)
	events := log.controlsFor(info.ID)
	firstAfter := events[len(events)-1]
	if firstAfter.Version <= highWater {
		t.Fatalf("post-reap recovery version = %d, want > high-water %d", firstAfter.Version, highWater)
	}
	if recovered.GridRevision == 0 {
		t.Fatal("post-reap recovery reset the grid revision")
	}
}

func TestDurableRecoveryWithoutVersionStoreStartsFresh(t *testing.T) {
	provider := opaqueDurableProvider{DurableProvider: newFakeDurableProvider()}
	log := &versionedEventLog{}
	first, _, _, firstSession := newDurableTestManagerWithEmitter(t, provider, log.emitter())
	info := openDurableTestTab(t, first, firstSession, "opaque-tab", "a-1", false)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, _, _, secondSession := newDurableTestManagerWithEmitter(t, provider, log.emitter())
	openDurableTestTab(t, second, secondSession, info.ID, "b-1", true)
	events := log.controlsFor(info.ID)
	firstAfter := events[len(events)-1]
	if firstAfter.Version != 1 || firstAfter.GridRevision != 0 {
		t.Fatalf("recovery without a version store = %+v, want fresh 1/0", firstAfter)
	}
}

// opaqueDurableProvider hides the optional version-floor/grid interfaces from
// the manager, emulating durable providers that predate (or opt out of)
// cross-restart version persistence.
type opaqueDurableProvider struct{ base.DurableProvider }

type opaqueDurableAttachment struct{ base.DurableAttachment }

func (p opaqueDurableProvider) Create(ctx context.Context, options base.DurableCreateOptions) (base.DurableAttachment, error) {
	attachment, err := p.DurableProvider.Create(ctx, options)
	if attachment == nil {
		return nil, err
	}
	return opaqueDurableAttachment{attachment}, err
}

func (p opaqueDurableProvider) Attach(ctx context.Context, id string) (base.DurableAttachment, error) {
	attachment, err := p.DurableProvider.Attach(ctx, id)
	if attachment == nil {
		return nil, err
	}
	return opaqueDurableAttachment{attachment}, err
}

func newDurableTestManagerWithEmitter(t *testing.T, provider base.DurableProvider, emitter Emitter) (*Manager, *fakeConnector, *fakeTerminalFactory, *Session) {
	t.Helper()
	connector := newFakeConnector()
	terminals := newFakeTerminalFactory()
	manager := NewManager(Config{Connector: connector, Terminals: terminals, Durable: provider, Emitter: emitter})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "durable-asset", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	return manager, connector, terminals, connected
}
