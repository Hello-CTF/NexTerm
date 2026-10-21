//go:build darwin || linux

package production

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/server"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
	"github.com/coder/websocket"
)

func TestConcreteProductionCompositionPreservesLegacyTaskData(t *testing.T) {
	dataDir := t.TempDir()
	legacyTasksDir := filepath.Join(dataDir, "tasks")
	if err := os.MkdirAll(legacyTasksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(legacyTasksDir, "task.json")
	if err := os.WriteFile(sentinelPath, []byte("legacy task data"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{Logger: logger}, DataDir: dataDir, Desktop: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		"app_info", "app_platform", "session_connect", "session_connect_local", "terminal_attach", "terminal_attach_tab",
		"terminal_resize", "terminal_resize_flush", "fs_list", "fs_write", "asset_list", "asset_update", "layout_put",
		"vault_set_credential", "credential_update", "ai_model_save", "ai_conversation_list", "docker_exec_attach",
		"docker_action", "db_connect", "mount_create", "forward_create", "forward_rule_list", "sync_link_get", "sync_status", "sync_wake",
	} {
		if !slices.Contains(production.Dispatcher.Commands(), command) {
			t.Fatalf("production is missing %s", command)
		}
	}
	if got := len(production.Dispatcher.Commands()); got < 120 {
		t.Fatalf("production command count = %d, commands = %v", got, production.Dispatcher.Commands())
	}
	for _, command := range []string{
		"ai_chat", "ai_cancel", "ai_confirm", "ai_answer", "ai_get_permission", "ai_set_permission",
		"ai_takeover_enter", "ai_takeover_exit", "ai_takeover_run",
		"ai_models", "ai_test_provider", "ai_get_provider", "ai_set_provider", "ai_presets",
		"ai_model_profiles", "ai_model_save", "ai_model_delete", "ai_model_activate", "ai_model_refresh", "ai_model_preset",
		"ai_conversation_list", "ai_conversation_create", "ai_conversation_delete", "ai_messages",
	} {
		if !slices.Contains(production.Dispatcher.Commands(), command) {
			t.Fatalf("production is missing AI command %s", command)
		}
	}
	if production.Services.Guard == nil || production.Services.Agent == nil || production.Services.Takeover == nil {
		t.Fatal("AI runtime services are not composed")
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := production.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	preserved, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(preserved) != "legacy task data" {
		t.Fatalf("legacy task data = %q", preserved)
	}
	if count := bytes.Count(logs.Bytes(), []byte("legacy persistent task data directory preserved")); count != 1 {
		t.Fatalf("legacy task log count = %d, logs = %s", count, logs.String())
	}
}

func TestConcreteProductionNormalizesRelativeDataDir(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	production, err := NewProduction(t.Context(), ProductionConfig{DataDir: "state", Desktop: true})
	if err != nil {
		t.Fatal(err)
	}
	if production.Services.dataDir != root+"/state" {
		t.Fatalf("data dir = %q, want %q", production.Services.dataDir, root+"/state")
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := production.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestConcreteProductionAutoInitializesVault(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{Logger: logger}, DataDir: dataDir, Desktop: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	status := production.Services.Vault.Status()
	if !status.Initialized || !status.Unlocked || !status.Passwordless {
		t.Fatalf("fresh desktop vault = %+v, want initialized, unlocked, passwordless", status)
	}
	response := dispatchStoreTest(production.Dispatcher, "vault_set_credential", `{"args":{"name":"auto","kind":"password","secret":"s3cret"}}`)
	var created map[string]string
	requireStoreTestResponse(t, response, &created)
	if created["id"] == "" {
		t.Fatalf("vault_set_credential = %+v", created)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := production.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{Logger: logger}, DataDir: dataDir, Desktop: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	status = reopened.Services.Vault.Status()
	if !status.Initialized || !status.Unlocked {
		t.Fatalf("reopened desktop vault = %+v, want initialized and unlocked", status)
	}
	response = dispatchStoreTest(reopened.Dispatcher, "vault_reveal_credential", `{"id":"`+created["id"]+`"}`)
	var revealed struct {
		Value string `json:"value"`
	}
	requireStoreTestResponse(t, response, &revealed)
	if revealed.Value != "s3cret" {
		t.Fatalf("revealed credential = %q", revealed.Value)
	}
	if err := reopened.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestConcreteProductionSessionOpenAccessDefaults(t *testing.T) {
	for _, test := range []struct {
		name    string
		desktop bool
	}{
		{name: "account", desktop: false},
		{name: "desktop", desktop: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			production, err := NewProduction(t.Context(), ProductionConfig{
				Config:  Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
				DataDir: t.TempDir(), Desktop: test.desktop,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := production.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
			_, err = production.Services.Sessions.ListSessionsFor(context.Background())
			if test.desktop && err != nil {
				t.Fatalf("desktop session access = %v, want open", err)
			}
			if !test.desktop && !errors.Is(err, session.ErrForbidden) {
				t.Fatalf("account session access = %v, want ErrForbidden", err)
			}
		})
	}
}

func TestConcreteProductionChannelOwnerLifecycle(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{Logger: logger}, DataDir: t.TempDir(), Desktop: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	if production.Services.channelBridge.enabled {
		t.Fatal("shared hub terminal bridge must stay disabled")
	}
	transport, err := server.New(server.Config{
		Options:     server.Options{Listen: "127.0.0.1:0", DataDir: t.TempDir(), Auth: server.AuthOff},
		Dispatcher:  production.Dispatcher,
		Environment: production.Environment(""),
		SyncObjects: production.Services.Sync.ObjectHandler(),
		Channels:    server.NewHubAdapter(production.Services.Sessions.Hub()),
		Spectator:   production.Services.Sessions,
		Logger:      logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	httpServer := httptest.NewServer(transport.Handler())
	t.Cleanup(httpServer.Close)

	t.Run("final close unregisters owner", func(t *testing.T) {
		stream, err := production.Environment("").Streams.OpenBinary(ipc.WithUserID(context.Background(), "u-1"), ipc.ChannelRef{ID: "final-close"})
		if err != nil {
			t.Fatal(err)
		}
		ownerID, err := production.Services.Sessions.ChannelOwner("final-close")
		if err != nil || ownerID != "u-1" {
			t.Fatalf("channel owner = %q, %v; want u-1", ownerID, err)
		}
		if err := stream.Close(); err != nil {
			t.Fatal(err)
		}
		if ownerID, err := production.Services.Sessions.ChannelOwner("final-close"); !errors.Is(err, session.ErrTabNotFound) {
			t.Fatalf("owner after final close = %q, %v; want ErrTabNotFound", ownerID, err)
		}
	})

	t.Run("stream capabilities survive owner wrapping", func(t *testing.T) {
		stream, err := production.Environment("").Streams.OpenJSON(ipc.WithUserID(context.Background(), "u-1"), ipc.ChannelRef{ID: "capabilities"})
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		trySend, ok := stream.(interface {
			TrySendJSON(context.Context, json.RawMessage) error
		})
		if !ok {
			t.Fatal("owner wrapping hid TrySendJSON")
		}
		if err := trySend.TrySendJSON(context.Background(), json.RawMessage(`{"ready":true}`)); err != nil {
			t.Fatal(err)
		}
		graceful, ok := stream.(interface{ CloseGracefully() error })
		if !ok {
			t.Fatal("owner wrapping hid CloseGracefully")
		}
		if err := graceful.CloseGracefully(); err != nil {
			t.Fatal(err)
		}
		ownerID, err := production.Services.Sessions.ChannelOwner("capabilities")
		if err != nil || ownerID != "u-1" {
			t.Fatalf("owner before graceful drain = %q, %v; want u-1", ownerID, err)
		}
		if err := stream.Close(); err != nil {
			t.Fatal(err)
		}
		if ownerID, err := production.Services.Sessions.ChannelOwner("capabilities"); !errors.Is(err, session.ErrTabNotFound) {
			t.Fatalf("owner after final close = %q, %v; want ErrTabNotFound", ownerID, err)
		}
	})

	t.Run("stale close preserves new generation", func(t *testing.T) {
		oldStream, err := production.Environment("").Streams.OpenJSON(ipc.WithUserID(context.Background(), "u-1"), ipc.ChannelRef{ID: "generation"})
		if err != nil {
			t.Fatal(err)
		}
		defer oldStream.Close()
		newStream, err := production.Environment("").Streams.OpenJSON(ipc.WithUserID(context.Background(), "u-1"), ipc.ChannelRef{ID: "generation"})
		if err != nil {
			t.Fatal(err)
		}
		defer newStream.Close()
		ownerID, err := production.Services.Sessions.ChannelOwner("generation")
		if err != nil || ownerID != "u-1" {
			t.Fatalf("new generation owner = %q, %v; want u-1", ownerID, err)
		}
		if err := oldStream.Close(); err != nil {
			t.Fatal(err)
		}
		ownerID, err = production.Services.Sessions.ChannelOwner("generation")
		if err != nil || ownerID != "u-1" {
			t.Fatalf("owner after stale close = %q, %v; want u-1", ownerID, err)
		}
		if err := newStream.SendJSON(context.Background(), json.RawMessage(`{"ready":true}`)); err != nil {
			t.Fatal(err)
		}
		if err := newStream.Close(); err != nil {
			t.Fatal(err)
		}
		if ownerID, err := production.Services.Sessions.ChannelOwner("generation"); !errors.Is(err, session.ErrTabNotFound) {
			t.Fatalf("owner after new generation close = %q, %v; want ErrTabNotFound", ownerID, err)
		}
	})

	t.Run("receiver detach preserves reconnect", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		stream, err := production.Environment("").Streams.OpenBinary(ipc.WithUserID(context.Background(), "u-1"), ipc.ChannelRef{ID: "reconnect"})
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		if err := stream.SendBinary(ctx, []byte("before-detach")); err != nil {
			t.Fatal(err)
		}
		channelURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws/channel/reconnect"
		connection, _, err := websocket.Dial(ctx, channelURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.CloseNow()
		_, data, err := connection.Read(ctx)
		if err != nil || string(data) != "before-detach" {
			t.Fatalf("initial frame = %q, %v", data, err)
		}
		if err := connection.Close(websocket.StatusNormalClosure, ""); err != nil {
			t.Fatal(err)
		}
		for production.Services.Sessions.Hub().Stats().LiveChannels != 0 {
			select {
			case <-ctx.Done():
				t.Fatal("receiver did not detach")
			case <-time.After(10 * time.Millisecond):
			}
		}
		ownerID, err := production.Services.Sessions.ChannelOwner("reconnect")
		if err != nil || ownerID != "u-1" {
			t.Fatalf("owner after receiver detach = %q, %v; want u-1", ownerID, err)
		}
		if err := stream.SendBinary(ctx, []byte("after-detach")); err != nil {
			t.Fatalf("production stream after receiver detach: %v", err)
		}
		reconnected, _, err := websocket.Dial(ctx, channelURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer reconnected.CloseNow()
		_, data, err = reconnected.Read(ctx)
		if err != nil || string(data) != "after-detach" {
			t.Fatalf("reconnected frame = %q, %v", data, err)
		}
	})
}

type productionChannelAccount struct {
	userID string
	cookie string
	csrf   string
}

type productionChannelFixture struct {
	production *Production
	httpServer *httptest.Server
	u1         productionChannelAccount
	u2         productionChannelAccount
}

func newProductionChannelFixture(t *testing.T) *productionChannelFixture {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	application, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{Logger: logger}, DataDir: t.TempDir(), Desktop: false,
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Dispatcher.RegisterRaw("test_open_binary", func(ctx context.Context, call *ipc.Call) (any, error) {
		stream, err := call.Streams.OpenBinary(ctx, call.Channel)
		if err != nil {
			return nil, terminalIPCError(err)
		}
		return nil, stream.Close()
	}); err != nil {
		t.Fatal(err)
	}
	if err := application.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Shutdown(context.Background()) })
	accounts := account.New(application.Services.Store.GORM())
	newAccount := func(username string) productionChannelAccount {
		user, err := accounts.CreateUser(context.Background(), username, "", "password-"+username)
		if err != nil {
			t.Fatal(err)
		}
		token, accountSession, err := accounts.IssueSession(context.Background(), user.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		return productionChannelAccount{userID: user.ID, cookie: token, csrf: remoteTestCSRF(accountSession.ID)}
	}
	fixture := &productionChannelFixture{production: application, u1: newAccount("u1"), u2: newAccount("u2")}
	transport, err := server.New(server.Config{
		Options:     server.Options{Listen: "127.0.0.1:0", DataDir: t.TempDir(), Auth: server.AuthOn},
		Dispatcher:  application.Dispatcher,
		Environment: application.Environment(""),
		Accounts:    accounts,
		SyncObjects: application.Services.Sync.ObjectHandler(),
		Channels:    server.NewHubAdapter(application.Services.Sessions.Hub()),
		Spectator:   application.Services.Sessions,
		Logger:      logger,
		WebSocket:   server.WebSocketConfig{WriteTimeout: 2 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	fixture.httpServer = httptest.NewServer(transport.Handler())
	t.Cleanup(fixture.httpServer.Close)
	return fixture
}

func (f *productionChannelFixture) rpc(t *testing.T, actor productionChannelAccount, command string, args any, channelID string) (int, ipc.Response) {
	t.Helper()
	argsJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	requestJSON, err := json.Marshal(ipc.Request{Command: command, Args: argsJSON, Channel: ipc.ChannelRef{ID: channelID}, ClientID: "integration-client"})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, f.httpServer.URL+"/rpc", bytes.NewReader(requestJSON))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Cookie", "nexterm_session="+actor.cookie)
	request.Header.Set("X-NexTerm-CSRF", actor.csrf)
	response, err := f.httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body ipc.Response
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body
}

func (f *productionChannelFixture) dial(t *testing.T, actor productionChannelAccount, channelID string) (*websocket.Conn, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(f.httpServer.URL, "http") + "/ws/channel/" + channelID
	conn, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": []string{"nexterm_session=" + actor.cookie}}})
	if err != nil {
		if response == nil {
			t.Fatalf("websocket dial: %v", err)
		}
		response.Body.Close()
		return nil, response.StatusCode
	}
	return conn, http.StatusSwitchingProtocols
}

func TestConcreteProductionChannelAccountIntegration(t *testing.T) {
	fixture := newProductionChannelFixture(t)

	t.Run("cross owner RPC cannot replace active producer", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		const channelID = "cross-owner-rpc"
		stream, err := fixture.production.Environment("").Streams.OpenBinary(ipc.WithUserID(context.Background(), fixture.u1.userID), ipc.ChannelRef{ID: channelID})
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		if err := stream.SendBinary(ctx, []byte("u1-before")); err != nil {
			t.Fatal(err)
		}
		status, body := fixture.rpc(t, fixture.u2, "test_open_binary", nil, channelID)
		if err := stream.SendBinary(ctx, []byte("u1-after")); err != nil {
			t.Fatalf("cross owner RPC replaced the u1 producer: %v", err)
		}
		if status != http.StatusOK || body.OK || body.Error == nil || body.Error.Code != ipc.CodeForbidden {
			t.Fatalf("cross owner RPC = %d %+v, want forbidden", status, body)
		}
		if conn, status := fixture.dial(t, fixture.u2, channelID); status != http.StatusForbidden {
			if conn != nil {
				_ = conn.CloseNow()
			}
			t.Fatalf("cross owner WS = %d, want 403", status)
		}
		conn, status := fixture.dial(t, fixture.u1, channelID)
		if status != http.StatusSwitchingProtocols {
			t.Fatalf("owner WS = %d, want 101", status)
		}
		defer conn.CloseNow()
		for _, want := range []string{"u1-before", "u1-after"} {
			_, data, err := conn.Read(ctx)
			if err != nil || string(data) != want {
				t.Fatalf("owner frame = %q, %v; want %q", data, err, want)
			}
		}
	})

	t.Run("terminal pre-subscribe detach and reattach", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		connected, err := fixture.production.Services.Sessions.Connect(ipc.WithUserID(context.Background(), fixture.u1.userID), session.Asset{ID: "account-local", Kind: session.KindLocal})
		if err != nil {
			t.Fatal(err)
		}
		const channelID = "terminal-pre-subscribe"
		if conn, status := fixture.dial(t, fixture.u1, channelID); status != http.StatusForbidden {
			if conn != nil {
				_ = conn.CloseNow()
			}
			t.Fatalf("unknown pre-subscribe WS = %d, want 403", status)
		}
		status, body := fixture.rpc(t, fixture.u1, "terminal_attach", map[string]any{"sessionId": connected.ID, "cols": 80, "rows": 24}, channelID)
		if status != http.StatusOK || !body.OK {
			t.Fatalf("terminal attach = %d %+v", status, body)
		}
		tabID := requireAttachedTabID(t, body)
		conn, status := fixture.dial(t, fixture.u1, channelID)
		if status != http.StatusSwitchingProtocols {
			t.Fatalf("post-attach WS = %d, want 101", status)
		}
		if _, _, err := conn.Read(ctx); err != nil {
			t.Fatal(err)
		}
		_ = conn.CloseNow()

		reconnected, status := fixture.dial(t, fixture.u1, channelID)
		if status != http.StatusSwitchingProtocols {
			t.Fatalf("detach reconnect WS = %d, want 101", status)
		}
		defer reconnected.CloseNow()
		status, body = fixture.rpc(t, fixture.u1, "terminal_attach_tab", map[string]any{"tabId": tabID, "replayBytes": -1}, channelID)
		if status != http.StatusOK || !body.OK {
			t.Fatalf("terminal reattach = %d %+v", status, body)
		}
		const marker = "s1b2-reattach-replay"
		if err := fixture.production.Services.Sessions.Write(ipc.WithUserID(context.Background(), fixture.u1.userID), tabID, "integration-client", []byte("echo "+marker+"\n")); err != nil {
			t.Fatal(err)
		}
		for {
			_, data, err := reconnected.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), marker) {
				break
			}
		}
		_ = reconnected.CloseNow()
		if err := fixture.production.Services.Sessions.CloseTab(tabID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.production.Services.Sessions.ChannelOwner(channelID); !errors.Is(err, session.ErrTabNotFound) {
			t.Fatalf("owner after tab close = %v, want ErrTabNotFound", err)
		}
		if conn, status := fixture.dial(t, fixture.u1, channelID); status != http.StatusForbidden {
			if conn != nil {
				_ = conn.CloseNow()
			}
			t.Fatalf("closed tab WS = %d, want 403", status)
		}
	})

	t.Run("binary graceful drain", func(t *testing.T) {
		testProductionGracefulDrain(t, fixture, "binary-graceful-drain", false)
	})
	t.Run("json graceful drain", func(t *testing.T) {
		testProductionGracefulDrain(t, fixture, "json-graceful-drain", true)
	})
}

func testProductionGracefulDrain(t *testing.T, fixture *productionChannelFixture, channelID string, useJSON bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var send func(context.Context, []byte) error
	var closeStream func() error
	var closeGracefully func() error
	var first, after, huge []byte
	if useJSON {
		stream, err := fixture.production.Environment("").Streams.OpenJSON(ipc.WithUserID(context.Background(), fixture.u1.userID), ipc.ChannelRef{ID: channelID})
		if err != nil {
			t.Fatal(err)
		}
		closeStream = stream.Close
		graceful, ok := stream.(interface{ CloseGracefully() error })
		if !ok {
			t.Fatal("JSON stream lost CloseGracefully")
		}
		closeGracefully = graceful.CloseGracefully
		send = func(ctx context.Context, data []byte) error { return stream.SendJSON(ctx, json.RawMessage(data)) }
		first = []byte(`{"seq":0}`)
		after = []byte(`{"seq":1}`)
		huge = []byte("\"" + strings.Repeat("x", 8<<20) + "\"")
	} else {
		stream, err := fixture.production.Environment("").Streams.OpenBinary(ipc.WithUserID(context.Background(), fixture.u1.userID), ipc.ChannelRef{ID: channelID})
		if err != nil {
			t.Fatal(err)
		}
		closeStream = stream.Close
		graceful, ok := stream.(interface{ CloseGracefully() error })
		if !ok {
			t.Fatal("binary stream lost CloseGracefully")
		}
		closeGracefully = graceful.CloseGracefully
		send = stream.SendBinary
		first = []byte("first")
		after = []byte("after")
		huge = bytes.Repeat([]byte{0xab}, 8<<20)
	}
	defer closeStream()
	for _, data := range [][]byte{first, huge, after} {
		if err := send(ctx, data); err != nil {
			t.Fatal(err)
		}
	}
	conn, status := fixture.dial(t, fixture.u1, channelID)
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("initial WS = %d, want 101", status)
	}
	_, data, err := conn.Read(ctx)
	if err != nil || !bytes.Equal(data, first) {
		t.Fatalf("first frame = %d bytes, %v", len(data), err)
	}
	if err := closeGracefully(); err != nil {
		t.Fatal(err)
	}
	ownerID, err := fixture.production.Services.Sessions.ChannelOwner(channelID)
	if err != nil || ownerID != fixture.u1.userID {
		t.Fatalf("owner after CloseGracefully = %q, %v; owner must survive drain", ownerID, err)
	}
	_ = conn.CloseNow()
	if denied, status := fixture.dial(t, fixture.u2, channelID); status != http.StatusForbidden {
		if denied != nil {
			_ = denied.CloseNow()
		}
		t.Fatalf("foreign drain WS = %d, want 403", status)
	}
	reconnected, status := fixture.dial(t, fixture.u1, channelID)
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("drain reconnect WS = %d, want 101", status)
	}
	defer reconnected.CloseNow()
	reconnected.SetReadLimit(32 << 20)
	_, data, err = reconnected.Read(ctx)
	if err != nil || !bytes.Equal(data, huge) {
		t.Fatalf("in-flight frame = %d bytes, %v; want %d", len(data), err, len(huge))
	}
	_, data, err = reconnected.Read(ctx)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatalf("final frame = %q, %v", data, err)
	}
	for {
		_, err := fixture.production.Services.Sessions.ChannelOwner(channelID)
		if errors.Is(err, session.ErrTabNotFound) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("owner survived final drain: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if closed, status := fixture.dial(t, fixture.u1, channelID); status != http.StatusForbidden {
		if closed != nil {
			_ = closed.CloseNow()
		}
		t.Fatalf("fully drained WS = %d, want 403", status)
	}
}

// An anomalous ownerless session on a non-open server must fail closed: the
// transcript writer refuses to record an ownerless row every account could
// read. Desktop deployments keep the legacy ownerless behavior.
func TestConcreteProductionTranscriptOwnerlessSessionFailsClosed(t *testing.T) {
	var logs syncedLogBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config:    Config{Logger: logger},
		DataDir:   t.TempDir(),
		Desktop:   false,
		Connector: transcriptTestConnector(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := production.Services.Sessions.Connect(context.Background(), session.Asset{ID: "anomaly-asset", Kind: session.KindSSH}); err != nil {
		t.Fatal(err)
	}
	waitForLogCount(t, &logs, "failing closed instead of ownerless", 1)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := production.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(context.Background(), filepath.Join(production.Services.dataDir, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	rows, err := reopened.TranscriptListByAsset(context.Background(), "anomaly-asset", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("ownerless transcript rows persisted on a non-open server: %+v", rows)
	}
}

func TestConcreteProductionDesktopOwnerlessSessionWritesLegacyRow(t *testing.T) {
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config:    Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		DataDir:   t.TempDir(),
		Desktop:   true,
		Connector: transcriptTestConnector(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := production.Services.Sessions.Connect(context.Background(), session.Asset{ID: "legacy-asset", Kind: session.KindSSH}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	var rows []store.TranscriptRow
	for time.Now().Before(deadline) {
		rows, err = production.Services.Store.TranscriptListByAsset(context.Background(), "legacy-asset", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(rows) != 1 || rows[0].OwnerID != "" {
		t.Fatalf("desktop ownerless session must keep the legacy row: %+v", rows)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := production.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
}

// The composition stops the transcript writer before the store, so Shutdown
// returns only after the retention loop has stopped and joined: no retention
// run may write to the store or the logger after Shutdown returned, and the
// store closes only after every retention write happened.
func TestConcreteProductionShutdownJoinsTranscriptRetention(t *testing.T) {
	var logs syncedLogBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config:  Config{Logger: logger},
		DataDir: t.TempDir(),
		Desktop: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitForLogCount(t, &logs, "scheduled retention cleanup finished", 1)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := production.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	frozen := strings.Count(logs.String(), "scheduled retention cleanup finished")
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := strings.Count(logs.String(), "scheduled retention cleanup finished"); got != frozen {
			t.Fatalf("retention wrote after Shutdown returned: count went %d -> %d", frozen, got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := production.Services.Store.TranscriptHosts(context.Background()); err == nil {
		t.Fatal("store must be closed by shutdown after the retention loop joined")
	}
}
