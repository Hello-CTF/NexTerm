package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// sizePushingProvider hands out attachments whose helper can report that
// another process resized the session.
type sizePushingProvider struct {
	*fakeDurableProvider
	mu       sync.Mutex
	attached []*sizePushingAttachment
}

type sizePushingAttachment struct {
	*fakeDurableAttachment
	mu         sync.Mutex
	cols, rows uint32
	wake       chan struct{}
}

func (p *sizePushingProvider) Create(ctx context.Context, options base.DurableCreateOptions) (base.DurableAttachment, error) {
	attachment, err := p.fakeDurableProvider.Create(ctx, options)
	if err != nil {
		return nil, err
	}
	pushing := &sizePushingAttachment{fakeDurableAttachment: attachment.(*fakeDurableAttachment), wake: make(chan struct{}, 1)}
	p.mu.Lock()
	p.attached = append(p.attached, pushing)
	p.mu.Unlock()
	return pushing, nil
}

func (p *sizePushingProvider) push(cols, rows uint32) {
	p.mu.Lock()
	attachment := p.attached[len(p.attached)-1]
	p.mu.Unlock()
	attachment.mu.Lock()
	attachment.cols, attachment.rows = cols, rows
	attachment.mu.Unlock()
	select {
	case attachment.wake <- struct{}{}:
	default:
	}
}

func (a *sizePushingAttachment) DurableSizeUpdates() <-chan struct{} { return a.wake }

func (a *sizePushingAttachment) DurablePushedSize() (uint32, uint32, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cols, a.rows, a.cols != 0
}

func waitControlEvent(t *testing.T, events <-chan ControlEvent, match func(ControlEvent) bool) ControlEvent {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-events:
			if match(event) {
				return event
			}
		case <-deadline:
			t.Fatal("timed out waiting for the control event")
		}
	}
}

// A second desktop attached to the same helper through its own connection
// resizes the PTY: this manager's views must render that size as followers
// instead of drawing the output at their own size, and take it back by
// typing after the handoff hold or by asking for it.
func TestRemoteResizeMakesLocalViewsFollow(t *testing.T) {
	events := make(chan ControlEvent, 256)
	clock := newSessionTestClock()
	provider := &sizePushingProvider{fakeDurableProvider: newFakeDurableProvider()}
	manager := NewManager(Config{
		Connector:       newFakeConnector(),
		Terminals:       newFakeTerminalFactory(),
		DurableResolver: &countingResolver{provider: provider},
		Emitter: EmitterFunc(func(_ context.Context, event Event) error {
			if event.Topic == TopicTerminalControl {
				events <- event.Payload.(ControlEvent)
			}
			return nil
		}),
		Now: clock.Now,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "shared-helper", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "small", ChannelID: "small-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(opened.ID)
	if err != nil {
		t.Fatal(err)
	}
	if owner := tab.Info().SizeOwner; owner != "small" {
		t.Fatalf("size owner before the push = %q", owner)
	}

	provider.push(160, 50)
	event := waitControlEvent(t, events, func(event ControlEvent) bool { return event.SizeOwner == remoteSizeOwner })
	if event.Cols != 160 || event.Rows != 50 {
		t.Fatalf("follow event = %+v, want the pushed 160x50", event)
	}
	// The local view's own size no longer reaches the PTY.
	if err := manager.Resize(ctx, tab.ID, "small", 80, 24); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.Cols != 160 || info.Rows != 50 {
		t.Fatalf("a follower resized the PTY: %+v", info)
	}
	// Typing right after the push keeps the other device's size.
	if err := manager.Write(ctx, tab.ID, "small", []byte("a")); err != nil {
		t.Fatal(err)
	}
	if owner := tab.Info().SizeOwner; owner != remoteSizeOwner {
		t.Fatalf("size owner within the hold = %q", owner)
	}
	clock.Advance(sizeHandoffHold)
	if err := manager.Write(ctx, tab.ID, "small", []byte("b")); err != nil {
		t.Fatal(err)
	}
	if owner := tab.Info().SizeOwner; owner != "small" {
		t.Fatalf("size owner after the hold = %q, want the local client", owner)
	}
	if err := manager.Resize(ctx, tab.ID, "small", 80, 24); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.Cols != 80 || info.Rows != 24 {
		t.Fatalf("the local owner's size = %+v", info)
	}

	// Taking the size back needs no typing.
	provider.push(120, 40)
	waitControlEvent(t, events, func(event ControlEvent) bool { return event.SizeOwner == remoteSizeOwner && event.Cols == 120 })
	if err := manager.TakeSize(tab.ID, "small"); err != nil {
		t.Fatal(err)
	}
	if owner := tab.Info().SizeOwner; owner != "small" {
		t.Fatalf("size owner after TakeSize = %q", owner)
	}
}
