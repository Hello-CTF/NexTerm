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
// identity-less caller is never elevated to open access — the session layer
// natively denies it on non-open servers — and only a vouched identity (an
// identified owner carrying the app's mark) keeps open-access semantics.
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
	if _, err := deps.TabDetail(ctx, registered.ID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("tab detail err = %v", err)
	}
	if _, err := deps.Session(ctx, registered.SessionID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("session err = %v", err)
	}
	if _, err := deps.Transport(ctx, registered.SessionID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("transport err = %v", err)
	}

	// An identity-less mark is a vouch, not an authorization: denied on a
	// non-open server.
	marked := owner.WithOpenAccess(context.Background())
	if _, err := deps.Screen(marked, registered.ID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("marked screen err = %v", err)
	}
	if _, err := deps.Tail(marked, registered.ID, 10); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("marked tail err = %v", err)
	}
	if _, err := deps.TabDetail(marked, registered.ID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("marked tab detail err = %v", err)
	}
	if _, err := deps.Session(marked, registered.SessionID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("marked session err = %v", err)
	}

	// A vouched identity (the owner carrying the app's mark) keeps open
	// access.
	vouched := owner.WithOpenAccess(ipc.WithUserID(context.Background(), "u1"))
	if _, err := deps.Screen(vouched, registered.ID); err != nil {
		t.Fatalf("vouched screen err = %v", err)
	}
	if _, err := deps.Tail(vouched, registered.ID, 10); err != nil {
		t.Fatalf("vouched tail err = %v", err)
	}
	if detail, err := deps.TabDetail(vouched, registered.ID); err != nil || detail.TabID != registered.ID {
		t.Fatalf("vouched tab detail = %+v err=%v", detail, err)
	}
	if brief, err := deps.Session(vouched, registered.SessionID); err != nil || brief.Kind != session.KindRemote {
		t.Fatalf("vouched session = %+v err=%v", brief, err)
	}

	// Cross-account: u2 never reads u1's tab; u1 reads it.
	if _, err := deps.Screen(ipc.WithUserID(context.Background(), "u2"), registered.ID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("cross-account screen err = %v", err)
	}
	if _, err := deps.Tail(ipc.WithUserID(context.Background(), "u2"), registered.ID, 10); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("cross-account tail err = %v", err)
	}
	if _, err := deps.TabDetail(ipc.WithUserID(context.Background(), "u2"), registered.ID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("cross-account tab detail err = %v", err)
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
	denied := make(chan error, racers*2)
	allowed := make(chan error, racers)
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
				denied <- err
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
	if got := sessionRequestContext(marked); got != marked {
		t.Fatal("identity-less mark must not be elevated")
	}
	identified := ipc.WithUserID(base, "u1")
	if got := sessionRequestContext(identified); got != identified {
		t.Fatal("identified context must pass through unchanged")
	}
	vouched := owner.WithOpenAccess(identified)
	if got := sessionRequestContext(vouched); got == vouched {
		t.Fatal("vouched identity must elevate to open access")
	}
}
