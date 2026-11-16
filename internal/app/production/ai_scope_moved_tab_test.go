package production

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
)

// A 持久终端 tab keeps its ID when it reattaches after a restart or from
// another machine, but lands under a new session. A conversation bound to the
// old session must follow the tab instead of refusing every later message.
func TestPersistedScopeFollowsTabMovedToNewSession(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
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
			return scopeForeignTabTerminal{}, nil
		}),
	})
	t.Cleanup(func() { _ = sessions.Close() })
	asset, err := createLocalFixtureAsset(ctx, database, store.AssetInput{Kind: "local", Name: "host"})
	if err != nil {
		t.Fatal(err)
	}
	oldSession, err := sessions.Connect(ctx, session.Asset{ID: asset.ID, Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.Disconnect(oldSession.ID); err != nil {
		t.Fatal(err)
	}
	newSession, err := sessions.Connect(ctx, session.Asset{ID: asset.ID, Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	if newSession.ID == oldSession.ID {
		t.Fatal("test setup reused the disconnected session")
	}
	tab, err := sessions.CreateTabFor(ctx, session.CreateTabOptions{SessionID: newSession.ID})
	if err != nil {
		t.Fatal(err)
	}
	stale := tools.Scope{SessionID: oldSession.ID, TabID: tab.ID, AssetID: asset.ID}
	conversation, err := database.ConvCreate(ctx, "moved", map[string]any{"scope": stale})
	if err != nil {
		t.Fatal(err)
	}
	resolver := productionScopeValidatorWith(sessions, database, nil, false)

	if _, err := resolver.ResolveScope(ctx, "", stale); err == nil {
		t.Fatal("a new conversation accepted a scope whose session and tab disagree")
	}

	resolved, err := resolver.ResolveScope(ctx, conversation.ID, stale)
	if err != nil {
		t.Fatalf("persisted scope with moved tab: %v", err)
	}
	want := tools.Scope{SessionID: newSession.ID, TabID: tab.ID, AssetID: asset.ID}
	if resolved != want {
		t.Fatalf("resolved = %+v, want %+v", resolved, want)
	}
	row, err := database.ConvGet(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := scopeFromConversationRow(row)
	if err != nil {
		t.Fatal(err)
	}
	if persisted != want {
		t.Fatalf("persisted scope = %+v, want %+v", persisted, want)
	}
}
