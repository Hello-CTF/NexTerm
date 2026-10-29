package session

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// dlFailingProvider hands out attachments whose Read fails with readErr once
// release is closed (a network loss surfaced by the supervisor Stream).
type dlFailingProvider struct {
	*fakeDurableProvider
	release chan struct{}
	readErr error
}

type dlFailingAttachment struct {
	*fakeDurableAttachment
	release chan struct{}
	readErr error
}

func (a *dlFailingAttachment) Read(p []byte) (int, error) {
	select {
	case <-a.release:
		return 0, a.readErr
	case <-a.fakeChannel.closed:
		return 0, io.EOF
	}
}

func (p *dlFailingProvider) Create(ctx context.Context, o base.DurableCreateOptions) (base.DurableAttachment, error) {
	att, err := p.fakeDurableProvider.Create(ctx, o)
	if err != nil {
		return nil, err
	}
	return &dlFailingAttachment{fakeDurableAttachment: att.(*fakeDurableAttachment), release: p.release, readErr: p.readErr}, nil
}

func dlOpenFailingTab(t *testing.T, readErr error) (*Manager, *dlFailingProvider, string, string) {
	provider := &dlFailingProvider{fakeDurableProvider: newFakeDurableProvider(), release: make(chan struct{}), readErr: readErr}
	manager := NewManager(Config{
		Connector:        newFakeConnector(),
		Terminals:        newFakeTerminalFactory(),
		DurableResolver:  &countingResolver{provider: provider},
		ReconnectBackoff: []time.Duration{10 * time.Millisecond},
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-x", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{SessionID: connected.ID, ClientID: "c", ChannelID: "ch", Cols: 80, Rows: 24, Durable: &DurableTabOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	return manager, provider, connected.ID, info.ID
}

// A stream read error that is not wrapped in base.ErrDisconnected (for example
// the bare context error left by a stalled write timeout) must not mark a
// still-alive durable terminal exited with no reconnect.
func TestBareReadErrorDoesNotEndDurableTab(t *testing.T) {
	for name, readErr := range map[string]error{
		"deadline":   context.DeadlineExceeded,
		"unexpected": io.ErrUnexpectedEOF,
		"closed":     durable.ErrClosed,
	} {
		t.Run(name, func(t *testing.T) {
			manager, provider, _, tabID := dlOpenFailingTab(t, readErr)
			close(provider.release)
			time.Sleep(300 * time.Millisecond)
			tab, err := manager.Tab(tabID)
			if err != nil {
				t.Fatal(err)
			}
			if info := tab.Info(); info.Exited {
				t.Fatalf("tab exited after transient read error %v (no reconnect was started); info=%+v", readErr, info)
			}
		})
	}
}

// Closing (X) a tab that was falsely marked exited must detach, not kill the
// host-side process that is still alive.
func TestCloseOfFalselyExitedDurableTabDoesNotKillHostProcess(t *testing.T) {
	manager, provider, _, tabID := dlOpenFailingTab(t, io.ErrUnexpectedEOF)
	close(provider.release)
	time.Sleep(300 * time.Millisecond)
	tab, _ := manager.Tab(tabID)
	if !tab.Info().Exited {
		t.Skip("tab not exited")
	}
	// closeSessionTab (no mode) on an Exited tab calls CloseTabFor => CloseTab => destroyDurable=true.
	if err := manager.CloseTab(tabID); err != nil {
		t.Logf("CloseTab: %v", err)
	}
	provider.mu.Lock()
	killed := provider.records[tabID].killed
	provider.mu.Unlock()
	if killed != 0 {
		t.Fatalf("host-side durable process killed %d time(s) by closing a tab whose exit was never confirmed", killed)
	}
}

// Attach failures that are network trouble must never be treated as terminal loss.
func TestTransientAttachErrorsKeepTabRecoverable(t *testing.T) {
	for name, attachErr := range map[string]error{
		"unavailable":  durable.ErrUnavailable,
		"disconnected": base.ErrDisconnected,
		"deadline":     context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
			connector := newFakeConnector()
			manager := NewManager(Config{
				Connector: connector, Terminals: newFakeTerminalFactory(),
				DurableResolver: &countingResolver{provider: provider}, ReconnectBackoff: []time.Duration{5 * time.Millisecond},
			})
			t.Cleanup(func() { _ = manager.Close() })
			ctx := context.Background()
			connected, _ := manager.Connect(ctx, Asset{ID: "ssh-t", Kind: KindSSH})
			info, err := manager.OpenTab(ctx, OpenTabOptions{SessionID: connected.ID, ClientID: "c", ChannelID: "ch", Cols: 80, Rows: 24, Durable: &DurableTabOptions{}})
			if err != nil {
				t.Fatal(err)
			}
			provider.mu.Lock()
			provider.attachErr = attachErr
			provider.mu.Unlock()
			rctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
			defer cancel()
			_ = manager.Reconnect(rctx, connected.ID)
			provider.mu.Lock()
			provider.attachErr = nil
			provider.mu.Unlock()
			tab, _ := manager.Tab(info.ID)
			if tab.Info().Exited {
				t.Fatalf("tab exited after transient attach error %v", attachErr)
			}
			if err := manager.Reconnect(ctx, connected.ID); err != nil {
				t.Fatalf("reconnect after network returned: %v", err)
			}
			if tab.Info().Exited {
				t.Fatal("tab still exited after network recovered")
			}
		})
	}
}

// RecoverRemoteDurable must not answer ErrTabNotFound when no host could be
// searched because every session is momentarily reconnecting.
func TestRecoverDuringReconnectIsNotNotFound(t *testing.T) {
	provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		DurableResolver: &countingResolver{provider: provider},
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "ssh-r", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	session, _ := manager.Session(connected.ID)
	session.mu.Lock()
	session.status = StatusReconnecting
	session.mu.Unlock()
	_, err = manager.RecoverRemoteDurable(context.Background(), "tab-that-lives-on-host", OpenTabOptions{})
	if errors.Is(err, ErrTabNotFound) {
		t.Fatalf("search skipped every disconnected session yet reported %v", err)
	}
	owners, oerr := manager.RemoteDurableOwners(context.Background(), "tab-that-lives-on-host", []string{connected.ID})
	if oerr == nil && len(owners) == 0 {
		t.Fatalf("RemoteDurableOwners reported 'no owner, no error' while the only session is reconnecting")
	}
}
