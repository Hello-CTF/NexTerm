package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func rosterIPCDispatch(t *testing.T, runner *Runner, ctx context.Context, command, args string) ipc.Response {
	t.Helper()
	dispatcher := ipc.NewDispatcher()
	if err := runner.RegisterRosterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
}

func TestRosterIPCListCarriesRealPresenceAndAssetEvidence(t *testing.T) {
	database, manager := newRosterProbeFixture(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	tab, err := manager.RegisterRemoteSession(ctx, session.RemoteSessionOptions{OwnerID: "owner-a", AssetID: "asset-1", Title: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	roster := NewRoster(database, NewSessionRosterProbe(database, manager))
	if _, err := roster.Provision(ctx, "conv-1", "handle-a", "agent-a", tab.ID, nil); err != nil {
		t.Fatal(err)
	}
	// The first attach binds the row to the live runtime episode; only a
	// bound row may report connected presence and real asset evidence.
	if _, err := roster.Attach(ctx, "handle-a", "job-1"); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{Store: database, Roster: roster})
	t.Cleanup(func() { _ = runner.Close() })

	response := rosterIPCDispatch(t, runner, ctx, "ai_roster_list", `{"conversationId":"conv-1"}`)
	if !response.OK {
		t.Fatalf("ai_roster_list = %+v", response.Error)
	}
	var listed []RosterListEntry
	if err := json.Unmarshal(response.Data, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed = %+v", listed)
	}
	entry := listed[0]
	if entry.Entry.Handle != "handle-a" || entry.Entry.Alias != "agent-a" || entry.Entry.TabID != tab.ID {
		t.Fatalf("entry = %+v", entry.Entry)
	}
	if entry.Presence != RosterResolutionConnected {
		t.Fatalf("presence = %q, want the real connected runtime", entry.Presence)
	}
	if entry.AssetID != "asset-1" {
		t.Fatalf("asset evidence = %q, want the real target asset", entry.AssetID)
	}

	// A second owner never sees the foreign roster.
	foreign := rosterIPCDispatch(t, runner, ipc.WithUserID(context.Background(), "owner-b"), "ai_roster_list", `{"conversationId":"conv-1"}`)
	if !foreign.OK {
		t.Fatalf("foreign ai_roster_list = %+v", foreign.Error)
	}
	var foreignListed []RosterListEntry
	if err := json.Unmarshal(foreign.Data, &foreignListed); err != nil {
		t.Fatal(err)
	}
	if len(foreignListed) != 0 {
		t.Fatalf("foreign owner listed %+v", foreignListed)
	}
}

func TestRosterIPCAttachBindsJobAndReleaseBindsExactIdentity(t *testing.T) {
	database, manager := newRosterProbeFixture(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	tab, err := manager.RegisterRemoteSession(ctx, session.RemoteSessionOptions{OwnerID: "owner-a", AssetID: "asset-1", Title: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	roster := NewRoster(database, NewSessionRosterProbe(database, manager))
	provisioned, err := roster.Provision(ctx, "conv-1", "handle-a", "", tab.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{Store: database, Roster: roster})
	t.Cleanup(func() { _ = runner.Close() })

	attached := rosterIPCDispatch(t, runner, ctx, "ai_roster_attach", `{"handle":"handle-a","jobId":"job-1"}`)
	if !attached.OK {
		t.Fatalf("ai_roster_attach = %+v", attached.Error)
	}
	var attachment RosterAttachmentDTO
	if err := json.Unmarshal(attached.Data, &attachment); err != nil {
		t.Fatal(err)
	}
	if attachment.Resolution != RosterResolutionConnected || attachment.Entry.JobID != "job-1" {
		t.Fatalf("attachment = %+v", attachment)
	}
	if attachment.AssetID != "asset-1" {
		t.Fatalf("attach asset evidence = %q, want the real target asset", attachment.AssetID)
	}

	// Release with a stale durable identity is a conflict, never a write.
	stale := rosterIPCDispatch(t, runner, ctx, "ai_roster_release", `{"handle":"handle-a","createdAt":`+jsonNumber(provisioned.CreatedAt)+`,"incarnation":"stale"}`)
	if stale.OK || stale.Error == nil || stale.Error.Code != ipc.Code("conflict") {
		t.Fatalf("stale release = %+v", stale)
	}
	// The caller's own saved identity releases the entry.
	released := rosterIPCDispatch(t, runner, ctx, "ai_roster_release", `{"handle":"handle-a","createdAt":`+jsonNumber(provisioned.CreatedAt)+`,"incarnation":"`+provisioned.Incarnation+`"}`)
	if !released.OK {
		t.Fatalf("ai_roster_release = %+v", released.Error)
	}
	var entry RosterEntry
	if err := json.Unmarshal(released.Data, &entry); err != nil {
		t.Fatal(err)
	}
	if entry.State != RosterStateReleasePending {
		t.Fatalf("released entry = %+v", entry)
	}
}

func jsonNumber(value int64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestRosterIPCReconcileReportsWithoutWritesForLiveRows(t *testing.T) {
	database, manager := newRosterProbeFixture(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	tab, err := manager.RegisterRemoteSession(ctx, session.RemoteSessionOptions{OwnerID: "owner-a", AssetID: "asset-1", Title: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	roster := NewRoster(database, NewSessionRosterProbe(database, manager))
	if _, err := roster.Provision(ctx, "conv-1", "handle-a", "", tab.ID, nil); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{Store: database, Roster: roster})
	t.Cleanup(func() { _ = runner.Close() })

	response := rosterIPCDispatch(t, runner, ctx, "ai_roster_reconcile", `{"conversationId":"conv-1"}`)
	if !response.OK {
		t.Fatalf("ai_roster_reconcile = %+v", response.Error)
	}
	var report RosterReconcileReport
	if err := json.Unmarshal(response.Data, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 1 || report.Entries[0].Outcome != RosterResolutionConnected || report.Entries[0].Wrote {
		t.Fatalf("reconcile report = %+v", report)
	}
}

func TestRosterIPCWithoutRosterIsUnsupported(t *testing.T) {
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runner := NewRunner(Config{Store: database})
	t.Cleanup(func() { _ = runner.Close() })

	ctx := ipc.WithUserID(context.Background(), "owner-a")
	response := rosterIPCDispatch(t, runner, ctx, "ai_roster_list", `{}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("ai_roster_list without roster = %+v", response)
	}
}

func TestRosterIPCRenameBindsExactOwnerIdentity(t *testing.T) {
	database, manager := newRosterProbeFixture(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	tab, err := manager.RegisterRemoteSession(ctx, session.RemoteSessionOptions{OwnerID: "owner-a", AssetID: "asset-1", Title: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	roster := NewRoster(database, NewSessionRosterProbe(database, manager))
	provisioned, err := roster.Provision(ctx, "conv-1", "handle-a", "alias-before", tab.ID, map[string]any{"origin": "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{Store: database, Roster: roster})
	t.Cleanup(func() { _ = runner.Close() })

	// A stale durable identity is a conflict, never a write.
	stale := rosterIPCDispatch(t, runner, ctx, "ai_roster_rename", `{"handle":"handle-a","alias":"alias-after","createdAt":`+jsonNumber(provisioned.CreatedAt)+`,"incarnation":"stale"}`)
	if stale.OK || stale.Error == nil || stale.Error.Code != ipc.Code("conflict") {
		t.Fatalf("stale rename = %+v", stale)
	}
	// A foreign owner never resolves the handle.
	foreign := rosterIPCDispatch(t, runner, ipc.WithUserID(context.Background(), "owner-b"), "ai_roster_rename", `{"handle":"handle-a","alias":"alias-after","createdAt":`+jsonNumber(provisioned.CreatedAt)+`,"incarnation":"`+provisioned.Incarnation+`"}`)
	if foreign.OK || foreign.Error == nil || foreign.Error.Code != ipc.CodeNotFound {
		t.Fatalf("foreign rename = %+v", foreign)
	}

	// The caller's own saved identity renames: only the alias changes.
	renamed := rosterIPCDispatch(t, runner, ctx, "ai_roster_rename", `{"handle":"handle-a","alias":"alias-after","createdAt":`+jsonNumber(provisioned.CreatedAt)+`,"incarnation":"`+provisioned.Incarnation+`"}`)
	if !renamed.OK {
		t.Fatalf("ai_roster_rename = %+v", renamed.Error)
	}
	var entry RosterEntry
	if err := json.Unmarshal(renamed.Data, &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Alias != "alias-after" || entry.Handle != "handle-a" || entry.Incarnation != provisioned.Incarnation {
		t.Fatalf("renamed entry = %+v", entry)
	}
	stored, err := database.AgentRosterGetByHandle(ctx, "handle-a")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Alias != "alias-after" || !strings.Contains(stored.MetadataJSON, `"origin":"synthetic"`) {
		t.Fatalf("stored row = %+v", stored)
	}

	// The roster-free runner answers unsupported rather than inventing a row.
	bare, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bare.Close() })
	bareRunner := NewRunner(Config{Store: bare})
	t.Cleanup(func() { _ = bareRunner.Close() })
	unsupported := rosterIPCDispatch(t, bareRunner, ctx, "ai_roster_rename", `{"handle":"handle-a","alias":"x","createdAt":1,"incarnation":"i"}`)
	if unsupported.OK || unsupported.Error == nil || unsupported.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("ai_roster_rename without roster = %+v", unsupported)
	}
}

// The roster list and attach responses carry the verifiable host and working
// directory from server-side snapshots: the configured store asset host and
// the tab's own OSC 7 report — never the asset name or a fabricated path.
func TestRosterIPCCarriesHostAndCwdEvidence(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "prod-host", Host: rosterStringPtr("prod.example")})
	if err != nil {
		t.Fatal(err)
	}
	transport := &probeFakeTransport{}
	manager := session.NewManager(session.Config{
		IdleTimeout: -1,
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return transport, nil
		}),
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: asset.ID, Name: asset.Name, Kind: session.KindSSH, StoreBacked: true})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.OpenTab(ctx, session.OpenTabOptions{SessionID: connected.Info().ID, ClientID: "client", ChannelID: "channel", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	transport.lastChannel().emit([]byte("\x1b]7;file://prod/srv/app\x07"))
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot, ok := manager.RosterRuntimeSnapshot(ctx, tab.ID)
		if ok && snapshot.Cwd == "/srv/app" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("snapshot never carried the OSC 7 cwd")
		}
		time.Sleep(10 * time.Millisecond)
	}
	roster := NewRoster(database, NewSessionRosterProbe(database, manager))
	if _, err := roster.Provision(ctx, "conv-1", "handle-a", "agent-a", tab.ID, nil); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{Store: database, Roster: roster})
	t.Cleanup(func() { _ = runner.Close() })

	attached := rosterIPCDispatch(t, runner, ctx, "ai_roster_attach", `{"handle":"handle-a","jobId":"job-1"}`)
	if !attached.OK {
		t.Fatalf("ai_roster_attach = %+v", attached.Error)
	}
	var attachment RosterAttachmentDTO
	if err := json.Unmarshal(attached.Data, &attachment); err != nil {
		t.Fatal(err)
	}
	if attachment.Host != "prod.example" || attachment.Cwd != "/srv/app" {
		t.Fatalf("attach target evidence = %q/%q", attachment.Host, attachment.Cwd)
	}

	listed := rosterIPCDispatch(t, runner, ctx, "ai_roster_list", `{"conversationId":"conv-1"}`)
	if !listed.OK {
		t.Fatalf("ai_roster_list = %+v", listed.Error)
	}
	var entries []RosterListEntry
	if err := json.Unmarshal(listed.Data, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("listed = %+v", entries)
	}
	if entries[0].Host != "prod.example" || entries[0].Cwd != "/srv/app" {
		t.Fatalf("list target evidence = %q/%q", entries[0].Host, entries[0].Cwd)
	}

	// A foreign owner never observes the evidence.
	foreignListed := rosterIPCDispatch(t, runner, ipc.WithUserID(context.Background(), "owner-b"), "ai_roster_list", `{"conversationId":"conv-1"}`)
	if !foreignListed.OK {
		t.Fatalf("foreign ai_roster_list = %+v", foreignListed.Error)
	}
	var foreignEntries []RosterListEntry
	if err := json.Unmarshal(foreignListed.Data, &foreignEntries); err != nil {
		t.Fatal(err)
	}
	if len(foreignEntries) != 0 {
		t.Fatalf("foreign owner listed %+v", foreignEntries)
	}
}
