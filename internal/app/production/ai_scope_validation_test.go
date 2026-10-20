package production

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
)

type scopeConnectionValidator struct{}

func (scopeConnectionValidator) Tables(ctx context.Context, connID, _ string) ([]string, error) {
	if owner.OpenAccess(ctx) {
		return nil, nil
	}
	ownerID := ipc.OwnerID(ctx)
	switch connID {
	case "conn-a":
		if ownerID != "owner-a" {
			return nil, ipc.NewError(ipc.CodeForbidden, "foreign connection")
		}
	case "conn-b":
		if ownerID != "owner-b" {
			return nil, ipc.NewError(ipc.CodeForbidden, "foreign connection")
		}
	default:
		return nil, ipc.NewError(ipc.CodeNotFound, "missing connection")
	}
	return nil, nil
}

func TestProductionScopeValidatorTwoAccountCoverage(t *testing.T) {
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	sessions := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return local.NewWithConfig(local.Config{}), nil
		}),
		Terminals: session.TerminalFactoryFunc(func(session.TerminalConfig) (session.TerminalState, error) {
			return &scopeTestTerminal{}, nil
		}),
	})
	t.Cleanup(func() { _ = sessions.Close() })
	assetA, err := database.AssetCreate(ctxA, store.AssetInput{Kind: "local", Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	assetA2, err := database.AssetCreate(ctxA, store.AssetInput{Kind: "local", Name: "A2"})
	if err != nil {
		t.Fatal(err)
	}
	assetB, err := database.AssetCreate(ctxB, store.AssetInput{Kind: "local", Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	sessionA, err := sessions.Connect(ctxA, session.Asset{ID: assetA.ID, Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	sessionA2, err := sessions.Connect(ctxA, session.Asset{ID: assetA2.ID, Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	sessionB, err := sessions.Connect(ctxB, session.Asset{ID: assetB.ID, Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	tabA, err := sessions.CreateTabFor(ctxA, session.CreateTabOptions{SessionID: sessionA.ID})
	if err != nil {
		t.Fatal(err)
	}
	tabB, err := sessions.CreateTabFor(ctxB, session.CreateTabOptions{SessionID: sessionB.ID})
	if err != nil {
		t.Fatal(err)
	}
	validator := productionScopeValidatorWith(sessions, database, scopeConnectionValidator{}, false)
	for _, test := range []struct {
		name    string
		ctx     context.Context
		scope   tools.Scope
		wantErr bool
	}{
		{name: "own complete scope", ctx: ctxA, scope: tools.Scope{SessionID: sessionA.ID, TabID: tabA.ID, AssetID: assetA.ID, ConnID: "conn-a"}},
		{name: "foreign session", ctx: ctxA, scope: tools.Scope{SessionID: sessionB.ID}, wantErr: true},
		{name: "foreign tab", ctx: ctxA, scope: tools.Scope{TabID: tabB.ID}, wantErr: true},
		{name: "same owner mismatched tab session", ctx: ctxA, scope: tools.Scope{SessionID: sessionA2.ID, TabID: tabA.ID}, wantErr: true},
		{name: "forged asset", ctx: ctxA, scope: tools.Scope{SessionID: sessionA.ID, AssetID: assetB.ID}, wantErr: true},
		{name: "foreign connection", ctx: ctxA, scope: tools.Scope{ConnID: "conn-b"}, wantErr: true},
		{name: "missing owner", ctx: context.Background(), scope: tools.Scope{}, wantErr: true},
		{name: "explicit open", ctx: owner.WithOpenAccess(context.Background()), scope: tools.Scope{SessionID: sessionA.ID, TabID: tabA.ID, AssetID: assetA.ID, ConnID: "conn-a"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validator.ValidateScope(test.ctx, test.scope)
			if test.wantErr && err == nil {
				t.Fatal("invalid scope was accepted")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("valid scope rejected: %v", err)
			}
		})
	}
}
