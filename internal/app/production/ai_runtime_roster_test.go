package production

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
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

	// The first attach binds handle-a to the live runtime episode; only a
	// bound row may report connected presence and real asset evidence.
	attach := dispatch("ai_roster_attach", `{"handle":"handle-a","jobId":"job-1"}`)
	if !attach.OK {
		t.Fatalf("ai_roster_attach = %+v", attach.Error)
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
	// A remote shell is not store-backed: its host is honestly unknown, and
	// no OSC 7 report ever reached the tab, so the directory is unknown too.
	if live.Host != "" || live.Cwd != "" {
		t.Fatalf("remote shell target evidence must stay unknown: %+v", live)
	}
	orphan := byHandle["handle-orphan"]
	if orphan.Presence != agent.RosterResolutionUnverifiable || orphan.AssetID != "" {
		t.Fatalf("orphan entry must stay honestly unknown: %+v", orphan)
	}
	if orphan.Host != "" || orphan.Cwd != "" {
		t.Fatalf("orphan target evidence must stay honestly unknown: %+v", orphan)
	}

	// Rename binds the exact owner identity: a stale identity conflicts, a
	// foreign owner never resolves the handle, and the saved identity renames
	// only the alias — the permanent handle and incarnation survive.
	stale := dispatch("ai_roster_rename", `{"handle":"handle-a","alias":"alias-after","createdAt":1,"incarnation":"stale"}`)
	if stale.OK || stale.Error == nil || stale.Error.Code != ipc.Code("conflict") {
		t.Fatalf("stale rename = %+v", stale)
	}
	foreignRename := dispatcher.Dispatch(ipc.WithUserID(context.Background(), "owner-b"), ipc.Request{Command: "ai_roster_rename", Args: json.RawMessage(`{"handle":"handle-a","alias":"alias-after","createdAt":` + jsonStamp(live.Entry.CreatedAt) + `,"incarnation":"` + live.Entry.Incarnation + `"}`)}, ipc.Environment{})
	if foreignRename.OK || foreignRename.Error == nil || foreignRename.Error.Code != ipc.CodeNotFound {
		t.Fatalf("foreign rename = %+v", foreignRename)
	}
	renamed := dispatch("ai_roster_rename", `{"handle":"handle-a","alias":"alias-after","createdAt":`+jsonStamp(live.Entry.CreatedAt)+`,"incarnation":"`+live.Entry.Incarnation+`"}`)
	if !renamed.OK {
		t.Fatalf("ai_roster_rename = %+v", renamed.Error)
	}
	var renamedEntry agent.RosterEntry
	if err := json.Unmarshal(renamed.Data, &renamedEntry); err != nil {
		t.Fatal(err)
	}
	if renamedEntry.Alias != "alias-after" || renamedEntry.Handle != "handle-a" || renamedEntry.Incarnation != live.Entry.Incarnation {
		t.Fatalf("renamed entry = %+v", renamedEntry)
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

func jsonStamp(value int64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// rosterProbeChannel is an emit-capable fake channel: the session pump reads
// injected runtime output from it exactly as it would from a real transport.
type rosterProbeChannel struct {
	closed   chan struct{}
	once     sync.Once
	incoming chan []byte
}

func newRosterProbeChannel() *rosterProbeChannel {
	return &rosterProbeChannel{closed: make(chan struct{}), incoming: make(chan []byte, 16)}
}

func (c *rosterProbeChannel) Read(p []byte) (int, error) {
	select {
	case data := <-c.incoming:
		return copy(p, data), nil
	case <-c.closed:
		return 0, io.EOF
	}
}

func (c *rosterProbeChannel) emit(data []byte) {
	select {
	case c.incoming <- data:
	case <-c.closed:
	}
}

func (c *rosterProbeChannel) Write(data []byte) (int, error) { return len(data), nil }
func (c *rosterProbeChannel) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}
func (c *rosterProbeChannel) CloseWrite() error                            { return nil }
func (c *rosterProbeChannel) Stderr() io.Reader                            { return strings.NewReader("") }
func (c *rosterProbeChannel) Resize(context.Context, uint32, uint32) error { return nil }
func (c *rosterProbeChannel) Wait(context.Context) error                   { <-c.closed; return nil }
func (c *rosterProbeChannel) ID() string                                   { return "roster-probe" }
func (c *rosterProbeChannel) Generation() uint64                           { return 1 }

type rosterProbeTransport struct {
	mu       sync.Mutex
	channels []*rosterProbeChannel
}

func (t *rosterProbeTransport) Kind() string       { return "ssh" }
func (t *rosterProbeTransport) Generation() uint64 { return 1 }
func (t *rosterProbeTransport) IsAlive() bool      { return true }
func (t *rosterProbeTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}
func (t *rosterProbeTransport) Close() error { return nil }
func (t *rosterProbeTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{}, nil
}
func (t *rosterProbeTransport) OpenPTY(context.Context, base.PTYOptions) (base.Channel, error) {
	channel := newRosterProbeChannel()
	t.mu.Lock()
	t.channels = append(t.channels, channel)
	t.mu.Unlock()
	return channel, nil
}

func (t *rosterProbeTransport) lastChannel() *rosterProbeChannel {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.channels[len(t.channels)-1]
}

// TestComposeAIRuntimeRosterExposesVerifiableHostAndCwd proves the production
// roster IPC surfaces host and working directory only from verifiable
// snapshots — the bound store asset's configured host and the tab's own OSC 7
// report — and that a reconnect replacement flips presence to replaced while
// the evidence fails closed, and a row-less handle stays honestly
// unverifiable instead of fabricating a nonpersistent attachment.
func TestComposeAIRuntimeRosterExposesVerifiableHostAndCwd(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	profileManager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	transport := &rosterProbeTransport{}
	sessions := session.NewManager(session.Config{
		IdleTimeout: -1,
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return transport, nil
		}),
		ReconnectMax:     10,
		ReconnectBackoff: []time.Duration{0},
	})
	services := &ProductionServices{Store: database, Profiles: profileManager, Sessions: sessions, desktop: false}
	if err := composeAIRuntime(ctx, services); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		services.closeAIRuntime()
		_ = sessions.Close()
		_ = database.Close()
	})

	host := "prod.example"
	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "prod-host", Host: &host})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := sessions.ConnectFor(ctx, session.Asset{ID: asset.ID, Name: asset.Name, Kind: session.KindSSH, StoreBacked: true})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := sessions.OpenTab(ctx, session.OpenTabOptions{SessionID: connected.Info().ID, ClientID: "client", ChannelID: "channel", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	// The runtime's own OSC 7 report lands through the real pump path.
	transport.lastChannel().emit([]byte("\x1b]7;file://prod/srv/app\x07"))
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot, ok := sessions.RosterRuntimeSnapshot(ctx, tab.ID)
		if ok && snapshot.Cwd == "/srv/app" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("snapshot never carried the OSC 7 cwd")
		}
		time.Sleep(10 * time.Millisecond)
	}

	dispatcher := ipc.NewDispatcher()
	if err := services.Agent.RegisterRosterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	dispatch := func(command, args string) ipc.Response {
		t.Helper()
		return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
	}

	if _, err := database.AgentRosterProvision(ctx, store.AgentRosterRow{
		ConversationID: "conv-1", Handle: "handle-a", Alias: "agent-a", TabID: tab.ID,
		State: string(agent.RosterStateProvisioning), Incarnation: "inc-1",
	}); err != nil {
		t.Fatal(err)
	}
	attach := dispatch("ai_roster_attach", `{"handle":"handle-a","jobId":"job-1"}`)
	if !attach.OK {
		t.Fatalf("ai_roster_attach = %+v", attach.Error)
	}
	var attachment agent.RosterAttachmentDTO
	if err := json.Unmarshal(attach.Data, &attachment); err != nil {
		t.Fatal(err)
	}
	if attachment.Host != "prod.example" || attachment.Cwd != "/srv/app" {
		t.Fatalf("attach target evidence = %q/%q, want the store asset host and OSC 7 cwd", attachment.Host, attachment.Cwd)
	}

	list := dispatch("ai_roster_list", `{"conversationId":"conv-1"}`)
	if !list.OK {
		t.Fatalf("ai_roster_list = %+v", list.Error)
	}
	var listed []agent.RosterListEntry
	if err := json.Unmarshal(list.Data, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Host != "prod.example" || listed[0].Cwd != "/srv/app" {
		t.Fatalf("listed = %+v", listed)
	}

	// A committed reconnect replaces the runtime episode: presence flips to
	// replaced and the target evidence fails closed instead of answering from
	// the replacement runtime.
	if err := sessions.Reconnect(ctx, connected.Info().ID); err != nil {
		t.Fatal(err)
	}
	list = dispatch("ai_roster_list", `{"conversationId":"conv-1"}`)
	if !list.OK {
		t.Fatalf("post-reconnect ai_roster_list = %+v", list.Error)
	}
	if err := json.Unmarshal(list.Data, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Presence != agent.RosterResolutionReplaced {
		t.Fatalf("post-reconnect listed = %+v, want replaced presence", listed)
	}
	if listed[0].Host != "" || listed[0].Cwd != "" || listed[0].AssetID != "" {
		t.Fatalf("post-reconnect evidence must fail closed: %+v", listed[0])
	}

	// A row-less handle never fabricates a nonpersistent attachment: the
	// production probe cannot evidence a runtime for it, so the resolution is
	// honestly unverifiable and nothing is persisted.
	unknown := dispatch("ai_roster_attach", `{"handle":"handle-unknown","jobId":"job-2"}`)
	if !unknown.OK {
		t.Fatalf("unknown-handle ai_roster_attach = %+v", unknown.Error)
	}
	var unknownAttachment agent.RosterAttachmentDTO
	if err := json.Unmarshal(unknown.Data, &unknownAttachment); err != nil {
		t.Fatal(err)
	}
	if unknownAttachment.Resolution != agent.RosterResolutionUnverifiable {
		t.Fatalf("unknown-handle resolution = %s, want unverifiable", unknownAttachment.Resolution)
	}
	if rows, err := database.AgentRosterList(ctx, ""); err != nil || len(rows) != 1 {
		t.Fatalf("unknown-handle attach persisted rows: %+v err=%v", rows, err)
	}
}
