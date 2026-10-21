package production

import (
	"context"
	"encoding/json"
	"errors"
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

// connectFakeChannel is an honest PTY channel: writes loop back into a
// readable queue (so tests can do real write/read I/O), reads block until
// data or close, and every operation after close fails with base.ErrClosed —
// the same contract real local and SSH channels have.
type connectFakeChannel struct {
	id     string
	closed chan struct{}
	once   sync.Once

	mu    sync.Mutex
	queue [][]byte
}

func newConnectFakeChannel(id string) *connectFakeChannel {
	return &connectFakeChannel{id: id, closed: make(chan struct{})}
}

func (c *connectFakeChannel) Read(buffer []byte) (int, error) {
	for {
		c.mu.Lock()
		if len(c.queue) > 0 {
			n := copy(buffer, c.queue[0])
			c.queue[0] = c.queue[0][n:]
			if len(c.queue[0]) == 0 {
				c.queue = c.queue[1:]
			}
			c.mu.Unlock()
			return n, nil
		}
		c.mu.Unlock()
		select {
		case <-c.closed:
			return 0, io.EOF
		case <-time.After(time.Millisecond):
		}
	}
}

func (c *connectFakeChannel) Write(data []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, base.ErrClosed
	default:
	}
	c.mu.Lock()
	c.queue = append(c.queue, append([]byte(nil), data...))
	c.mu.Unlock()
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
// creation. A dead transport refuses PTY and Exec with base.ErrClosed —
// exactly what real local and SSH transports do — so a test can never fake
// success onto a dead connection.
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
	if !t.alive.Load() {
		return 0, base.ErrClosed
	}
	return 0, nil
}
func (t *connectFakeTransport) Exec(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
	if !t.alive.Load() {
		return base.ExecResult{}, base.ErrClosed
	}
	code := 0
	return base.ExecResult{Stdout: "ran:" + command, ExitCode: &code}, nil
}
func (t *connectFakeTransport) OpenPTY(context.Context, base.PTYOptions) (base.Channel, error) {
	if !t.alive.Load() {
		return nil, base.ErrClosed
	}
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

// connectTransportLog records every dial the harness produced — the asset it
// was asked to connect and the transport it returned — so tests can assert
// the strict-host-key options survived the dial and kill a specific
// session's transport.
type connectTransportLog struct {
	mu     sync.Mutex
	assets []session.Asset
	list   []*connectFakeTransport
}

func (l *connectTransportLog) add(asset session.Asset, transport *connectFakeTransport) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.assets = append(l.assets, asset)
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

func (l *connectTransportLog) asset(index int) session.Asset {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.assets[index]
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
		Connector: session.ConnectorFunc(func(_ context.Context, asset session.Asset, _ uint64) (base.Transport, error) {
			transport := newConnectFakeTransport()
			dials.add(asset, transport)
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
	// the gated connect path dials again. The session layer no longer reuses
	// the dead-transport session — it disconnects the stale record and
	// redials — so the operation lands on a fresh, healthy session whose
	// strict-host-key asset options were never weakened.
	enableAiTerminalConnectOptIn(t, ctx, database)
	created := registry.Execute(ctx, "job-2", tools.Scope{}, connectCreateCall(assetID), nil)
	if !created.OK {
		t.Fatalf("dead-transport connect after public opt-in = %+v", created)
	}
	if got := dials.count(); got != 2 {
		t.Fatalf("no redial after the dead transport: dials=%d, want 2", got)
	}
	options, ok := dials.asset(1).Options.(map[string]any)
	if !ok || options["strictHostKey"] != true {
		t.Fatalf("redialed asset lost the strict-host-key mark: %+v", dials.asset(1).Options)
	}
	if created.CreatedTab == nil || created.CreatedTab.SessionID == "" || created.CreatedTab.SessionID == initial.ID {
		t.Fatalf("created tab not on a fresh session: %+v (initial %s)", created.CreatedTab, initial.ID)
	}
	// The final session's transport is alive and does real I/O; the dead one
	// refuses PTY and Exec exactly like a real local or SSH transport.
	final, err := services.Sessions.TransportFor(ctx, created.CreatedTab.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !final.IsAlive() {
		t.Fatal("final session's transport is not alive")
	}
	result, err := final.Exec(ctx, "echo healthy", base.ExecOptions{})
	if err != nil || result.Stdout != "ran:echo healthy" {
		t.Fatalf("Exec on the final session = %+v, %v", result, err)
	}
	channel, err := final.(base.PTYTransport).OpenPTY(ctx, base.PTYOptions{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = channel.Close() }()
	if written, err := channel.Write([]byte("healthy")); err != nil || written != len("healthy") {
		t.Fatalf("PTY Write on the final session = %d, %v", written, err)
	}
	readBack := make(chan string, 1)
	go func() {
		buffer := make([]byte, len("healthy"))
		n, readErr := channel.Read(buffer)
		if readErr != nil {
			readBack <- "read error: " + readErr.Error()
			return
		}
		readBack <- string(buffer[:n])
	}()
	select {
	case got := <-readBack:
		if got != "healthy" {
			t.Fatalf("PTY Read on the final session = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PTY Read on the final session timed out")
	}
	dead := dials.at(0)
	if _, err := dead.Exec(ctx, "echo nope", base.ExecOptions{}); !errors.Is(err, base.ErrClosed) {
		t.Fatalf("dead transport Exec = %v, want ErrClosed", err)
	}
	var deadPTY base.PTYTransport = dead
	if _, err := deadPTY.OpenPTY(ctx, base.PTYOptions{Cols: 80, Rows: 24}); !errors.Is(err, base.ErrClosed) {
		t.Fatalf("dead transport OpenPTY = %v, want ErrClosed", err)
	}
}

// An interleaved deletion of the selected session between the listing and
// the SessionFor read is the same definite not-connected verdict: without
// the opt-in the connect reaches the R-07 gate, and with the public opt-in
// it redials to a healthy session with real I/O.
func TestProductionTerminalConnectSessionDeletedInterleaveDialsHealthy(t *testing.T) {
	ctx := context.Background()
	dials := &connectTransportLog{}
	services, database, assetID := composeConnectRuntime(t, ctx, filepath.Join(t.TempDir(), "connect.db"), dials)
	initial, err := services.Sessions.ConnectFor(ctx, session.Asset{ID: assetID, Kind: session.KindLocal, StoreBacked: true, ConfigFingerprint: "fp-initial"})
	if err != nil {
		t.Fatal(err)
	}
	if got := dials.count(); got != 1 {
		t.Fatalf("initial connect dialed %d times", got)
	}
	// The interleave deletes the selected session after the listing (a local
	// session is reaped on disconnect) and before the resolver's own read.
	resolveAssetSessionInterleaveHook = func(point string) {
		if point == resolveAssetSessionInterleaveAfterLive {
			_ = services.Sessions.Disconnect(initial.ID)
		}
	}
	defer func() { resolveAssetSessionInterleaveHook = nil }()

	registry := services.Agent.ToolsRegistry()
	declined := registry.Execute(ctx, "job", tools.Scope{}, connectCreateCall(assetID), nil)
	if declined.OK {
		t.Fatalf("connect without opt-in succeeded after the interleaved deletion: %+v", declined)
	}
	if !strings.Contains(declined.Text, "opt-in") {
		t.Fatalf("deleted-session connect did not reach the opt-in gate: %+v", declined)
	}
	if strings.Contains(declined.Text, "无效或无权") || strings.Contains(declined.Text, "请手动") {
		t.Fatalf("deleted-session verdict not mapped to the not-connected sentinel: %+v", declined)
	}
	if got := dials.count(); got != 1 {
		t.Fatalf("declined connect dialed: %d", got)
	}

	// The user enables the surface opt-in through the public settings IPC.
	// The same interleave now lands on the R-07 dial: the fresh session is
	// healthy and does real I/O.
	enableAiTerminalConnectOptIn(t, ctx, database)
	second, err := services.Sessions.ConnectFor(ctx, session.Asset{ID: assetID, Kind: session.KindLocal, StoreBacked: true, ConfigFingerprint: "fp-second"})
	if err != nil {
		t.Fatal(err)
	}
	if got := dials.count(); got != 2 {
		t.Fatalf("second connect dialed %d times", got)
	}
	resolveAssetSessionInterleaveHook = func(point string) {
		if point == resolveAssetSessionInterleaveAfterLive {
			_ = services.Sessions.Disconnect(second.ID)
		}
	}
	created := registry.Execute(ctx, "job-2", tools.Scope{}, connectCreateCall(assetID), nil)
	if !created.OK {
		t.Fatalf("connect after public opt-in = %+v", created)
	}
	if got := dials.count(); got != 3 {
		t.Fatalf("no redial after the interleaved deletion: dials=%d, want 3", got)
	}
	if created.CreatedTab == nil || created.CreatedTab.SessionID == "" || created.CreatedTab.SessionID == second.ID {
		t.Fatalf("created tab not on a fresh session: %+v", created.CreatedTab)
	}
	final, err := services.Sessions.TransportFor(ctx, created.CreatedTab.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !final.IsAlive() {
		t.Fatal("final session's transport is not alive")
	}
	result, err := final.Exec(ctx, "echo healthy", base.ExecOptions{})
	if err != nil || result.Stdout != "ran:echo healthy" {
		t.Fatalf("Exec on the final session = %+v, %v", result, err)
	}
	channel, err := final.(base.PTYTransport).OpenPTY(ctx, base.PTYOptions{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = channel.Close() }()
	if written, err := channel.Write([]byte("healthy")); err != nil || written != len("healthy") {
		t.Fatalf("PTY Write on the final session = %d, %v", written, err)
	}
	buffer := make([]byte, len("healthy"))
	read := 0
	for read < len(buffer) {
		n, readErr := channel.Read(buffer[read:])
		if readErr != nil {
			t.Fatalf("PTY Read on the final session = %v", readErr)
		}
		read += n
	}
	if string(buffer) != "healthy" {
		t.Fatalf("PTY Read on the final session = %q", buffer)
	}
}

// An interleaved disconnect of the selected session between the status check
// and the transport read (or an interleaved deletion there) is the same
// definite not-connected verdict: it enters the R-07 gates instead of
// failing at the identity binding.
func TestProductionTerminalConnectTransportInterleaveDialsHealthy(t *testing.T) {
	ctx := context.Background()
	dials := &connectTransportLog{}
	services, database, assetID := composeConnectRuntime(t, ctx, filepath.Join(t.TempDir(), "connect.db"), dials)
	initial, err := services.Sessions.ConnectFor(ctx, session.Asset{ID: assetID, Kind: session.KindLocal, StoreBacked: true, ConfigFingerprint: "fp-initial"})
	if err != nil {
		t.Fatal(err)
	}
	if got := dials.count(); got != 1 {
		t.Fatalf("initial connect dialed %d times", got)
	}
	// The interleave deletes the selected session after the resolver's status
	// check passes and before its transport read.
	resolveAssetSessionInterleaveHook = func(point string) {
		if point == resolveAssetSessionInterleaveBeforeTransport {
			_ = services.Sessions.Disconnect(initial.ID)
		}
	}
	defer func() { resolveAssetSessionInterleaveHook = nil }()

	registry := services.Agent.ToolsRegistry()
	declined := registry.Execute(ctx, "job", tools.Scope{}, connectCreateCall(assetID), nil)
	if declined.OK {
		t.Fatalf("connect without opt-in succeeded after the interleaved deletion: %+v", declined)
	}
	if !strings.Contains(declined.Text, "opt-in") {
		t.Fatalf("transport-gone connect did not reach the opt-in gate: %+v", declined)
	}
	if strings.Contains(declined.Text, "无效或无权") || strings.Contains(declined.Text, "请手动") {
		t.Fatalf("transport-gone verdict not mapped to the not-connected sentinel: %+v", declined)
	}
	if got := dials.count(); got != 1 {
		t.Fatalf("declined connect dialed: %d", got)
	}

	enableAiTerminalConnectOptIn(t, ctx, database)
	second, err := services.Sessions.ConnectFor(ctx, session.Asset{ID: assetID, Kind: session.KindLocal, StoreBacked: true, ConfigFingerprint: "fp-second"})
	if err != nil {
		t.Fatal(err)
	}
	if got := dials.count(); got != 2 {
		t.Fatalf("second connect dialed %d times", got)
	}
	resolveAssetSessionInterleaveHook = func(point string) {
		if point == resolveAssetSessionInterleaveBeforeTransport {
			_ = services.Sessions.Disconnect(second.ID)
		}
	}
	created := registry.Execute(ctx, "job-2", tools.Scope{}, connectCreateCall(assetID), nil)
	if !created.OK {
		t.Fatalf("connect after public opt-in = %+v", created)
	}
	if got := dials.count(); got != 3 {
		t.Fatalf("no redial after the interleaved deletion: dials=%d, want 3", got)
	}
	if created.CreatedTab == nil || created.CreatedTab.SessionID == "" || created.CreatedTab.SessionID == second.ID {
		t.Fatalf("created tab not on a fresh session: %+v", created.CreatedTab)
	}
	final, err := services.Sessions.TransportFor(ctx, created.CreatedTab.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !final.IsAlive() {
		t.Fatal("final session's transport is not alive")
	}
	result, err := final.Exec(ctx, "echo healthy", base.ExecOptions{})
	if err != nil || result.Stdout != "ran:echo healthy" {
		t.Fatalf("Exec on the final session = %+v, %v", result, err)
	}
	channel, err := final.(base.PTYTransport).OpenPTY(ctx, base.PTYOptions{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = channel.Close() }()
	if written, err := channel.Write([]byte("healthy")); err != nil || written != len("healthy") {
		t.Fatalf("PTY Write on the final session = %d, %v", written, err)
	}
	buffer := make([]byte, len("healthy"))
	read := 0
	for read < len(buffer) {
		n, readErr := channel.Read(buffer[read:])
		if readErr != nil {
			t.Fatalf("PTY Read on the final session = %v", readErr)
		}
		read += n
	}
	if string(buffer) != "healthy" {
		t.Fatalf("PTY Read on the final session = %q", buffer)
	}
}

// The ErrDisconnected sentinel at the transport read maps the same way: an
// SSH session disconnected between the status check and the read reaches the
// R-07 gates after the public opt-in, and the session layer's
// deliberate-disconnect contract then refuses the redial honestly instead of
// the verdict dying at the identity binding.
func TestProductionTerminalConnectTransportDisconnectedReachesGate(t *testing.T) {
	ctx := context.Background()
	dials := &connectTransportLog{}
	services, database, _ := composeConnectRuntime(t, ctx, filepath.Join(t.TempDir(), "connect.db"), dials)
	hostKeys, err := newProductionHostKeyStore(ctx, filepath.Join(t.TempDir(), "known_hosts.json"), database, false)
	if err != nil {
		t.Fatal(err)
	}
	services.hostKeys = hostKeys
	hostname := "gate.example"
	sshAsset, err := database.AssetCreate(ctx, store.AssetInput{Kind: session.KindSSH, Name: "gate", Host: &hostname})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.KnownHostAccept(ctx, hostname, 22, "ssh-ed25519", "SHA256:gate"); err != nil {
		t.Fatal(err)
	}
	initial, err := services.Sessions.ConnectFor(ctx, session.Asset{ID: sshAsset.ID, Name: sshAsset.Name, Kind: session.KindSSH, StoreBacked: true, ConfigFingerprint: "fp-initial"})
	if err != nil {
		t.Fatal(err)
	}
	if got := dials.count(); got != 1 {
		t.Fatalf("initial connects dialed %d times", got)
	}
	// The interleave disconnects the SSH session (a reconnectable kind keeps
	// its record) after the status check and before the transport read.
	resolveAssetSessionInterleaveHook = func(point string) {
		if point == resolveAssetSessionInterleaveBeforeTransport {
			_ = services.Sessions.Disconnect(initial.ID)
		}
	}
	defer func() { resolveAssetSessionInterleaveHook = nil }()

	registry := services.Agent.ToolsRegistry()
	declined := registry.Execute(ctx, "job", tools.Scope{}, connectCreateCall(sshAsset.ID), nil)
	if declined.OK {
		t.Fatalf("connect without opt-in succeeded after the interleaved disconnect: %+v", declined)
	}
	if !strings.Contains(declined.Text, "opt-in") {
		t.Fatalf("disconnected-session connect did not reach the opt-in gate: %+v", declined)
	}
	if strings.Contains(declined.Text, "无效或无权") || strings.Contains(declined.Text, "请手动") {
		t.Fatalf("disconnected-session verdict not mapped to the not-connected sentinel: %+v", declined)
	}
	if got := dials.count(); got != 1 {
		t.Fatalf("declined connect dialed: %d", got)
	}

	// With the public opt-in the R-07 gates all pass, and the session layer's
	// deliberate-disconnect contract refuses the redial of the disconnected
	// record honestly — the same contract every R-07 surface shares.
	enableAiTerminalConnectOptIn(t, ctx, database)
	failed := registry.Execute(ctx, "job-2", tools.Scope{}, connectCreateCall(sshAsset.ID), nil)
	if failed.OK {
		t.Fatalf("redial of a deliberately disconnected record faked success: %+v", failed)
	}
	if failed.Panic {
		t.Fatalf("redial of a deliberately disconnected record panicked: %+v", failed)
	}
	if got := dials.count(); got != 1 {
		t.Fatalf("session layer redialed a deliberately disconnected record: dials=%d", got)
	}
}
