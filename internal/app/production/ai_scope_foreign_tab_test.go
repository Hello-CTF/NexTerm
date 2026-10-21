package production

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
)

type scopeForeignTabTerminal struct{}

func (scopeForeignTabTerminal) StopRecording() (uint64, error) { return 0, nil }
func (scopeForeignTabTerminal) RecordingStatus() terminal.RecordingStatus {
	return terminal.RecordingStatus{}
}
func (scopeForeignTabTerminal) Feed([]byte)                     {}
func (scopeForeignTabTerminal) Dump(int) []byte                 { return nil }
func (scopeForeignTabTerminal) Resize(int, int) error           { return nil }
func (scopeForeignTabTerminal) SetResponseHandler(func([]byte)) {}
func (scopeForeignTabTerminal) Close()                          {}

func TestScopeRebindRejectsForeignTabWithOwnAssetR2(t *testing.T) {
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	sessions := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			// The platform default shell keeps this fixture working on every
			// supported OS; the shell choice is incidental to the scope
			// validation under test.
			return local.NewWithConfig(local.Config{}), nil
		}),
		Terminals: session.TerminalFactoryFunc(func(session.TerminalConfig) (session.TerminalState, error) {
			return scopeForeignTabTerminal{}, nil
		}),
	})
	t.Cleanup(func() { _ = sessions.Close() })
	assetA, err := database.AssetCreate(ctxA, store.AssetInput{Kind: "local", Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	assetB, err := database.AssetCreate(ctxB, store.AssetInput{Kind: "local", Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Connect(ctxA, session.Asset{ID: assetA.ID, Kind: session.KindLocal}); err != nil {
		t.Fatal(err)
	}
	sessionB, err := sessions.Connect(ctxB, session.Asset{ID: assetB.ID, Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	tabB, err := sessions.CreateTabFor(ctxB, session.CreateTabOptions{SessionID: sessionB.ID})
	if err != nil {
		t.Fatal(err)
	}

	_, err = productionScopeValidatorWith(sessions, database, nil, false).ResolveScope(ctxA, "", tools.Scope{
		SessionID: "missing-session", TabID: tabB.ID, AssetID: assetA.ID,
	})
	if !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("foreign tab with own asset and healthy candidate error = %v, want ErrForbidden", err)
	}
}
