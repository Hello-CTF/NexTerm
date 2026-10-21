package production

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// TestComposeAIRuntimeWiresRosterWithRealAssetEvidence proves the production
// composition carries the M74 roster with a session-backed runtime probe: the
// roster IPC resolves the real target asset and live presence from server-side
// session state, never from client claims, and a row whose runtime is absent
// stays honestly unknown instead of being classified gone.
func TestComposeAIRuntimeWiresRosterWithRealAssetEvidence(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	profileManager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager(session.Config{IdleTimeout: -1})
	services := &ProductionServices{Store: database, Profiles: profileManager, Sessions: sessions, desktop: false}
	if err := composeAIRuntime(ctx, services); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		services.closeAIRuntime()
		_ = sessions.Close()
		_ = database.Close()
	})

	tab, err := sessions.RegisterRemoteSession(ctx, session.RemoteSessionOptions{OwnerID: "owner-a", AssetID: "asset-1", Title: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.AgentRosterProvision(ctx, store.AgentRosterRow{
		ConversationID: "conv-1", Handle: "handle-a", Alias: "agent-a", TabID: tab.ID,
		State: string(agent.RosterStateProvisioning), Incarnation: "inc-1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AgentRosterProvision(ctx, store.AgentRosterRow{
		ConversationID: "conv-1", Handle: "handle-orphan", TabID: "tab-never-created",
		State: string(agent.RosterStateProvisioning), Incarnation: "inc-2",
	}); err != nil {
		t.Fatal(err)
	}

	dispatcher := ipc.NewDispatcher()
	if err := services.Agent.RegisterRosterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	dispatch := func(command, args string) ipc.Response {
		t.Helper()
		return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
	}

	response := dispatch("ai_roster_list", `{"conversationId":"conv-1"}`)
	if !response.OK {
		t.Fatalf("ai_roster_list = %+v", response.Error)
	}
	var listed []agent.RosterListEntry
	if err := json.Unmarshal(response.Data, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed = %+v", listed)
	}
	byHandle := map[string]agent.RosterListEntry{}
	for _, entry := range listed {
		byHandle[entry.Entry.Handle] = entry
	}
	live := byHandle["handle-a"]
	if live.Presence != agent.RosterResolutionConnected || live.AssetID != "asset-1" {
		t.Fatalf("live entry evidence = %+v", live)
	}
	orphan := byHandle["handle-orphan"]
	if orphan.Presence != agent.RosterResolutionUnverifiable || orphan.AssetID != "" {
		t.Fatalf("orphan entry must stay honestly unknown: %+v", orphan)
	}

	// Cross-owner dispatch never observes the roster.
	foreign := dispatcher.Dispatch(ipc.WithUserID(context.Background(), "owner-b"), ipc.Request{Command: "ai_roster_list", Args: json.RawMessage(`{"conversationId":"conv-1"}`)}, ipc.Environment{})
	if !foreign.OK {
		t.Fatalf("foreign ai_roster_list = %+v", foreign.Error)
	}
	var foreignListed []agent.RosterListEntry
	if err := json.Unmarshal(foreign.Data, &foreignListed); err != nil {
		t.Fatal(err)
	}
	if len(foreignListed) != 0 {
		t.Fatalf("foreign owner listed %+v", foreignListed)
	}
}
