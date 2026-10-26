package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// ownerScopedStore records which owner every call carried and keeps per-owner
// rows to prove no cross-owner leak.
type ownerScopedStore struct {
	mu      sync.Mutex
	offsets map[string]map[string]int64
	titles  map[string]map[string]string
	calls   []string
}

func newOwnerScopedStore() *ownerScopedStore {
	return &ownerScopedStore{
		offsets: make(map[string]map[string]int64),
		titles:  make(map[string]map[string]string),
	}
}

func (s *ownerScopedStore) record(op, owner string) {
	s.calls = append(s.calls, op+":"+owner)
}

func (s *ownerScopedStore) ownerCalls(op, owner string) int {
	count := 0
	for _, call := range s.calls {
		if call == op+":"+owner {
			count++
		}
	}
	return count
}

func (s *ownerScopedStore) DurableTranscriptCatchUpBytesContext(ctx context.Context, tabID string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := userIDFromContext(ctx)
	s.record("catchup", owner)
	return s.offsets[owner][tabID]
}

func (s *ownerScopedStore) PersistDurableTranscriptOffsetContext(ctx context.Context, tabID string, offset int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := userIDFromContext(ctx)
	s.record("persist", owner)
	if s.offsets[owner] == nil {
		s.offsets[owner] = make(map[string]int64)
	}
	s.offsets[owner][tabID] = offset
}

func (s *ownerScopedStore) DeleteDurableTranscriptOffsetContext(ctx context.Context, tabID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := userIDFromContext(ctx)
	s.record("delete", owner)
	delete(s.offsets[owner], tabID)
}

func (s *ownerScopedStore) DurableTabTitleContext(ctx context.Context, tabID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := userIDFromContext(ctx)
	s.record("title", owner)
	return s.titles[owner][tabID]
}

func (s *ownerScopedStore) PersistDurableTabTitleContext(ctx context.Context, tabID, title string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := userIDFromContext(ctx)
	s.record("title-write", owner)
	if s.titles[owner] == nil {
		s.titles[owner] = make(map[string]string)
	}
	s.titles[owner][tabID] = title
}

func (s *ownerScopedStore) DeleteDurableTabTitleContext(ctx context.Context, tabID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := userIDFromContext(ctx)
	s.record("title-delete", owner)
	delete(s.titles[owner], tabID)
}

var (
	_ OwnerScopedDurableTranscriptOffsetStore = (*ownerScopedStore)(nil)
	_ OwnerScopedDurableTabTitleStore         = (*ownerScopedStore)(nil)
)

// ListDurable lets the fake provider back RecoverRemoteDurable's descriptor
// search in these tests.
func (p *fakeDurableProvider) ListDurable(ctx context.Context) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := make([]string, 0, len(p.records))
	for id := range p.records {
		ids = append(ids, id)
	}
	return ids, nil
}

// ListDurableDescriptors reports each live record with the incarnation a
// helper listing carries, so recovery can use checked attach.
func (p *fakeDurableProvider) ListDurableDescriptors(context.Context) ([]base.DurableDescriptor, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	descriptors := make([]base.DurableDescriptor, 0, len(p.records))
	for id, record := range p.records {
		if record.killed == 0 {
			descriptors = append(descriptors, base.DurableDescriptor{ID: id, Incarnation: fakeIncarnation(record.identity)})
		}
	}
	return descriptors, nil
}

func TestDurableTitleWriteAndReadAreOwnerScoped(t *testing.T) {
	provider := newFakeDurableProvider()
	store := newOwnerScopedStore()
	newManager := func() *Manager {
		manager := NewManager(Config{
			Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
			DurableResolver:   &countingResolver{provider: provider},
			TranscriptOffsets: store,
			TabTitles:         store,
		})
		t.Cleanup(func() { _ = manager.Close() })
		return manager
	}
	managerA := newManager()
	managerB := newManager()
	sessionA, err := managerA.Connect(requestTestUser("owner-a"), Asset{ID: "shared-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	sessionB, err := managerB.Connect(requestTestUser("owner-b"), Asset{ID: "shared-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}

	const tabID = "shared-durable-title"
	if _, err := managerA.OpenTab(requestTestUser("owner-a"), OpenTabOptions{
		TabID: tabID, SessionID: sessionA.ID, ClientID: "client-a", ChannelID: "a-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := store.ownerCalls("persist", "owner-a"); got != 1 {
		t.Fatalf("first offset persist owner-a calls = %d, want 1", got)
	}

	if _, err := managerA.RenameTab(requestTestUser("owner-a"), tabID, "title-a"); err != nil {
		t.Fatal(err)
	}
	if got := store.ownerCalls("title-write", "owner-a"); got != 1 {
		t.Fatalf("title write owner-a calls = %d, want 1", got)
	}

	// owner-b recovers the same durable ID on their own manager: the title
	// read must carry owner-b and must not see owner-a's title.
	if _, err := managerB.RecoverRemoteDurable(requestTestUser("owner-b"), tabID, OpenTabOptions{
		SessionID: sessionB.ID, ClientID: "client-b", ChannelID: "b-1", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatal(err)
	}
	if got := store.ownerCalls("title", "owner-b"); got == 0 {
		t.Fatal("recover title read did not carry owner-b")
	}
	if title := store.titles["owner-b"][tabID]; title != "" {
		t.Fatalf("owner-b read owner-a's title %q", title)
	}

	if _, err := managerB.RenameTab(requestTestUser("owner-b"), tabID, "title-b"); err != nil {
		t.Fatal(err)
	}
	if got := store.ownerCalls("title-write", "owner-b"); got != 1 {
		t.Fatalf("title write owner-b calls = %d, want 1", got)
	}
	if store.titles["owner-a"][tabID] != "title-a" || store.titles["owner-b"][tabID] != "title-b" {
		t.Fatalf("titles not isolated: a=%q b=%q", store.titles["owner-a"][tabID], store.titles["owner-b"][tabID])
	}
}

func TestDurableOffsetCatchUpIsOwnerScoped(t *testing.T) {
	provider := newFakeDurableProvider()
	store := newOwnerScopedStore()
	newManager := func() *Manager {
		manager := NewManager(Config{
			Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
			DurableResolver:   &countingResolver{provider: provider},
			TranscriptOffsets: store,
			TabTitles:         store,
		})
		t.Cleanup(func() { _ = manager.Close() })
		return manager
	}
	managerA := newManager()
	managerA2 := newManager()
	managerB := newManager()
	sessionA, err := managerA.Connect(requestTestUser("owner-a"), Asset{ID: "shared-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	sessionA2, err := managerA2.Connect(requestTestUser("owner-a"), Asset{ID: "shared-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	sessionB, err := managerB.Connect(requestTestUser("owner-b"), Asset{ID: "shared-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}

	const tabID = "shared-durable-offset"
	if _, err := managerA.OpenTab(requestTestUser("owner-a"), OpenTabOptions{
		TabID: tabID, SessionID: sessionA.ID, ClientID: "client-a", ChannelID: "a-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}
	// Simulate the offset recorded during the tab's earlier life; the recover
	// on a fresh manager must read it back under owner-a only.
	store.mu.Lock()
	store.offsets["owner-a"] = map[string]int64{tabID: 100}
	store.mu.Unlock()
	// A fresh manager for owner-a recovers the tab: the catch-up read carries
	// owner-a and returns their own pre-seeded offset.
	if _, err := managerA2.RecoverRemoteDurable(requestTestUser("owner-a"), tabID, OpenTabOptions{
		SessionID: sessionA2.ID, ClientID: "client-a", ChannelID: "a-2", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatal(err)
	}
	if got := store.ownerCalls("catchup", "owner-a"); got == 0 {
		t.Fatal("catch-up read did not carry owner-a")
	}
	if got := store.offsets["owner-a"][tabID]; got == 0 {
		t.Fatal("owner-a catch-up read lost the pre-seeded offset")
	}
	if _, err := managerB.RecoverRemoteDurable(requestTestUser("owner-b"), tabID, OpenTabOptions{
		SessionID: sessionB.ID, ClientID: "client-b", ChannelID: "b-1", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatal(err)
	}
	if got := store.ownerCalls("catchup", "owner-b"); got == 0 {
		t.Fatal("catch-up read did not carry owner-b")
	}
	if got := store.offsets["owner-b"][tabID]; got != 0 {
		t.Fatalf("owner-b catch-up read owner-a's offset %d", got)
	}
}

func TestContextDurableStoreWorksWithEmptyOwner(t *testing.T) {
	provider := newFakeDurableProvider()
	store := newOwnerScopedStore()
	newManager := func() *Manager {
		manager := NewManager(Config{
			Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
			DurableResolver:   &countingResolver{provider: provider},
			TranscriptOffsets: store,
			TabTitles:         store,
			OpenAccess:        true,
		})
		t.Cleanup(func() { _ = manager.Close() })
		return manager
	}
	manager := newManager()
	connected, err := manager.Connect(context.Background(), Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	const tabID = "context-open-durable"
	if _, err := manager.OpenTab(context.Background(), OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "a-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RenameTab(context.Background(), tabID, "open-title"); err != nil {
		t.Fatal(err)
	}
	if got := store.titles[""][tabID]; got != "open-title" {
		t.Fatalf("empty-owner title = %q, want open-title", got)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	recoveredManager := newManager()
	recoveredSession, err := recoveredManager.Connect(context.Background(), Asset{ID: "asset-recovered", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := recoveredManager.RecoverRemoteDurable(context.Background(), tabID, OpenTabOptions{
		SessionID: recoveredSession.ID, ClientID: "client-b", ChannelID: "b-1", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Title != "open-title" {
		t.Fatalf("empty-owner recovered title = %q, want open-title", recovered.Title)
	}
	if store.ownerCalls("catchup", "") == 0 || store.ownerCalls("title", "") == 0 {
		t.Fatalf("empty-owner recovery did not use Context reads: %v", store.calls)
	}
	if err := recoveredManager.CloseTab(tabID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.titles[""][tabID]; ok {
		t.Fatal("empty-owner title survived durable close")
	}
	if _, ok := store.offsets[""][tabID]; ok {
		t.Fatal("empty-owner offset survived durable close")
	}
}

func TestReconnectSecondPeriodCarriesOwner(t *testing.T) {
	provider := newFakeDurableProvider()
	store := newOwnerScopedStore()
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		DurableResolver:   &countingResolver{provider: provider},
		TranscriptOffsets: store,
		TabTitles:         store,
		ReconnectBackoff:  []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(requestTestUser("owner-a"), Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	const tabID = "reconnect-owner-tab"
	if _, err := manager.OpenTab(requestTestUser("owner-a"), OpenTabOptions{
		TabID: tabID, SessionID: session.ID, ClientID: "client-a", ChannelID: "a-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}

	if err := manager.StartReconnect(session.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		session.mu.Lock()
		reconnecting := session.reconnecting
		session.mu.Unlock()
		if !reconnecting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reconnect did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	session.mu.Lock()
	owner := userIDFromContext(session.ctx)
	session.mu.Unlock()
	if owner != "owner-a" {
		t.Fatalf("second period ctx owner = %q, want owner-a", owner)
	}
}

var _ base.DurableProvider = (*fakeDurableProvider)(nil)

// ownerRecordingSink records the owner every sink call carried so tests can
// prove the manager stamps the trusted session owner onto the transcript
// context, independent of what the caller's context carried.
type ownerRecordingSink struct {
	mu     sync.Mutex
	owners map[string][]string
}

func newOwnerRecordingSink() *ownerRecordingSink {
	return &ownerRecordingSink{owners: make(map[string][]string)}
}

func (s *ownerRecordingSink) record(method string, ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.owners[method] = append(s.owners[method], ipc.OwnerID(ctx))
}

func (s *ownerRecordingSink) SessionStarted(ctx context.Context, _ TranscriptInfo) {
	s.record("started", ctx)
}

func (s *ownerRecordingSink) SessionOutput(ctx context.Context, _, _, _ string, _ []byte) {
	s.record("output", ctx)
}

func (s *ownerRecordingSink) SessionInput(ctx context.Context, _, _ string, _ []byte) {
	s.record("input", ctx)
}

func (s *ownerRecordingSink) SessionResize(ctx context.Context, _, _ string, _, _ uint32) {
	s.record("resize", ctx)
}

func (s *ownerRecordingSink) SessionEnded(ctx context.Context, _ string) {
	s.record("ended", ctx)
}

func (s *ownerRecordingSink) calls(method string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.owners[method]...)
}

func (s *ownerRecordingSink) assertEveryCallCarries(t *testing.T, method, want string) {
	t.Helper()
	calls := s.calls(method)
	if len(calls) == 0 {
		t.Fatalf("sink recorded no %s calls", method)
	}
	for index, owner := range calls {
		if owner != want {
			t.Fatalf("%s call %d owner = %q, want %q (all calls: %v)", method, index, owner, want, calls)
		}
	}
}

func TestTranscriptSinkCallsCarryTrustedSessionOwner(t *testing.T) {
	connector := newFakeConnector()
	sink := newOwnerRecordingSink()
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(),
		Transcripts: sink, ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	ownerA := requestTestUser("owner-a")
	connected, err := manager.Connect(ownerA, Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ownerA, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "channel-a", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := connector.transport(0).channel(0).emit([]byte("owner-output\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(sink.calls("output")) > 0 })
	if err := manager.Write(ownerA, info.ID, "client-a", []byte("owner-input\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(sink.calls("input")) > 0 })
	if err := manager.Resize(ownerA, info.ID, "client-a", 120, 40); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(sink.calls("resize")) >= 2 })

	if err := manager.Disconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(sink.calls("ended")) == 1 })
	if err := manager.Reconnect(ownerA, connected.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(sink.calls("started")) == 2 })

	for _, method := range []string{"started", "output", "input", "resize", "ended"} {
		sink.assertEveryCallCarries(t, method, "owner-a")
	}
}

// The trusted owner comes from the session record, not the caller's context:
// an open deployment admits identity-less callers, and their transcript calls
// must still write under the session owner's identity. Ownerless sessions on
// open deployments keep the legacy ownerless behavior.
func TestTranscriptSinkOwnerComesFromSessionNotCaller(t *testing.T) {
	connector := newFakeConnector()
	sink := newOwnerRecordingSink()
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(),
		Transcripts: sink, OpenAccess: true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(requestTestUser("owner-a"), Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(context.Background(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "channel-a", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := connector.transport(0).channel(0).emit([]byte("open-output\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(sink.calls("output")) > 0 })
	if err := manager.Write(context.Background(), info.ID, "client-a", []byte("open-input\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(sink.calls("input")) > 0 })
	for _, method := range []string{"started", "output", "input"} {
		sink.assertEveryCallCarries(t, method, "owner-a")
	}

	if _, err := manager.Connect(context.Background(), Asset{ID: "asset-open", Kind: KindSSH}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(sink.calls("started")) == 2 })
	if got := sink.calls("started")[1]; got != "" {
		t.Fatalf("ownerless open-deployment session sink owner = %q, want legacy ownerless", got)
	}
}

func TestReconnectOnceSecondPeriodCarriesOwner(t *testing.T) {
	connector := newFakeConnector()
	sink := newOwnerRecordingSink()
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(),
		Transcripts: sink, ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	ownerA := requestTestUser("owner-a")
	connected, err := manager.Connect(ownerA, Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}

	if err := manager.ReconnectOnceFor(ownerA, connected.ID, Asset{ID: "asset", Kind: KindSSH}); err != nil {
		t.Fatal(err)
	}
	connected.mu.Lock()
	owner := userIDFromContext(connected.ctx)
	connected.mu.Unlock()
	if owner != "owner-a" {
		t.Fatalf("reconnect-once second period ctx owner = %q, want owner-a", owner)
	}
	waitFor(t, func() bool { return len(sink.calls("started")) == 2 && len(sink.calls("ended")) == 1 })
	for _, method := range []string{"started", "ended"} {
		sink.assertEveryCallCarries(t, method, "owner-a")
	}
}
