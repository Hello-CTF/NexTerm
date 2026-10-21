package production

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// connectFakeChannel is the minimal PTY channel a local tab needs: reads
// block until close (the tab stays live for the whole test), writes are
// accepted and forgotten.
type connectFakeChannel struct {
	id     string
	closed chan struct{}
	once   sync.Once
}

func newConnectFakeChannel(id string) *connectFakeChannel {
	return &connectFakeChannel{id: id, closed: make(chan struct{})}
}

func (c *connectFakeChannel) Read(buffer []byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *connectFakeChannel) Write(data []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, base.ErrClosed
	default:
	}
	return len(data), nil
}

func (c *connectFakeChannel) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *connectFakeChannel) Stderr() io.Reader                            { return strings.NewReader("") }
func (c *connectFakeChannel) Resize(context.Context, uint32, uint32) error { return nil }
func (c *connectFakeChannel) Wait(ctx context.Context) error {
	select {
	case <-c.closed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *connectFakeChannel) CloseWrite() error  { return nil }
func (c *connectFakeChannel) ID() string         { return c.id }
func (c *connectFakeChannel) Generation() uint64 { return 1 }

// connectFakeTransport serves both the exec path and PTY-backed local tab
// creation; every channel is tracked so Close tears the runtime down, and
// the alive flag lets tests flip a live session's transport dead.
type connectFakeTransport struct {
	mu       sync.Mutex
	channels []*connectFakeChannel
	opens    int
	alive    atomic.Bool
}

func newConnectFakeTransport() *connectFakeTransport {
	transport := &connectFakeTransport{}
	transport.alive.Store(true)
	return transport
}

func (t *connectFakeTransport) Kind() string       { return session.KindLocal }
func (t *connectFakeTransport) Generation() uint64 { return 1 }
func (t *connectFakeTransport) IsAlive() bool      { return t.alive.Load() }
func (t *connectFakeTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}
func (t *connectFakeTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	code := 0
	return base.ExecResult{ExitCode: &code}, nil
}
func (t *connectFakeTransport) OpenPTY(context.Context, base.PTYOptions) (base.Channel, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.opens++
	channel := newConnectFakeChannel(t.Kind() + "-pty")
	t.channels = append(t.channels, channel)
	return channel, nil
}
func (t *connectFakeTransport) Close() error {
	t.alive.Store(false)
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, channel := range t.channels {
		_ = channel.Close()
	}
	return nil
}

// connectTransportLog records every transport the harness dial produced, in
// order, so tests can kill a specific session's transport and count dials.
type connectTransportLog struct {
	mu   sync.Mutex
	list []*connectFakeTransport
}

func (l *connectTransportLog) add(transport *connectFakeTransport) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.list = append(l.list, transport)
}

func (l *connectTransportLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.list)
}

func (l *connectTransportLog) at(index int) *connectFakeTransport {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.list[index]
}

// The production terminal_create connect=true flow: the AI terminal surface
// has its own per-user opt-in, every R-07 gate applies before the strict
// host-key dial, and the recorded outcome adjudicates and replays across a
// full runtime restart instead of reconnecting.

// composeConnectRuntime builds a file-backed production runtime with real
// tab support; reopening the same path with a fresh session manager leaves
// only the durable rows carrying over, which is the restart boundary. Every
// dial is recorded in the log.
func composeConnectRuntime(t *testing.T, ctx context.Context, path string, dials *connectTransportLog) (*ProductionServices, *store.Store, string) {
	t.Helper()
	database, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	assets, err := database.AssetList(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	assetID := ""
	if len(assets) == 0 {
		asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: session.KindLocal, Name: "workstation"})
		if err != nil {
			t.Fatal(err)
		}
		assetID = asset.ID
	} else {
		assetID = assets[0].ID
	}
	initTestVault(t, ctx, database)
	profileManager, err := profiles.NewManager(ctx, database, profiles.WithShared(true))
	if err != nil {
		t.Fatal(err)
	}
	script := &outcomeScriptServer{command: "go"}
	providerServer := httptest.NewServer(script)
	t.Cleanup(providerServer.Close)
	if _, err := profileManager.Save(ctx, profiles.Profile{
		BaseURL: providerServer.URL, APIKey: "test-key", Model: "test-model",
		ContextWindow: 32768, Stream: true,
	}); err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager(session.Config{
		OpenAccess: true,
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			transport := newConnectFakeTransport()
			dials.add(transport)
			return transport, nil
		}),
		Terminals: session.TerminalFactoryFunc(func(config session.TerminalConfig) (session.TerminalState, error) {
			return terminal.NewTab(config.TabID, config.SessionID, int(config.Cols), int(config.Rows), terminal.UTF8), nil
		}),
	})
	services := &ProductionServices{Store: database, Profiles: profileManager, Sessions: sessions, desktop: true}
	if err := composeAIRuntime(ctx, services); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		services.closeAIRuntime()
		_ = sessions.Close()
		_ = database.Close()
	})
	return services, database, assetID
}

// enableAiTerminalConnectOptIn turns the AI terminal connect opt-in on
// through the public production_settings_set IPC — the same path a user
// takes — never by writing the store directly.
func enableAiTerminalConnectOptIn(t *testing.T, ctx context.Context, database *store.Store) {
	t.Helper()
	dispatcher := ipc.NewDispatcher()
	if err := registerProductionSettingsCommands(dispatcher, database); err != nil {
		t.Fatal(err)
	}
	enabled := true
	response := dispatchExecBatchTest(dispatcher, ctx, "production_settings_set", productionSettingsUpdateRequest{AiTerminalConnectOptIn: &enabled})
	if !response.OK {
		t.Fatalf("production_settings_set = %+v", response)
	}
}

func connectCreateCall(assetID string) tools.Call {
	return tools.Call{ID: "c", Name: "terminal_create",
		Args: json.RawMessage(`{"asset_id":"` + assetID + `","connect":true}`), AuthorizationID: "guard:test"}
}

func TestProductionTerminalConnectRequiresOptIn(t *testing.T) {
	ctx := context.Background()
	dials := &connectTransportLog{}
	services, _, assetID := composeConnectRuntime(t, ctx, filepath.Join(t.TempDir(), "connect.db"), dials)

	result := services.Agent.ToolsRegistry().Execute(ctx, "job", tools.Scope{}, connectCreateCall(assetID), nil)
	if result.OK {
		t.Fatalf("connect without opt-in succeeded: %+v", result)
	}
	if strings.Contains(result.Text, "未配置资产连接建立") {
		t.Fatalf("connect surface left as an unconfigured boundary: %+v", result)
	}
	if !strings.Contains(result.Text, "opt-in") {
		t.Fatalf("opt-in decline not surfaced: %+v", result)
	}
	if got := dials.count(); got != 0 {
		t.Fatalf("declined connect dialed %d times", got)
	}
}

func TestProductionTerminalConnectRecordsDurableOutcome(t *testing.T) {
	ctx := context.Background()
	dials := &connectTransportLog{}
	services, database, assetID := composeConnectRuntime(t, ctx, filepath.Join(t.TempDir(), "connect.db"), dials)
	enableAiTerminalConnectOptIn(t, ctx, database)

	registry := services.Agent.ToolsRegistry()
	result := registry.Execute(ctx, "job", tools.Scope{}, connectCreateCall(assetID), nil)
	if !result.OK {
		t.Fatalf("gated connect create = %+v", result)
	}
	if result.OperationID == "" {
		t.Fatalf("connected create lost its operation ID: %+v", result)
	}
	// The connect established a real session for the asset and the created
	// tab rides on it.
	var connectedSessionID string
	infos, err := services.Sessions.ListSessionsFor(session.WithOpenAccess(ctx))
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.AssetID == assetID {
			connectedSessionID = info.ID
		}
	}
	if connectedSessionID == "" {
		t.Fatal("gated connect established no session")
	}
	if result.CreatedTab == nil || result.CreatedTab.SessionID != connectedSessionID {
		t.Fatalf("created tab not on the connected session: %+v", result.CreatedTab)
	}
	// The durable owner-scoped record answers the result query with the
	// residual session and the recorded payload.
	query := registry.Execute(ctx, "job", tools.Scope{}, tools.Call{ID: "q", Name: "operation_result",
		Args: json.RawMessage(`{"operation_id":"` + result.OperationID + `"}`)}, nil)
	if !query.OK || !strings.Contains(query.Text, "succeeded") || !strings.Contains(query.Text, connectedSessionID) {
		t.Fatalf("operation_result = %+v", query)
	}
}

func TestProductionTerminalConnectOutcomeSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "connect.db")
	dials := &connectTransportLog{}
	services, database, assetID := composeConnectRuntime(t, ctx, path, dials)
	enableAiTerminalConnectOptIn(t, ctx, database)

	call := connectCreateCall(assetID)
	first := services.Agent.ToolsRegistry().Execute(ctx, "job", tools.Scope{}, call, nil)
	if !first.OK || first.OperationID == "" {
		t.Fatalf("first connected create = %+v", first)
	}
	if got := dials.count(); got != 1 {
		t.Fatalf("first create dialed %d times", got)
	}
	// Close the whole runtime: sessions, AI runtime, and the store handle —
	// only durable rows survive.
	services.closeAIRuntime()
	_ = services.Sessions.Close()
	_ = database.Close()

	restored, _, restoredAssetID := composeConnectRuntime(t, ctx, path, dials)
	if restoredAssetID != assetID {
		t.Fatalf("asset identity changed across restart: %s vs %s", restoredAssetID, assetID)
	}
	registry := restored.Agent.ToolsRegistry()
	// The durable record answers the result query on the reopened store
	// before any retry.
	query := registry.Execute(ctx, "job", tools.Scope{}, tools.Call{ID: "q", Name: "operation_result",
		Args: json.RawMessage(`{"operation_id":"` + first.OperationID + `"}`)}, nil)
	if !query.OK || !strings.Contains(query.Text, "succeeded") {
		t.Fatalf("operation_result after restart = %+v", query)
	}
	// The retry adjudicates the finished record and replays instead of
	// reconnecting: the fresh session manager never dials.
	second := registry.Execute(ctx, "job", tools.Scope{}, call, nil)
	if !second.OK || !strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("post-restart retry did not replay: %+v", second)
	}
	if got := dials.count(); got != 1 {
		t.Fatalf("post-restart retry reconnected: dials=%d", got)
	}
}

// A resolved session whose transport died is the same definite not-connected
// verdict as having no live session: connect=true enters the R-07 gated
// connector (declined without the opt-in), and once the user enables the AI
// terminal opt-in through the public settings IPC the redial succeeds.
// Unknown or forbidden assets never reach the dialer.
func TestProductionTerminalConnectDeadTransportDialsThroughPublicOptIn(t *testing.T) {
	ctx := context.Background()
	dials := &connectTransportLog{}
	services, database, assetID := composeConnectRuntime(t, ctx, filepath.Join(t.TempDir(), "connect.db"), dials)

	// The asset starts connected with a complete bound identity.
	initial, err := services.Sessions.ConnectFor(ctx, session.Asset{ID: assetID, Kind: session.KindLocal, StoreBacked: true, ConfigFingerprint: "fp-initial"})
	if err != nil {
		t.Fatal(err)
	}
	if got := dials.count(); got != 1 {
		t.Fatalf("initial connect dialed %d times", got)
	}
	// The transport dies: the resolver's verdict must be the not-connected
	// sentinel so connect=true reaches the gated connector.
	dials.at(0).alive.Store(false)
	registry := services.Agent.ToolsRegistry()
	declined := registry.Execute(ctx, "job", tools.Scope{}, connectCreateCall(assetID), nil)
	if declined.OK {
		t.Fatalf("connect without opt-in succeeded on a dead transport: %+v", declined)
	}
	if strings.Contains(declined.Text, "请手动重连") || strings.Contains(declined.Text, "请先手动连接") {
		t.Fatalf("dead transport still told to reconnect manually: %+v", declined)
	}
	if !strings.Contains(declined.Text, "opt-in") {
		t.Fatalf("dead-transport connect did not reach the opt-in gate: %+v", declined)
	}
	if got := dials.count(); got != 1 {
		t.Fatalf("declined connect dialed: %d", got)
	}
	// Unknown assets never reach the dialer at all.
	unknown := registry.Execute(ctx, "job", tools.Scope{}, connectCreateCall("asset-missing"), nil)
	if unknown.OK || strings.Contains(unknown.Text, "opt-in") {
		t.Fatalf("unknown asset reached the connector: %+v", unknown)
	}
	if got := dials.count(); got != 1 {
		t.Fatalf("unknown asset dialed: %d", got)
	}
	// The user enables the surface opt-in through the public settings IPC:
	// the gated connect path completes. Manager.Connect reuses the existing
	// session record for the asset (the session layer's established
	// contract, shared by every R-07 surface), so no second dial happens;
	// the created tab rides the manager-resolved session.
	enableAiTerminalConnectOptIn(t, ctx, database)
	created := registry.Execute(ctx, "job-2", tools.Scope{}, connectCreateCall(assetID), nil)
	if !created.OK {
		t.Fatalf("dead-transport connect after public opt-in = %+v", created)
	}
	if got := dials.count(); got != 1 {
		t.Fatalf("Manager.Connect reuse semantics changed: dials=%d", got)
	}
	if created.CreatedTab == nil || created.CreatedTab.SessionID != initial.ID {
		t.Fatalf("created tab not on the manager-resolved session: %+v", created.CreatedTab)
	}
}
