package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// waitForTabTitle polls the tab's title until it matches or the deadline
// expires; the pump feeds asynchronously, so title changes land off the
// emitting goroutine.
func waitForTabTitle(t *testing.T, tab *Tab, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := tab.Info().Title; got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("tab title = %q, want %q", tab.Info().Title, want)
		}
		time.Sleep(time.Millisecond)
	}
}

// A durable recover with the default empty canonical title must not latch
// the custom-title precedence: the OSC dynamic title keeps flowing in.
func TestDurableRecoverEmptyCanonicalTitleKeepsOSCTitle(t *testing.T) {
	provider := newFakeDurableProvider()
	manager, _, terminals, connected := newDurableTestManager(t, provider)
	ctx := context.Background()
	tabID := "durable-empty-title"
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, Cols: 80, Rows: 24, Detached: true,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}
	first, err := manager.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	// Drop the first tab without killing the durable record so the recover
	// path can reattach to it.
	if err := manager.closeTab(first, false); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, Cols: 80, Rows: 24, Detached: true,
		Durable: &DurableTabOptions{Recover: true, CanonicalMetadata: true, Title: "", Identity: recoveryIdentity(manager, tabID)},
	}); err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	terminals.terminal(tabID).setTitle("osc-title")
	if err := provider.attachment(tabID, 1).emit([]byte("x")); err != nil {
		t.Fatal(err)
	}
	waitForTabTitle(t, tab, "osc-title")
}

// A durable recover with a real canonical title latches the custom-title
// precedence: later OSC reports never override it.
func TestDurableRecoverCanonicalTitleLatchesCustom(t *testing.T) {
	provider := newFakeDurableProvider()
	manager, _, terminals, connected := newDurableTestManager(t, provider)
	ctx := context.Background()
	tabID := "durable-custom-title"
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, Cols: 80, Rows: 24, Detached: true,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}
	first, err := manager.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.closeTab(first, false); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, Cols: 80, Rows: 24, Detached: true,
		Durable: &DurableTabOptions{Recover: true, CanonicalMetadata: true, Title: "deploy", Identity: recoveryIdentity(manager, tabID)},
	}); err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	terminals.terminal(tabID).setTitle("osc-title")
	if err := provider.attachment(tabID, 1).emit([]byte("x")); err != nil {
		t.Fatal(err)
	}
	// The custom title is latched: the OSC report never overrides it. Give
	// the pump a beat, then assert the title is still the canonical one.
	time.Sleep(100 * time.Millisecond)
	if got := tab.Info().Title; got != "deploy" {
		t.Fatalf("recovered tab title = %q, want the canonical custom title kept", got)
	}
}

// identityAttachment gives the fake durable attachment a supervisor
// identity so canonical rename flows run end to end.
type identityAttachment struct {
	*fakeDurableAttachment
	provider *unlatchMetadataProvider
}

func (a *identityAttachment) Identity() supervisor.Identity {
	return supervisor.Identity{CreatedAt: time.Unix(1, 0), Incarnation: a.provider.incarnation}
}

// unlatchMetadataProvider is a fake durable provider with a controllable
// incarnation and descriptor title.
type unlatchMetadataProvider struct {
	*fakeDurableProvider
	mu          sync.Mutex
	incarnation string
	title       string
	revision    uint64
	renameErr   error
}

func (p *unlatchMetadataProvider) Create(ctx context.Context, options base.DurableCreateOptions) (base.DurableAttachment, error) {
	attachment, err := p.fakeDurableProvider.Create(ctx, options)
	if err != nil {
		return nil, err
	}
	return &identityAttachment{fakeDurableAttachment: attachment.(*fakeDurableAttachment), provider: p}, nil
}

func (p *unlatchMetadataProvider) AttachWithIdentity(ctx context.Context, id string, identity base.DurableIdentity) (base.DurableAttachment, error) {
	return p.AttachFromWithIdentity(ctx, id, identity, 0)
}

func (p *unlatchMetadataProvider) AttachTailWithIdentity(ctx context.Context, id string, identity base.DurableIdentity, _ base.DurableTailOptions) (base.DurableAttachment, error) {
	return p.AttachWithIdentity(ctx, id, identity)
}

func (p *unlatchMetadataProvider) AttachFromWithIdentity(ctx context.Context, id string, identity base.DurableIdentity, offset uint64) (base.DurableAttachment, error) {
	p.mu.Lock()
	incarnation := p.incarnation
	p.mu.Unlock()
	if identity.Incarnation != incarnation {
		return nil, durable.ErrIdentity
	}
	inner, ok := p.fakeDurableProvider.currentDurableIdentity(id)
	if !ok {
		return nil, durable.ErrNotFound
	}
	attachment, err := p.fakeDurableProvider.AttachFromWithIdentity(ctx, id, inner, offset)
	if err != nil {
		return nil, err
	}
	return &identityAttachment{fakeDurableAttachment: attachment.(*fakeDurableAttachment), provider: p}, nil
}

func (p *unlatchMetadataProvider) currentDurableIdentity(id string) (base.DurableIdentity, bool) {
	if _, ok := p.fakeDurableProvider.currentDurableIdentity(id); !ok {
		return base.DurableIdentity{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return base.DurableIdentity{CreatedAt: time.Unix(1, 0), Incarnation: p.incarnation}, true
}

func (p *unlatchMetadataProvider) ListDurableDescriptors(ctx context.Context) ([]base.DurableDescriptor, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	descriptors := make([]base.DurableDescriptor, 0, len(p.records))
	for id, record := range p.records {
		if record.killed == 0 {
			descriptors = append(descriptors, base.DurableDescriptor{
				ID: id, Incarnation: p.incarnation, Title: p.title, MetadataRevision: p.revision,
			})
		}
	}
	return descriptors, nil
}

func (p *unlatchMetadataProvider) RenameDurable(_ context.Context, options base.DurableRenameOptions) (base.DurableRenameResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.renameErr != nil {
		return base.DurableRenameResult{}, p.renameErr
	}
	p.title = options.Title
	p.revision = options.ExpectedMetadataRevision + 1
	return base.DurableRenameResult{Title: p.title, MetadataRevision: p.revision}, nil
}

func (p *unlatchMetadataProvider) setDescriptor(title string, revision uint64, renameErr error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.title = title
	p.revision = revision
	p.renameErr = renameErr
}

// A descriptor refresh reporting an empty title unlatches the custom-title
// precedence: the durable no longer carries a user title, so the OSC title
// resumes.
func TestDurableRenameEmptyDescriptorTitleUnlatches(t *testing.T) {
	provider := &unlatchMetadataProvider{fakeDurableProvider: newFakeDurableProvider(), incarnation: "inc-1"}
	manager, _, terminals, connected := newDurableTestManager(t, provider)
	ctx := context.Background()
	tabID := "durable-unlatch"
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "unlatch-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RenameTab(ctx, tabID, "deploy"); err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	// The durable loses its custom title elsewhere; the next rename attempt
	// refreshes the descriptor and must unlatch the precedence.
	provider.setDescriptor("", 2, durable.ErrIdentity)
	if _, err := manager.RenameTab(ctx, tabID, "other"); err == nil {
		t.Fatal("RenameTab = nil, want the identity error surfaced")
	}
	terminals.terminal(tabID).setTitle("osc-title")
	if err := provider.attachment(tabID, 0).emit([]byte("x")); err != nil {
		t.Fatal(err)
	}
	waitForTabTitle(t, tab, "osc-title")
}
