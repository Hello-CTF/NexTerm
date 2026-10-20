//go:build darwin || linux

package production

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	gossh "golang.org/x/crypto/ssh"
)

func TestProductionAttachTabMissingTabReturnsNotFoundAndInvalidTabBadParam(t *testing.T) {
	dataDir := durableTestDataDir(t)
	production := newTerminalTestProduction(t, dataDir, &bridgeTestFactory{}, nil)
	connectLocalTerminalTest(t, production)

	cases := []struct {
		name string
		id   string
		want ipc.Code
	}{
		{"missing daemon tab", ids.New(), ipc.CodeNotFound},
		{"missing volatile tab", strings.Repeat("a1b2", 8), ipc.CodeNotFound},
		{"malformed tab id", "not-a-supervisor-id", ipc.CodeBadParam},
		{"empty tab id", "", ipc.CodeBadParam},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			channelID := "attach-tab-codes-" + strings.Repeat("x", index+1)
			response := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tc.id+`","replayBytes":1024}`, channelID, "client-a")
			if response.OK || response.Error == nil || response.Error.Code != tc.want {
				t.Fatalf("attach tab %q = %+v, want %v", tc.id, response, tc.want)
			}
		})
	}
}

func TestProductionAttachTabLocalTabEndsWithAppRestart(t *testing.T) {
	dataDir := durableTestDataDir(t)
	first := newTerminalTestProduction(t, dataDir, &bridgeTestFactory{}, nil)
	connected := connectLocalTerminalTest(t, first)
	attachResponse := dispatchDurableTest(t, first, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "attach-tab-restart-channel", "client-a")
	var attached attachedTabDTO
	requireStoreTestResponse(t, attachResponse, &attached)
	tabID := attached.TabID
	if tabID == "" || attached.SessionID != connected.ID || attached.Shell != "posix" || !attached.Retainable {
		t.Fatalf("initial attach = %+v, want unified attached DTO with posix shell and local background capability", attached)
	}
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	second := newTerminalTestProduction(t, dataDir, &bridgeTestFactory{}, nil)
	recoveredResponse := dispatchDurableTest(t, second, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":65536}`, "attach-tab-restart-channel", "client-a")
	if recoveredResponse.OK || recoveredResponse.Error == nil || recoveredResponse.Error.Code != ipc.CodeNotFound {
		t.Fatalf("attach tab %s after restart = %+v, want not_found", tabID, recoveredResponse)
	}
}

func TestProductionTerminalCreateAndRenameTab(t *testing.T) {
	dataDir := durableTestDataDir(t)
	production := newTerminalTestProduction(t, dataDir, &bridgeTestFactory{}, nil)
	connected := connectLocalTerminalTest(t, production)

	createResponse := dispatchDurableTest(t, production, "terminal_create_tab", `{"sessionId":"`+connected.ID+`","title":"构建机"}`, "", "client-a")
	var created attachedTabDTO
	requireStoreTestResponse(t, createResponse, &created)
	if created.TabID == "" || created.Title != "构建机" || created.SessionID != connected.ID {
		t.Fatalf("created = %+v", created)
	}

	renameResponse := dispatchDurableTest(t, production, "terminal_rename_tab", `{"tabId":"`+created.TabID+`","title":"部署终端"}`, "", "client-a")
	var renamed attachedTabDTO
	requireStoreTestResponse(t, renameResponse, &renamed)
	if renamed.TabID != created.TabID || renamed.Title != "部署终端" {
		t.Fatalf("renamed = %+v", renamed)
	}

	listResponse := dispatchDurableTest(t, production, "terminal_list", `null`, "", "client-a")
	var live []liveTabDTO
	requireStoreTestResponse(t, listResponse, &live)
	found := false
	for _, tab := range live {
		if tab.TabID == created.TabID {
			found = true
			if tab.Title != "部署终端" {
				t.Fatalf("listed tab = %+v", tab)
			}
		}
	}
	if !found {
		t.Fatalf("created tab missing from terminal_list: %+v", live)
	}

	badTitle := dispatchDurableTest(t, production, "terminal_rename_tab", `{"tabId":"`+created.TabID+`","title":"  "}`, "", "client-a")
	if badTitle.OK || badTitle.Error == nil || badTitle.Error.Code != ipc.CodeBadParam {
		t.Fatalf("blank title rename = %+v, want bad_param", badTitle)
	}
	missing := dispatchDurableTest(t, production, "terminal_rename_tab", `{"tabId":"missing","title":"x"}`, "", "client-a")
	if missing.OK || missing.Error == nil || missing.Error.Code != ipc.CodeNotFound {
		t.Fatalf("missing tab rename = %+v, want not_found", missing)
	}
	commandCreate := dispatchDurableTest(t, production, "terminal_create_tab", `{"sessionId":"`+connected.ID+`","command":"echo nx-startup-mark"}`, "", "client-a")
	var commandTab attachedTabDTO
	requireStoreTestResponse(t, commandCreate, &commandTab)
	deadline := time.Now().Add(10 * time.Second)
	for {
		screenResponse := dispatchDurableTest(t, production, "terminal_screen_text", `{"tabId":"`+commandTab.TabID+`"}`, "", "client-a")
		var screen string
		requireStoreTestResponse(t, screenResponse, &screen)
		if strings.Contains(screen, "nx-startup-mark") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("startup command output missing from local tab screen: %q", screen)
		}
		time.Sleep(50 * time.Millisecond)
	}
	missingSession := dispatchDurableTest(t, production, "terminal_create_tab", `{"sessionId":"missing"}`, "", "client-a")
	if missingSession.OK || missingSession.Error == nil || missingSession.Error.Code != ipc.CodeNotFound {
		t.Fatalf("missing session create = %+v, want not_found", missingSession)
	}
}

type productionRetryChannel struct {
	id         string
	generation uint64
	closed     chan struct{}
	closeOnce  sync.Once
	mu         sync.Mutex
	failures   int
	calls      int
	err        error
}

func (c *productionRetryChannel) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}
func (c *productionRetryChannel) Write(data []byte) (int, error) { return len(data), nil }
func (c *productionRetryChannel) Close() error {
	c.mu.Lock()
	c.calls++
	if c.failures > 0 {
		c.failures--
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.mu.Unlock()
	c.finish()
	return nil
}
func (c *productionRetryChannel) finish() {
	c.closeOnce.Do(func() { close(c.closed) })
}
func (c *productionRetryChannel) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}
func (c *productionRetryChannel) Stderr() io.Reader                            { return nil }
func (c *productionRetryChannel) Resize(context.Context, uint32, uint32) error { return nil }
func (c *productionRetryChannel) Wait(ctx context.Context) error {
	select {
	case <-c.closed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *productionRetryChannel) CloseWrite() error  { return nil }
func (c *productionRetryChannel) ID() string         { return c.id }
func (c *productionRetryChannel) Generation() uint64 { return c.generation }

type productionRetryTransport struct {
	fakeSSHTransport
	channel *productionRetryChannel
}

func (t *productionRetryTransport) OpenPTY(context.Context, base.PTYOptions) (base.Channel, error) {
	return t.channel, nil
}

func TestProductionTerminalCloseFailureRetainsTabForRetry(t *testing.T) {
	injected := errors.New("injected scan failure")
	channel := &productionRetryChannel{id: "close-retry", generation: 1, closed: make(chan struct{}), failures: 1, err: injected}
	connector := session.ConnectorFunc(func(_ context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
		return &productionRetryTransport{
			fakeSSHTransport: fakeSSHTransport{kind: asset.Kind, generation: generation},
			channel:          channel,
		}, nil
	})
	manager := session.NewManager(session.Config{Connector: connector, OpenAccess: true})
	production, err := NewProductionWithServices(Config{}, ProductionServices{Sessions: manager})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		channel.finish()
		_ = production.Shutdown(context.Background())
	})
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "close-retry-local", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "close-retry-channel", "client-a")
	tabID := requireAttachedTabID(t, attachResponse)

	failed := dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`"}`, "", "client-a")
	if failed.OK || failed.Error == nil {
		t.Fatalf("failed terminal close = %+v, want an error response", failed)
	}
	if _, err := manager.Tab(tabID); err != nil {
		t.Fatalf("failed terminal close removed tab: %v", err)
	}
	background := dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","mode":"detach"}`, "", "client-a")
	if background.OK || background.Error == nil {
		t.Fatalf("partial terminal background = %+v, want an error response", background)
	}
	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`"}`, "", "client-a"))
	if _, err := manager.Tab(tabID); !errors.Is(err, session.ErrTabNotFound) {
		t.Fatalf("successfully retried tab still listed: %v", err)
	}
	if got := channel.callCount(); got != 2 {
		t.Fatalf("channel Close calls = %d, want failed attempt and retry", got)
	}
}

func TestProductionLocalExplicitDetachRetainsAndListsBackgroundTab(t *testing.T) {
	t.Setenv("SHELL", "")
	t.Setenv("ENV", "")
	factory := &bridgeTestFactory{}
	production := newTerminalTestProduction(t, durableTestDataDir(t), factory, nil)
	connected := connectLocalTerminalTest(t, production)
	channelID := "local-background-channel"
	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelID, "client-a")
	tabID := requireAttachedTabID(t, attachResponse)
	writeDurableTestCommand(t, production, tabID, "local-background-ready", "client-a")
	waitForProductionOutput(t, factory.at(channelID, 0), "local-background-ready")

	unattached := dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","mode":"detach"}`, channelID, "client-b")
	if unattached.OK || unattached.Error == nil || unattached.Error.Code != ipc.CodeDisconnected {
		t.Fatalf("unattached client explicit detach = %+v, want disconnected", unattached)
	}
	current, err := production.Services.Sessions.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	if info := current.Info(); info.Subscribers != 1 || info.Exited {
		t.Fatalf("tab after rejected client detach = %+v", info)
	}

	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","mode":"detach"}`, channelID, "client-a"))
	listResponse := dispatchDurableTest(t, production, "terminal_list", `null`, "", "client-a")
	var listed []liveTabDTO
	requireStoreTestResponse(t, listResponse, &listed)
	found := false
	for _, tab := range listed {
		if tab.TabID == tabID {
			found = true
			if tab.Subscribers != 0 || tab.Exited || tab.SessionKind != "local" {
				t.Fatalf("background tab = %+v", tab)
			}
		}
	}
	if !found {
		t.Fatalf("explicitly detached local tab missing from terminal_list: %+v", listed)
	}

	reattachChannel := "local-background-reattach"
	reattachResponse := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":65536}`, reattachChannel, "client-a")
	var attached attachedTabDTO
	requireStoreTestResponse(t, reattachResponse, &attached)
	if attached.TabID != tabID || attached.SessionID != connected.ID || attached.Exited || !attached.Retainable {
		t.Fatalf("reattached local tab = %+v, want same retainable tab", attached)
	}
	writeDurableTestCommand(t, production, tabID, "nx-ipc-background-input", "client-a")
	waitForProductionOutput(t, factory.at(reattachChannel, 0), "nx-ipc-background-input")
	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`"}`, "", "client-a"))
}

func TestProductionLineTabExplicitDetachUnsupportedPreservesTab(t *testing.T) {
	factory := &bridgeTestFactory{}
	production := newTerminalTestProduction(t, durableTestDataDir(t), factory, nil)
	connected := connectLocalTerminalTest(t, production)
	channelID := "local-line-background-channel"
	attachResponse := dispatchDurableTest(t, production, "session_open_line_tab", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelID, "client-a")
	var attached attachedTabDTO
	requireStoreTestResponse(t, attachResponse, &attached)
	tabID := attached.TabID

	response := dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","mode":"detach"}`, channelID, "client-a")
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("line tab explicit detach = %+v, want unsupported", response)
	}
	tab, err := production.Services.Sessions.Tab(tabID)
	if err != nil {
		t.Fatalf("failed explicit detach removed line tab: %v", err)
	}
	if info := tab.Info(); info.Subscribers != 1 || info.Exited {
		t.Fatalf("line tab after rejected detach = %+v", info)
	}
	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`"}`, "", "client-a"))
}

func TestProductionSessionReconnectDoesNotAutoTrustChangedHostKey(t *testing.T) {
	server := newHostKeyTestServer(t)
	production := newHostKeyTestProduction(t)
	assetID := createHostKeyTestAsset(t, production, server.port())

	response := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	pending := requireHostKeyPending(t, response, false)
	acceptHostKeyTest(t, production, pending)
	connected := connectHostKeyTestAsset(t, production, assetID)

	rotated := server.rotateHostKey(t)
	reconnectResponse := dispatchHostKeyTest(t, production, "session_reconnect", `{"sessionId":"`+connected.ID+`"}`)
	var started bool
	requireStoreTestResponse(t, reconnectResponse, &started)
	if !started {
		t.Fatal("session_reconnect did not start")
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if listed := knownHostTestFingerprints(t, production); len(listed) != 1 || listed[0] != pending.Fingerprint {
			t.Fatalf("reconnect auto-trusted a changed host key: %+v", listed)
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))

	response = dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	changed := requireHostKeyPending(t, response, true)
	if changed.Fingerprint != gossh.FingerprintSHA256(rotated) {
		t.Fatalf("changed detail fingerprint = %+v", changed)
	}
	if listed := knownHostTestFingerprints(t, production); len(listed) != 1 || listed[0] != pending.Fingerprint {
		t.Fatalf("reconnect attempts altered the trust ledger: %+v", listed)
	}
}
