package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

var errFakeDurableIdentity = errors.New("fake durable identity changed")

type fakeDurableProvider struct {
	mu            sync.Mutex
	records       map[string]*fakeDurableRecord
	titles        map[string]string
	createOptions []base.DurableCreateOptions
	createCalls   int
	attachCalls   int
	createErr     error
	attachErr     error
	killErr       error
}

type fakeDurableRecord struct {
	identity    int
	output      []byte
	transcribed int64
	killed      int
	exitCode    *int
	attachments []*fakeDurableAttachment
	cols        uint32
	rows        uint32
	floorEvent  uint64
	floorGrid   uint64
}

type fakeDurableAttachment struct {
	*fakeChannel
	provider *fakeDurableProvider
	tabID    string
	identity int
}

func newFakeDurableProvider() *fakeDurableProvider {
	return &fakeDurableProvider{records: make(map[string]*fakeDurableRecord), titles: make(map[string]string)}
}

func (p *fakeDurableProvider) Create(_ context.Context, options base.DurableCreateOptions) (base.DurableAttachment, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.createCalls++
	options.Command = append([]string(nil), options.Command...)
	options.Env = append([]string(nil), options.Env...)
	p.createOptions = append(p.createOptions, options)
	if p.createErr != nil {
		return nil, p.createErr
	}
	identity := 1
	if previous := p.records[options.ID]; previous != nil {
		if previous.killed == 0 {
			return nil, durable.ErrAlreadyExists
		}
		identity = previous.identity + 1
	}
	record := &fakeDurableRecord{identity: identity}
	p.records[options.ID] = record
	return p.newAttachmentLocked(options.ID, record), nil
}

func fakeIncarnation(identity int) string {
	return fmt.Sprintf("fake-incarnation-%d", identity)
}

func (p *fakeDurableProvider) AttachWithIdentity(ctx context.Context, id string, identity base.DurableIdentity) (base.DurableAttachment, error) {
	return p.AttachFromWithIdentity(ctx, id, identity, 0)
}

func (p *fakeDurableProvider) AttachFromWithIdentity(_ context.Context, id string, identity base.DurableIdentity, offset uint64) (base.DurableAttachment, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attachCalls++
	if p.attachErr != nil {
		return nil, p.attachErr
	}
	if identity.Incarnation == "" {
		return nil, durable.ErrInvalidInput
	}
	record := p.records[id]
	if record == nil || record.killed > 0 {
		return nil, durable.ErrNotFound
	}
	if identity.Incarnation != fakeIncarnation(record.identity) {
		return nil, durable.ErrIdentity
	}
	attachment := p.newAttachmentLocked(id, record)
	if offset < uint64(len(record.output)) {
		attachment.reads <- append([]byte(nil), record.output[offset:]...)
	}
	return attachment, nil
}

// currentDurableIdentity is the identity a descriptor listing would report.
func (p *fakeDurableProvider) currentDurableIdentity(id string) (base.DurableIdentity, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	record := p.records[id]
	if record == nil {
		return base.DurableIdentity{}, false
	}
	return base.DurableIdentity{Incarnation: fakeIncarnation(record.identity)}, true
}

func (p *fakeDurableProvider) DurableTranscriptCatchUpBytesContext(_ context.Context, tabID string) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	record := p.records[tabID]
	if record == nil {
		return 0
	}
	return record.transcribed
}

func (p *fakeDurableProvider) PersistDurableTranscriptOffsetContext(_ context.Context, tabID string, offset int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if record := p.records[tabID]; record != nil {
		record.transcribed = offset
	}
}

func (p *fakeDurableProvider) DeleteDurableTranscriptOffsetContext(_ context.Context, tabID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if record := p.records[tabID]; record != nil {
		record.transcribed = 0
	}
}

func (p *fakeDurableProvider) DurableTabTitleContext(_ context.Context, tabID string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.titles[tabID]
}

func (p *fakeDurableProvider) PersistDurableTabTitleContext(_ context.Context, tabID, title string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.titles[tabID] = title
}

func (p *fakeDurableProvider) DeleteDurableTabTitleContext(_ context.Context, tabID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.titles, tabID)
}

func (p *fakeDurableProvider) newAttachmentLocked(id string, record *fakeDurableRecord) *fakeDurableAttachment {
	channel := newFakeChannel(0)
	channel.id = id
	attachment := &fakeDurableAttachment{fakeChannel: channel, provider: p, tabID: id, identity: record.identity}
	record.attachments = append(record.attachments, attachment)
	return attachment
}

func (a *fakeDurableAttachment) Identity() supervisor.Identity {
	return supervisor.Identity{Incarnation: fakeIncarnation(a.identity)}
}

func (a *fakeDurableAttachment) Kill(context.Context) error {
	a.provider.mu.Lock()
	defer a.provider.mu.Unlock()
	if a.provider.killErr != nil {
		return a.provider.killErr
	}
	record := a.provider.records[a.tabID]
	if record == nil || record.identity != a.identity {
		return errFakeDurableIdentity
	}
	if record.killed == 0 {
		record.killed++
	}
	return a.fakeChannel.Close()
}

func (a *fakeDurableAttachment) Wait(context.Context) error {
	a.provider.mu.Lock()
	defer a.provider.mu.Unlock()
	record := a.provider.records[a.tabID]
	if record == nil || record.exitCode == nil {
		return nil
	}
	return &base.ExitError{Code: *record.exitCode}
}

func (a *fakeDurableAttachment) DurableVersions() (uint64, uint64, error) {
	a.provider.mu.Lock()
	defer a.provider.mu.Unlock()
	record := a.provider.records[a.tabID]
	if record == nil || record.identity != a.identity {
		return 0, 0, errFakeDurableIdentity
	}
	return record.floorEvent, record.floorGrid, nil
}

func (a *fakeDurableAttachment) PersistDurableVersions(eventVersion, gridRevision uint64) error {
	a.provider.mu.Lock()
	defer a.provider.mu.Unlock()
	record := a.provider.records[a.tabID]
	if record == nil || record.identity != a.identity {
		return errFakeDurableIdentity
	}
	record.floorEvent = max(record.floorEvent, eventVersion)
	record.floorGrid = max(record.floorGrid, gridRevision)
	return nil
}

func (a *fakeDurableAttachment) DurableGrid() (uint32, uint32, bool) {
	a.provider.mu.Lock()
	defer a.provider.mu.Unlock()
	record := a.provider.records[a.tabID]
	if record == nil || record.killed > 0 || record.cols == 0 || record.rows == 0 {
		return 0, 0, false
	}
	return record.cols, record.rows, true
}

func (p *fakeDurableProvider) setGrid(id string, cols, rows uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	record := p.records[id]
	record.cols, record.rows = cols, rows
}

func (p *fakeDurableProvider) floor(id string) (eventVersion, gridRevision uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	record := p.records[id]
	return record.floorEvent, record.floorGrid
}

func (a *fakeDurableAttachment) emit(data []byte) error {
	a.provider.mu.Lock()
	record := a.provider.records[a.tabID]
	if record == nil || record.killed > 0 {
		a.provider.mu.Unlock()
		return base.ErrClosed
	}
	record.output = append(record.output, data...)
	a.provider.mu.Unlock()
	return a.fakeChannel.emit(data)
}

func (p *fakeDurableProvider) attachment(id string, index int) *fakeDurableAttachment {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.records[id].attachments[index]
}

func (p *fakeDurableProvider) counts() (create, attach, killed int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	create, attach = p.createCalls, p.attachCalls
	for _, record := range p.records {
		killed += record.killed
	}
	return create, attach, killed
}

func (p *fakeDurableProvider) created(index int) base.DurableCreateOptions {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.createOptions[index]
}

func (p *fakeDurableProvider) replaceIdentity(id string, identity int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.records[id].identity = identity
}

func (p *fakeDurableProvider) finish(id string, code int) {
	p.mu.Lock()
	record := p.records[id]
	record.exitCode = &code
	attachments := append([]*fakeDurableAttachment(nil), record.attachments...)
	p.mu.Unlock()
	for _, attachment := range attachments {
		_ = attachment.fakeChannel.Close()
	}
}

func newDurableTestManager(t *testing.T, provider base.DurableProvider) (*Manager, *fakeConnector, *fakeTerminalFactory, *Session) {
	t.Helper()
	connector := newFakeConnector()
	terminals := newFakeTerminalFactory()
	config := Config{Connector: connector, Terminals: terminals, Durable: provider}
	if store, ok := provider.(OwnerScopedDurableTranscriptOffsetStore); ok {
		config.TranscriptOffsets = store
	}
	if store, ok := provider.(OwnerScopedDurableTabTitleStore); ok {
		config.TabTitles = store
	}
	manager := NewManager(config)
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: ids.New(), Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	return manager, connector, terminals, connected
}

func openDurableTestTab(t *testing.T, manager *Manager, connected *Session, id, channel string, recover bool) TabInfo {
	t.Helper()
	options := &DurableTabOptions{Command: []string{"/bin/sh", "-c", "exit 99"}, Recover: recover}
	if recover {
		options.Identity = recoveryIdentity(manager, id)
	}
	info, err := manager.OpenTab(context.Background(), OpenTabOptions{
		TabID: id, SessionID: connected.ID, ClientID: "client-a", ChannelID: channel, Cols: 80, Rows: 24,
		Durable: options,
	})
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// recoveryIdentity returns the identity production recovery reads from the
// provider's descriptor listing, or nil when the provider cannot report one.
func recoveryIdentity(manager *Manager, id string) *base.DurableIdentity {
	if source, ok := manager.durable.(interface {
		currentDurableIdentity(string) (base.DurableIdentity, bool)
	}); ok {
		identity, found := source.currentDurableIdentity(id)
		if !found {
			return nil
		}
		return &identity
	}
	if manager.durable == nil {
		return nil
	}
	descriptor, found, err := findDurableDescriptor(context.Background(), manager.durable, id)
	if err != nil || !found || descriptor.Incarnation == "" {
		return nil
	}
	return &base.DurableIdentity{CreatedAt: descriptor.CreatedAt, Incarnation: descriptor.Incarnation}
}

func TestDurableStableIDAndRPCAttachReplayDedup(t *testing.T) {
	provider := newFakeDurableProvider()
	manager, connector, terminals, connected := newDurableTestManager(t, provider)
	info := openDurableTestTab(t, manager, connected, "stable-tab", "a-1", false)
	if !info.Durable || info.ID != "stable-tab" {
		t.Fatalf("durable tab info = %+v", info)
	}
	created := provider.created(0)
	if created.ID != info.ID || len(created.Command) == 0 || connector.transport(0).channelCount() != 0 {
		t.Fatalf("durable create = %+v, volatile channels = %d", created, connector.transport(0).channelCount())
	}
	if err := provider.attachment(info.ID, 0).emit([]byte("boot")); err != nil {
		t.Fatal(err)
	}
	terminal := terminals.terminal(info.ID)
	waitFor(t, func() bool { return bytes.Equal(terminal.bytes(), []byte("boot")) })
	if err := manager.DetachChannel("a-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, killed := provider.counts(); killed != 0 {
		t.Fatalf("detach killed %d durable tabs", killed)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1", ReplayBytes: -1}); err != nil {
		t.Fatal(err)
	}
	if create, attach, _ := provider.counts(); create != 1 || attach != 0 {
		t.Fatalf("RPC attach re-opened durable recording: create=%d attach=%d", create, attach)
	}
	if got := terminal.bytes(); !bytes.Equal(got, []byte("boot")) {
		t.Fatalf("RPC attach fed emulator again: %q", got)
	}
	receiver := bindTestReceiver(t, manager, "b-1")
	assertFrameData(t, receiver, replayClear)
	assertFrameData(t, receiver, []byte("boot"))
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, killed := provider.counts(); killed != 0 {
		t.Fatalf("manager shutdown killed %d durable tabs", killed)
	}
}

func TestDurableManagerRestartRecoveryDetachSurvivalAndExplicitKill(t *testing.T) {
	provider := newFakeDurableProvider()
	first, _, _, firstSession := newDurableTestManager(t, provider)
	info := openDurableTestTab(t, first, firstSession, "restart-tab", "a-1", false)
	if err := provider.attachment(info.ID, 0).emit([]byte("boot")); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, _, secondTerminals, secondSession := newDurableTestManager(t, provider)
	recovered := openDurableTestTab(t, second, secondSession, info.ID, "b-1", true)
	if recovered.ID != info.ID {
		t.Fatalf("recovered ID = %q, want %q", recovered.ID, info.ID)
	}
	if create, attach, killed := provider.counts(); create != 1 || attach != 1 || killed != 0 {
		t.Fatalf("recovery reran or killed process: create=%d attach=%d killed=%d", create, attach, killed)
	}
	secondTerminal := secondTerminals.terminal(info.ID)
	waitFor(t, func() bool { return bytes.Equal(secondTerminal.bytes(), []byte("boot")) })
	if err := provider.attachment(info.ID, 1).emit([]byte("-live")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Equal(secondTerminal.bytes(), []byte("boot-live")) })
	if _, err := second.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-c", ChannelID: "c-1", ReplayBytes: -1}); err != nil {
		t.Fatal(err)
	}
	if got := secondTerminal.bytes(); !bytes.Equal(got, []byte("boot-live")) {
		t.Fatalf("hub replay changed recovered emulator: %q", got)
	}
	receiver := bindTestReceiver(t, second, "c-1")
	assertFrameData(t, receiver, replayClear)
	assertFrameData(t, receiver, []byte("boot-live"))
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, killed := provider.counts(); killed != 0 {
		t.Fatal("second shutdown killed durable process")
	}

	third, _, thirdTerminals, thirdSession := newDurableTestManager(t, provider)
	openDurableTestTab(t, third, thirdSession, info.ID, "d-1", true)
	waitFor(t, func() bool { return bytes.Equal(thirdTerminals.terminal(info.ID).bytes(), []byte("boot-live")) })
	if err := third.DetachAll(info.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, killed := provider.counts(); killed != 0 {
		t.Fatal("DetachAll killed durable process")
	}
	if err := third.CloseTab(info.ID); err != nil {
		t.Fatal(err)
	}
	if create, attach, killed := provider.counts(); create != 1 || attach != 2 || killed != 1 {
		t.Fatalf("explicit close counts: create=%d attach=%d killed=%d", create, attach, killed)
	}
	if _, err := provider.AttachWithIdentity(context.Background(), info.ID, base.DurableIdentity{Incarnation: fakeIncarnation(1)}); !errors.Is(err, durable.ErrNotFound) {
		t.Fatalf("killed durable tab attach error = %v", err)
	}
}

func TestDurableTabTitleSurvivesRestartAndDiesWithTab(t *testing.T) {
	provider := newFakeDurableProvider()
	first, _, _, firstSession := newDurableTestManager(t, provider)
	info := openDurableTestTab(t, first, firstSession, "title-tab", "a-1", false)
	if info.Title != "" {
		t.Fatalf("fresh durable tab title = %q", info.Title)
	}
	renamed, err := first.RenameTab(context.Background(), info.ID, "构建机")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Title != "构建机" || provider.DurableTabTitleContext(context.Background(), info.ID) != "构建机" {
		t.Fatalf("rename info = %+v, persisted = %q", renamed, provider.DurableTabTitleContext(context.Background(), info.ID))
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, _, _, secondSession := newDurableTestManager(t, provider)
	recovered := openDurableTestTab(t, second, secondSession, info.ID, "b-1", true)
	if recovered.Title != "构建机" {
		t.Fatalf("recovered title = %q", recovered.Title)
	}
	tab, err := second.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := tab.Info().Title; got != "构建机" {
		t.Fatalf("recovered tab info title = %q", got)
	}
	if err := second.CloseTab(info.ID); err != nil {
		t.Fatal(err)
	}
	if got := provider.DurableTabTitleContext(context.Background(), info.ID); got != "" {
		t.Fatalf("title after kill = %q", got)
	}
}

func TestDurableOpenErrorsNeverFallBackToVolatilePTY(t *testing.T) {
	t.Run("provider unavailable", func(t *testing.T) {
		manager, connector, _, connected := newDurableTestManager(t, nil)
		_, err := manager.OpenTab(context.Background(), OpenTabOptions{
			SessionID: connected.ID, ChannelID: "a-1", Cols: 80, Rows: 24, Durable: &DurableTabOptions{},
		})
		if !errors.Is(err, ErrUnsupported) || connector.transport(0).channelCount() != 0 {
			t.Fatalf("missing provider error=%v volatile channels=%d", err, connector.transport(0).channelCount())
		}
	})
	for _, test := range []struct {
		name      string
		recover   bool
		provider  *fakeDurableProvider
		wantError error
	}{
		{name: "create unavailable", provider: &fakeDurableProvider{records: make(map[string]*fakeDurableRecord), createErr: durable.ErrUnavailable}, wantError: durable.ErrUnavailable},
		{name: "recover not found", recover: true, provider: newFakeDurableProvider(), wantError: durable.ErrNotFound},
		{name: "recover identity", recover: true, provider: &fakeDurableProvider{records: make(map[string]*fakeDurableRecord), attachErr: durable.ErrIdentity}, wantError: durable.ErrIdentity},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, connector, _, connected := newDurableTestManager(t, test.provider)
			options := &DurableTabOptions{Recover: test.recover}
			if test.recover {
				options.Identity = &base.DurableIdentity{Incarnation: fakeIncarnation(1)}
			}
			_, err := manager.OpenTab(context.Background(), OpenTabOptions{
				TabID: "error-tab", SessionID: connected.ID, ChannelID: "a-1", Cols: 80, Rows: 24,
				Durable: options,
			})
			if !errors.Is(err, test.wantError) || connector.transport(0).channelCount() != 0 {
				t.Fatalf("error=%v volatile channels=%d", err, connector.transport(0).channelCount())
			}
			if create, _, _ := test.provider.counts(); test.recover && create != 0 {
				t.Fatalf("recovery called Create %d times", create)
			}
		})
	}
}

func TestDurableCloseIdentityFailureRetainsTabAndExitedAttachment(t *testing.T) {
	provider := newFakeDurableProvider()
	manager, _, _, connected := newDurableTestManager(t, provider)
	info := openDurableTestTab(t, manager, connected, "identity-tab", "a-1", false)
	provider.replaceIdentity(info.ID, 2)
	if err := manager.CloseTab(info.ID); !errors.Is(err, errFakeDurableIdentity) {
		t.Fatalf("replacement close error = %v", err)
	}
	if _, err := manager.Tab(info.ID); err != nil {
		t.Fatalf("identity failure unregistered tab: %v", err)
	}
	if _, _, killed := provider.counts(); killed != 0 {
		t.Fatal("identity failure killed replacement")
	}
	provider.replaceIdentity(info.ID, 1)
	provider.finish(info.ID, 7)
	waitFor(t, func() bool { return infoExited(manager, info.ID) })
	if err := manager.CloseTab(info.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, killed := provider.counts(); killed != 1 {
		t.Fatalf("exited durable close killed %d processes", killed)
	}
}

func TestDurableCloseRemoteAlreadyEndedStillRemovesLocalTab(t *testing.T) {
	provider := newFakeDurableProvider()
	provider.killErr = durable.ErrNotFound
	manager, _, _, connected := newDurableTestManager(t, provider)
	info := openDurableTestTab(t, manager, connected, "already-ended", "a-1", false)

	if err := manager.CloseTab(info.ID); err != nil {
		t.Fatalf("close already-ended durable tab = %v, want nil", err)
	}
	if _, err := manager.Tab(info.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("already-ended durable tab remains registered: %v", err)
	}
}

func infoExited(manager *Manager, id string) bool {
	tab, err := manager.Tab(id)
	return err == nil && tab.Info().Exited
}

func TestDurableGeneratedIDAndConcurrentRecoveryAndClose(t *testing.T) {
	provider := newFakeDurableProvider()
	first, _, _, firstSession := newDurableTestManager(t, provider)
	info, err := first.OpenTab(context.Background(), OpenTabOptions{
		SessionID: firstSession.ID, ClientID: "client-a", ChannelID: "a-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ids.Valid(info.ID) || provider.created(0).ID != info.ID {
		t.Fatalf("generated durable ID %q is invalid or unstable", info.ID)
	}
	if err := provider.attachment(info.ID, 0).emit([]byte("once")); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	manager, _, _, connected := newDurableTestManager(t, provider)
	const callers = 8
	results := make(chan error, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	start := make(chan struct{})
	for index := 0; index < callers; index++ {
		go func(index int) {
			ready.Done()
			<-start
			_, err := manager.OpenTab(context.Background(), OpenTabOptions{
				TabID: info.ID, SessionID: connected.ID, ClientID: "client", ChannelID: fmt.Sprintf("recover-%d", index),
				Cols: 80, Rows: 24, Durable: &DurableTabOptions{Recover: true, Identity: recoveryIdentity(manager, info.ID)},
			})
			results <- err
		}(index)
	}
	ready.Wait()
	close(start)
	succeeded := 0
	for index := 0; index < callers; index++ {
		err := <-results
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrTabExists) {
			t.Fatalf("concurrent recovery error = %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful registrations = %d, want one", succeeded)
	}
	registered, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Equal(registered.terminal.(*fakeTerminal).bytes(), []byte("once")) })
	if create, _, killed := provider.counts(); create != 1 || killed != 0 {
		t.Fatalf("concurrent recovery reran or killed process: create=%d killed=%d", create, killed)
	}

	closeResults := make(chan error, callers)
	for index := 0; index < callers; index++ {
		go func() { closeResults <- manager.CloseTab(info.ID) }()
	}
	for index := 0; index < callers; index++ {
		if err := <-closeResults; err != nil {
			t.Fatal(err)
		}
	}
	if _, _, killed := provider.counts(); killed != 1 {
		t.Fatalf("concurrent explicit close killed %d processes", killed)
	}
}
