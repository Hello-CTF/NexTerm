package aicontext

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// The context builder's session dependencies share the tools-layer gate: an
// identity-less caller is elevated to open access only in trusted-open
// shapes, so a server gateway request without an identity stays denied.
func newGateManager(t *testing.T) *session.Manager {
	t.Helper()
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return nil, errors.New("connector unused in gate tests")
		}),
		Terminals: session.TerminalFactoryFunc(func(config session.TerminalConfig) (session.TerminalState, error) {
			return terminal.NewTab(config.TabID, config.SessionID, int(config.Cols), int(config.Rows), terminal.UTF8), nil
		}),
	})
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func TestContextSessionGatewayIdentityLessDenied(t *testing.T) {
	manager := newGateManager(t)
	registered, err := manager.RegisterRemoteSession(context.Background(), session.RemoteSessionOptions{
		OwnerID: "u1", Title: "gate", AssetID: "asset-gate", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	deps := WithSession(Dependencies{}, manager, nil, nil)

	// Gateway shape: identity-less, no trusted mark, server account mode.
	ctx := context.Background()
	if _, err := deps.Screen(ctx, registered.ID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("screen err = %v", err)
	}
	if _, err := deps.Tail(ctx, registered.ID, 10); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("tail err = %v", err)
	}
	if _, err := deps.Session(ctx, registered.SessionID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("session err = %v", err)
	}
	if _, err := deps.Transport(ctx, registered.SessionID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("transport err = %v", err)
	}

	// Trusted-open shape (desktop / explicit open deployment): elevated.
	marked := owner.WithOpenAccess(context.Background())
	if _, err := deps.Screen(marked, registered.ID); err != nil {
		t.Fatalf("marked screen err = %v", err)
	}
	if _, err := deps.Tail(marked, registered.ID, 10); err != nil {
		t.Fatalf("marked tail err = %v", err)
	}
	if brief, err := deps.Session(marked, registered.SessionID); err != nil || brief.Kind != session.KindRemote {
		t.Fatalf("marked session = %+v err=%v", brief, err)
	}

	// Cross-account: u2 never reads u1's tab; u1 reads it.
	if _, err := deps.Screen(ipc.WithUserID(context.Background(), "u2"), registered.ID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("cross-account screen err = %v", err)
	}
	if _, err := deps.Tail(ipc.WithUserID(context.Background(), "u2"), registered.ID, 10); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("cross-account tail err = %v", err)
	}
	if _, err := deps.Screen(ipc.WithUserID(context.Background(), "u1"), registered.ID); err != nil {
		t.Fatalf("owner screen err = %v", err)
	}
	if _, err := deps.Tail(ipc.WithUserID(context.Background(), "u1"), registered.ID, 10); err != nil {
		t.Fatalf("owner tail err = %v", err)
	}
}

func TestContextSessionGateRace(t *testing.T) {
	manager := newGateManager(t)
	registered, err := manager.RegisterRemoteSession(context.Background(), session.RemoteSessionOptions{
		OwnerID: "u1", Title: "gate-race", AssetID: "asset-gate-race", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	deps := WithSession(Dependencies{}, manager, nil, nil)
	marked := owner.WithOpenAccess(context.Background())
	ownerCtx := ipc.WithUserID(context.Background(), "u1")

	const racers = 8
	begin := make(chan struct{})
	var wg sync.WaitGroup
	denied := make(chan error, racers)
	allowed := make(chan error, racers*2)
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-begin
			if _, err := deps.Screen(context.Background(), registered.ID); err != nil {
				denied <- err
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-begin
			if _, err := deps.Screen(marked, registered.ID); err != nil {
				allowed <- err
			}
			if _, err := deps.Tail(ownerCtx, registered.ID, 10); err != nil {
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
			t.Fatalf("identity-less racer screen err = %v", err)
		}
	}
	for err := range allowed {
		t.Fatalf("trusted racer err = %v", err)
	}
}

func TestContextSessionRequestContextUnit(t *testing.T) {
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
