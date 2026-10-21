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
// creation; every channel is tracked so Close tears the runtime down.
type connectFakeTransport struct {
	mu       sync.Mutex
	channels []*connectFakeChannel
	opens    int
}

func (t *connectFakeTransport) Kind() string       { return session.KindLocal }
func (t *connectFakeTransport) Generation() uint64 { return 1 }
func (t *connectFakeTransport) IsAlive() bool      { return true }
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
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, channel := range t.channels {
		_ = channel.Close()
	}
	return nil
}

// The production terminal_create connect=true flow: the AI terminal surface
// has its own per-user opt-in, every R-07 gate applies before the strict
// host-key dial, and the recorded outcome adjudicates and replays across a
// full runtime restart instead of reconnecting.

// composeConnectRuntime builds a file-backed production runtime with real
// tab support; reopening the same path with a fresh session manager leaves
// only the durable rows carrying over, which is the restart boundary.
func composeConnectRuntime(t *testing.T, ctx context.Context, path string, dials *atomic.Int64) (*ProductionServices, *store.Store, string) {
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
			dials.Add(1)
			return &connectFakeTransport{}, nil
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

func connectCreateCall(assetID string) tools.Call {
	return tools.Call{ID: "c", Name: "terminal_create",
		Args: json.RawMessage(`{"asset_id":"` + assetID + `","connect":true}`), AuthorizationID: "guard:test"}
}

func TestProductionTerminalConnectRequiresOptIn(t *testing.T) {
	ctx := context.Background()
	var dials atomic.Int64
	services, _, assetID := composeConnectRuntime(t, ctx, filepath.Join(t.TempDir(), "connect.db"), &dials)

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
	if got := dials.Load(); got != 0 {
		t.Fatalf("declined connect dialed %d times", got)
	}
}

func TestProductionTerminalConnectRecordsDurableOutcome(t *testing.T) {
	ctx := context.Background()
	var dials atomic.Int64
	services, database, assetID := composeConnectRuntime(t, ctx, filepath.Join(t.TempDir(), "connect.db"), &dials)
	if err := database.SettingSet(ctx, aiTerminalConnectOptInSettingKeyForUser(ctx), "true"); err != nil {
		t.Fatal(err)
	}

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
	var dials atomic.Int64
	services, database, assetID := composeConnectRuntime(t, ctx, path, &dials)
	if err := database.SettingSet(ctx, aiTerminalConnectOptInSettingKeyForUser(ctx), "true"); err != nil {
		t.Fatal(err)
	}

	call := connectCreateCall(assetID)
	first := services.Agent.ToolsRegistry().Execute(ctx, "job", tools.Scope{}, call, nil)
	if !first.OK || first.OperationID == "" {
		t.Fatalf("first connected create = %+v", first)
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("first create dialed %d times", got)
	}
	// Close the whole runtime: sessions, AI runtime, and the store handle —
	// only durable rows survive.
	services.closeAIRuntime()
	_ = services.Sessions.Close()
	_ = database.Close()

	restored, _, restoredAssetID := composeConnectRuntime(t, ctx, path, &dials)
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
	if got := dials.Load(); got != 1 {
		t.Fatalf("post-restart retry reconnected: dials=%d", got)
	}
}
