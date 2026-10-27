//go:build darwin || linux

package production

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/skills"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	fleetserver "github.com/Hello-CTF/NexTerm/internal/fleet/server"
	"github.com/Hello-CTF/NexTerm/internal/forward"
	"github.com/Hello-CTF/NexTerm/internal/fs/chunked"
	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
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

func TestDurableTranscriptAdaptersEmptyOwnerRoundtrip(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	offsets := durableTranscriptOffsets{database: database}
	const tabID = "desktop-open-tab"

	offsets.PersistDurableTranscriptOffsetContext(ctx, tabID, 41)
	if got := offsets.DurableTranscriptCatchUpBytesContext(ctx, tabID); got != 41 {
		t.Fatalf("empty-owner offset = %d, want 41", got)
	}
	var offsetRow store.DurableTranscriptOffset
	if err := database.GORM().Where("durable_id = ?", tabID).Take(&offsetRow).Error; err != nil {
		t.Fatal(err)
	}
	if offsetRow.OwnerID != "" {
		t.Fatalf("empty-owner offset row owner = %q, want empty", offsetRow.OwnerID)
	}

	offsets.DeleteDurableTranscriptOffsetContext(ctx, tabID)
	if got := offsets.DurableTranscriptCatchUpBytesContext(ctx, tabID); got != 0 {
		t.Fatalf("empty-owner offset after delete = %d, want 0", got)
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

// forwardFSTransport couples the forward dial test double with a real local
// filesystem so composition tests can exercise both forward rules and
// chunked-upload staging through one session.
type forwardFSTransport struct {
	fakeDialTransport
	fs base.FileSystem
}

func (t *forwardFSTransport) FileSystem(context.Context) (base.FileSystem, error) {
	return t.fs, nil
}

func newForwardFSProduction(t *testing.T, upstream string, desktop bool, reaperInterval time.Duration) *Production {
	t.Helper()
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config:  Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		DataDir: t.TempDir(),
		Desktop: desktop,
		Connector: session.ConnectorFunc(func(_ context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
			return &forwardFSTransport{
				fakeDialTransport: fakeDialTransport{
					fakeSSHTransport: fakeSSHTransport{kind: asset.Kind, generation: generation},
					upstream:         upstream,
				},
				fs: fslocal.New(),
			}, nil
		}),
		ForwardReaperInterval: reaperInterval,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	return production
}

// The connected lifecycle drives the forward runtime through the session
// status emitter: an auto-start rule owned by the connecting account must
// start with the trusted owner restored from the event context.
func TestConcreteProductionForwardRuntimeConnectedStartsAutoStartRules(t *testing.T) {
	production := newForwardFSProduction(t, startEchoListener(t), false, 0)
	ownerCtx := ipc.WithUserID(context.Background(), "builder-owner")
	asset, err := production.Services.Store.AssetCreate(ownerCtx, store.AssetInput{Kind: "ssh", Name: "builder-host", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	rule, err := production.Services.Store.ForwardRuleSave(ownerCtx, store.ForwardRuleInput{
		AssetID: asset.ID, Name: "database", ListenPort: freeRuntimePort(t),
		TargetHost: "remote.internal", TargetPort: 4567, AutoStart: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := production.Services.Sessions.Connect(ownerCtx, session.Asset{ID: asset.ID, Kind: session.KindSSH}); err != nil {
		t.Fatal(err)
	}

	waitForCondition(t, 5*time.Second, func() bool {
		runtimes := production.Services.Forward.ListFor(ownerCtx)
		return len(runtimes) == 1 && runtimes[0].ID == rule.ID && runtimes[0].Status == forward.StatusActive
	})
	conn := assertForwardEcho(t, production.Services.Forward.ListFor(ownerCtx)[0], "builder-auto-start")
	_ = conn.Close()
}

func TestConcreteProductionForwardRecoveryUsesOwnerProductionEnvironments(t *testing.T) {
	production := newForwardFSProduction(t, startEchoListener(t), false, 0)
	database := production.Services.Store
	ownerCtx := ipc.WithUserID(t.Context(), "forward-owner")
	otherCtx := ipc.WithUserID(t.Context(), "forward-other")
	if err := guard.SetProductionEnvironments(ownerCtx, database, []string{"live"}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		ctx  context.Context
		skip bool
	}{
		{"configured owner", ownerCtx, true},
		{"other owner", otherCtx, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			asset, err := database.AssetCreate(test.ctx, store.AssetInput{Kind: "ssh", Name: "live", Tags: "env:live"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.ForwardRuleSave(test.ctx, store.ForwardRuleInput{
				AssetID: asset.ID, Name: "auto", ListenPort: freeRuntimePort(t),
				TargetHost: "remote.internal", TargetPort: 4567, AutoStart: true,
			}); err != nil {
				t.Fatal(err)
			}
			connects := 0
			connectErr := errors.New("connection observed")
			outcomes := production.Services.Forward.RecoverAutoStartRules(test.ctx, []forward.RecoveryDevice{{
				AssetID: asset.ID,
				Connect: func(context.Context) (string, error) {
					connects++
					return "", connectErr
				},
			}})
			if len(outcomes) != 1 {
				t.Fatalf("recovery outcomes = %+v", outcomes)
			}
			if test.skip {
				if connects != 0 || outcomes[0].Skipped == "" || outcomes[0].Err != nil {
					t.Fatalf("configured production recovery connected %d times: %+v", connects, outcomes)
				}
			} else if connects != 1 || !errors.Is(outcomes[0].Err, connectErr) {
				t.Fatalf("unconfigured owner's recovery = %+v, connects=%d", outcomes, connects)
			}
		})
	}
}

// The composition reaper sweeps the registry's known staging directories: a
// stale crashed group sharing a directory with an active upload is removed
// while the fresh in-flight staging is kept.
func TestConcreteProductionForwardRuntimeReaperSweepsStaleStrangers(t *testing.T) {
	production := newForwardFSProduction(t, startEchoListener(t), true, 5*time.Millisecond)
	ctx := context.Background()
	connected, err := production.Services.Sessions.Connect(ctx, session.Asset{ID: "ssh-asset", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	remote := filepath.Join(dir, "active.bin")
	input := beginChunkIPC(t, production.Dispatcher, ctx, fsChunkUploadRequest{
		SessionID: connected.ID, RemotePath: remote, TaskID: "active-task", AttemptID: "active-attempt", TotalSize: 8, ChunkSize: 4,
	})
	decodeFSTransferTest[fsChunkUploadProgressDTO](t, dispatchFSTransferTest(production.Dispatcher, ctx, "fs_upload_chunk", chunkIPCRequest(input, 0, "abcd"), nil))

	strangerRemote := filepath.Join(dir, "crashed.bin")
	stranger, err := chunked.New("00000000000000000000000099", strangerRemote, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	destination := fslocal.New()
	if _, err := stranger.WriteChunk(context.Background(), destination, runtimeChunk(0, "wxyz")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-25 * time.Hour)
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(info.Name(), "00000000000000000000000099") {
			return os.Chtimes(path, old, old)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	waitForCondition(t, 5*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(dir, ".crashed.bin.nexterm-00000000000000000000000099.manifest"))
		return errors.Is(err, os.ErrNotExist)
	})
	if _, err := os.Stat(filepath.Join(dir, ".active.bin.nexterm-"+input.UploadID+".manifest")); err != nil {
		t.Fatalf("active upload staging was reaped: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".active.bin.nexterm-"+input.UploadID+".part-000000000000")); err != nil {
		t.Fatalf("active upload part was reaped: %v", err)
	}
}

// Crash recovery through the real composition: after the registry loses the
// live entry, the next begin adopts the crashed staging on the destination
// and keeps the original upload ID instead of duplicating the group.
func TestConcreteProductionFSUploadBeginResumesCrashedStaging(t *testing.T) {
	production := newForwardFSProduction(t, startEchoListener(t), true, 0)
	ctx := context.Background()
	connected, err := production.Services.Sessions.Connect(ctx, session.Asset{ID: "ssh-asset", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(t.TempDir(), "remote.bin")
	first := beginChunkIPC(t, production.Dispatcher, ctx, fsChunkUploadRequest{
		SessionID: connected.ID, RemotePath: remote, TaskID: "crash-task", AttemptID: "crash-attempt", TotalSize: 8, ChunkSize: 4,
	})
	decodeFSTransferTest[fsChunkUploadProgressDTO](t, dispatchFSTransferTest(production.Dispatcher, ctx, "fs_upload_chunk", chunkIPCRequest(first, 0, "abcd"), nil))

	// Simulate the crash: the entry vanishes with the process while the
	// staging files stay on the destination.
	production.Services.fsUploads.mu.Lock()
	for key := range production.Services.fsUploads.entries {
		if key.uploadID == first.UploadID {
			delete(production.Services.fsUploads.entries, key)
		}
	}
	production.Services.fsUploads.mu.Unlock()

	second := beginChunkIPC(t, production.Dispatcher, ctx, fsChunkUploadRequest{
		SessionID: connected.ID, RemotePath: remote, TaskID: "resume-task", AttemptID: "resume-attempt", TotalSize: 8, ChunkSize: 4,
	})
	if second.UploadID != first.UploadID {
		t.Fatalf("resumed begin = %q, want crashed upload %q", second.UploadID, first.UploadID)
	}
	// Staged chunks are re-presented and verified before commit; the matching
	// chunk deduplicates without changing progress.
	replayed := decodeFSTransferTest[fsChunkUploadProgressDTO](t, dispatchFSTransferTest(production.Dispatcher, ctx, "fs_upload_chunk", chunkIPCRequest(second, 0, "abcd"), nil))
	if replayed.Received != 4 {
		t.Fatalf("re-presented chunk progress = %+v, want received 4", replayed)
	}
	progress := decodeFSTransferTest[fsChunkUploadProgressDTO](t, dispatchFSTransferTest(production.Dispatcher, ctx, "fs_upload_chunk", chunkIPCRequest(second, 1, "efgh"), nil))
	if progress.Received != 8 {
		t.Fatalf("resumed progress = %+v, want received 8", progress)
	}
	committed := decodeFSTransferTest[int64](t, dispatchFSTransferTest(production.Dispatcher, ctx, "fs_upload_commit", second, nil))
	if committed != 8 {
		t.Fatalf("committed = %d, want 8", committed)
	}
	data, err := os.ReadFile(remote)
	if err != nil || string(data) != "abcdefgh" {
		t.Fatalf("committed target = %q, %v", data, err)
	}
	assertNoChunkIArtifacts(t, remote)
}

// A crashed upload's staging directory is persisted at begin, so the composed
// reaper still sweeps the leftover after the registry entry is gone — no live
// entry required — once the asset has a live session again.
func TestConcreteProductionForwardRuntimeReaperSweepsPersistedTargetsAfterCrash(t *testing.T) {
	production := newForwardFSProduction(t, startEchoListener(t), true, 5*time.Millisecond)
	ctx := context.Background()
	connected, err := production.Services.Sessions.Connect(ctx, session.Asset{ID: "ssh-asset", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.bin")
	input := beginChunkIPC(t, production.Dispatcher, ctx, fsChunkUploadRequest{
		SessionID: connected.ID, RemotePath: remote, TaskID: "crash-task", AttemptID: "crash-attempt", TotalSize: 8, ChunkSize: 4,
	})
	decodeFSTransferTest[fsChunkUploadProgressDTO](t, dispatchFSTransferTest(production.Dispatcher, ctx, "fs_upload_chunk", chunkIPCRequest(input, 0, "abcd"), nil))

	// Simulate the crash: the entry vanishes while the staging files stay.
	production.Services.fsUploads.mu.Lock()
	for key := range production.Services.fsUploads.entries {
		if key.uploadID == input.UploadID {
			delete(production.Services.fsUploads.entries, key)
		}
	}
	production.Services.fsUploads.mu.Unlock()

	old := time.Now().Add(-25 * time.Hour)
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, old, old)
	}); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 5*time.Second, func() bool {
		matches, err := filepath.Glob(filepath.Join(dir, ".remote.bin.nexterm-*"))
		return err == nil && len(matches) == 0
	})
}

// Through the real production composition (NewProduction + server.New with
// the fleet service mounted), the removed legacy /share/public/{token} route
// answers 410 with no-store for plain GETs and WS upgrades alike and never
// reaches the static entry, while the root page is unchanged.
func TestConcreteProductionRetiredPublicShareRouteGone(t *testing.T) {
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
	accounts := account.New(production.Services.Store.GORM())
	_, err = accounts.CreateUser(context.Background(), "a03-owner", "", "password-a03")
	if err != nil {
		t.Fatal(err)
	}
	fleetService, err := fleetserver.New(fleetserver.Config{
		DB: production.Services.Store.GORM(), Accounts: accounts, AuthOff: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fleetService.Close() })

	const spaPage = "<html><head><title>NexTerm</title></head><body>a03-spa</body></html>"
	var gotPath string
	staticStub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(spaPage))
	})
	api, err := server.New(server.Config{
		Options:     server.Options{Listen: "127.0.0.1:0", DataDir: t.TempDir(), Auth: server.AuthOn},
		Dispatcher:  production.Dispatcher,
		Environment: production.Environment(""),
		Accounts:    accounts,
		SyncObjects: production.Services.Sync.ObjectHandler(),
		Channels:    server.NewHubAdapter(production.Services.Sessions.Hub()),
		Spectator:   production.Services.Sessions,
		Logger:      logger,
		Fleet:       fleetService,
		Static:      staticStub,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(api.Handler())
	t.Cleanup(httpServer.Close)

	get := func(path string) (int, string, string) {
		t.Helper()
		response, err := httpServer.Client().Get(httpServer.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, string(body), response.Header.Get("Cache-Control")
	}

	if status, body, _ := get("/"); status != http.StatusOK || body != spaPage {
		t.Fatalf("root page = %d %q", status, body)
	}
	gotPath = ""
	if status, _, cache := get("/share/public/some-token"); status != http.StatusGone || cache != "no-store" {
		t.Fatalf("legacy public page = %d %q, want 410 no-store", status, cache)
	}
	if gotPath != "" {
		t.Fatalf("legacy public page reached the static entry with path %q", gotPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, handshake, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/share/public/some-token", nil)
	if err == nil {
		t.Fatal("legacy public WS handshake succeeded")
	}
	if handshake == nil || handshake.StatusCode != http.StatusGone {
		t.Fatalf("legacy public WS handshake = %+v, want 410", handshake)
	}
}

// TestConcreteProductionSharesSingleSkillsManager proves the concrete
// production assembly builds one skills manager and hands the same instance
// to both the ai-skills IPC surface and the agent tools registry: the
// services-level manager is set, ai_skills_* commands are registered, and
// the composed runner exposes load_skill backed by that manager.
func TestConcreteProductionSharesSingleSkillsManager(t *testing.T) {
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{Logger: slog.Default()}, DataDir: t.TempDir(), Desktop: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = production.Shutdown(ctx) })

	manager := production.Services.Skills
	if manager == nil {
		t.Fatal("production services carry no skills manager")
	}
	for _, command := range []string{
		skills.CommandList, skills.CommandSaveOverride, skills.CommandDeleteOverride, skills.CommandDispatch, skills.CommandMaterial,
	} {
		if !slices.Contains(production.Dispatcher.Commands(), command) {
			t.Fatalf("production is missing skills command %s", command)
		}
	}
	registry := production.Services.Agent.ToolsRegistry()
	if registry == nil {
		t.Fatal("the composed runner has no tools registry")
	}
	body := "production-shared-instance"
	description := "shared instance evidence"
	if _, err := manager.SaveOverride(t.Context(), skills.OverrideInput{Name: "ops-note", Description: &description, Body: &body}); err != nil {
		t.Fatal(err)
	}
	call := tools.Call{ID: "call-1", Name: "load_skill", Args: json.RawMessage(`{"name":"ops-note"}`)}
	if err := registry.Validate(call); err != nil {
		t.Fatalf("load_skill is not exposed by the production registry: %v", err)
	}
	viaRegistry := registry.Execute(t.Context(), "job-1", tools.Scope{}, call, nil)
	viaManager := manager.LoadSkill(t.Context(), call.Args)
	if !viaRegistry.OK || !viaManager.OK || viaRegistry.Text != viaManager.Text {
		t.Fatalf("load_skill outputs diverge: registry=%+v manager=%+v", viaRegistry, viaManager)
	}
}

// dbTunnelCompositionTransport is an SSH transport double whose DialContext
// reaches a fixed upstream; Close kills every connection it vended, like a
// real SSH session drop kills its direct-tcpip channels.
type dbTunnelCompositionTransport struct {
	kind       string
	generation uint64
	upstream   string
	alive      atomic.Bool
	dialed     atomic.Int32
	mu         sync.Mutex
	conns      map[*dbTunnelCompositionConn]struct{}
}

func newDBTunnelCompositionTransport(kind string, generation uint64, upstream string) *dbTunnelCompositionTransport {
	transport := &dbTunnelCompositionTransport{kind: kind, generation: generation, upstream: upstream, conns: make(map[*dbTunnelCompositionConn]struct{})}
	transport.alive.Store(true)
	return transport
}

func (t *dbTunnelCompositionTransport) Kind() string       { return t.kind }
func (t *dbTunnelCompositionTransport) Generation() uint64 { return t.generation }
func (t *dbTunnelCompositionTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{}, nil
}
func (t *dbTunnelCompositionTransport) Ping(context.Context) (time.Duration, error) { return 0, nil }
func (t *dbTunnelCompositionTransport) IsAlive() bool                               { return t.alive.Load() }
func (t *dbTunnelCompositionTransport) Close() error {
	t.alive.Store(false)
	t.mu.Lock()
	conns := make([]*dbTunnelCompositionConn, 0, len(t.conns))
	for conn := range t.conns {
		conns = append(conns, conn)
	}
	t.mu.Unlock()
	for _, conn := range conns {
		conn.kill()
	}
	return nil
}
func (t *dbTunnelCompositionTransport) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	if !t.alive.Load() {
		return nil, base.ErrDisconnected
	}
	var dialer net.Dialer
	raw, err := dialer.DialContext(ctx, "tcp", t.upstream)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	if !t.alive.Load() {
		t.mu.Unlock()
		_ = raw.Close()
		return nil, base.ErrDisconnected
	}
	var conn *dbTunnelCompositionConn
	conn = &dbTunnelCompositionConn{Conn: raw, release: func() {
		t.mu.Lock()
		delete(t.conns, conn)
		t.mu.Unlock()
	}}
	t.conns[conn] = struct{}{}
	t.mu.Unlock()
	t.dialed.Add(1)
	return conn, nil
}

// dbTunnelCompositionConn models an SSH channel: a session drop kills it, and
// the client-side Close that comes later is a no-op rather than an error.
type dbTunnelCompositionConn struct {
	net.Conn
	killed  atomic.Bool
	once    sync.Once
	release func()
}

func (c *dbTunnelCompositionConn) kill() {
	c.killed.Store(true)
	_ = c.Conn.Close()
	c.once.Do(c.release)
}

func (c *dbTunnelCompositionConn) Close() error {
	if c.killed.Load() {
		c.once.Do(c.release)
		return nil
	}
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// startDBTunnelCompositionRESP serves just enough RESP2 for the real go-redis
// client handshake: HELLO is refused like a pre-6.0 server so the client falls
// back to legacy AUTH, PING gets +PONG, everything else gets +OK. It exercises
// NexTerm's own tunnel wiring only; it is not a stand-in for the flagged
// real-Redis integration tests.
func startDBTunnelCompositionRESP(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var conns sync.Map
	serve := func(conn net.Conn) {
		defer conn.Close()
		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadString('\n')
			if err != nil || !strings.HasPrefix(line, "*") {
				return
			}
			count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
			if err != nil {
				return
			}
			command := ""
			for i := 0; i < count; i++ {
				bulk, err := reader.ReadString('\n')
				if err != nil || !strings.HasPrefix(bulk, "$") {
					return
				}
				length, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(bulk, "$")))
				if err != nil {
					return
				}
				payload := make([]byte, length+2)
				if _, err := io.ReadFull(reader, payload); err != nil {
					return
				}
				if i == 0 {
					command = strings.ToUpper(string(payload[:length]))
				}
			}
			reply := "+OK\r\n"
			switch command {
			case "HELLO":
				reply = "-ERR unknown command 'HELLO'\r\n"
			case "PING":
				reply = "+PONG\r\n"
			}
			if _, err := conn.Write([]byte(reply)); err != nil {
				return
			}
		}
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conns.Store(conn, struct{}{})
			go func() {
				serve(conn)
				conns.Delete(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		conns.Range(func(key, _ any) bool {
			_ = key.(net.Conn).Close()
			return true
		})
	})
	return listener.Addr().String()
}

// The DB connection asset identity through the real production composition
// root: NewProduction wires the store resolver, the vault decryptor, and the
// session-backed SSH tunnel dialer into one db.Service, and the identity the
// grant layer resolves is the DB asset the connection was resolved from —
// never the tunnel viaAssetId, never an inline handle. Owner scoping, stale
// tunnel redial through a replacement session, and account-session logout all
// behave the same as at the unit level.
func TestConcreteProductionDatabaseTunnelIdentityComposition(t *testing.T) {
	upstream := startDBTunnelCompositionRESP(t)
	var mu sync.Mutex
	var transports []*dbTunnelCompositionTransport
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config:  Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		DataDir: t.TempDir(),
		Desktop: true,
		Connector: session.ConnectorFunc(func(_ context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
			transport := newDBTunnelCompositionTransport(asset.Kind, generation, upstream)
			mu.Lock()
			transports = append(transports, transport)
			mu.Unlock()
			return transport, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })

	ownerA := ipc.WithUserID(context.Background(), "owner-a")
	loginA := ipc.WithAccountSessionID(ownerA, "login-a")
	ownerB := ipc.WithUserID(context.Background(), "owner-b")
	dispatch := func(ctx context.Context, command, args string) ipc.Response {
		t.Helper()
		return production.Dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
	}

	if _, ok := any(production.Services.Database).(server.AccountConnectionCloser); !ok {
		t.Fatal("composition database service does not close account sessions; server logout wiring has no seam")
	}

	response := dispatch(ownerA, "vault_set_credential", `{"args":{"name":"cache-password","kind":"password","secret":"s3cret"}}`)
	var credential map[string]string
	requireStoreTestResponse(t, response, &credential)
	credentialID := credential["id"]
	via, err := production.Services.Store.AssetCreate(ownerA, store.AssetInput{Kind: session.KindSSH, Name: "jump", OptionsJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	host := "redis.tunnel.invalid"
	port := int32(6379)
	optionsJSON, err := json.Marshal(map[string]any{"database": "0", "viaAssetId": via.ID})
	if err != nil {
		t.Fatal(err)
	}
	target, err := production.Services.Store.AssetCreate(ownerA, store.AssetInput{
		Kind: "redis", Name: "cache", Host: &host, Port: &port, CredID: &credentialID, OptionsJSON: string(optionsJSON),
	})
	if err != nil {
		t.Fatal(err)
	}

	// The production surface cannot open identity-less inline handles.
	inline := dispatch(ownerA, "db_connect", `{"args":{"inline":{"kind":"redis","host":"redis.tunnel.invalid"}}}`)
	if inline.OK || inline.Error == nil || inline.Error.Code != ipc.CodeBadParam {
		t.Fatalf("inline connect through production = %+v, want bad_param", inline)
	}

	// Without a connected jump session the tunnel surfaces disconnected.
	noSession := dispatch(loginA, "db_connect", `{"args":{"assetId":"`+target.ID+`"}}`)
	if noSession.OK || noSession.Error == nil || noSession.Error.Code != ipc.CodeDisconnected {
		t.Fatalf("connect without jump session = %+v, want disconnected", noSession)
	}

	sshSession, err := production.Services.Sessions.Connect(ownerA, session.Asset{ID: via.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	response = dispatch(loginA, "db_connect", `{"args":{"assetId":"`+target.ID+`"}}`)
	var connected struct {
		ConnID string `json:"connId"`
	}
	requireStoreTestResponse(t, response, &connected)

	// The grant-identity seam resolves the DB asset, never the tunnel asset,
	// for both the direct service lookup and the production scope resolver.
	if assetID, ok := production.Services.Database.AssetIDOf(loginA, connected.ConnID); !ok || assetID != target.ID {
		t.Fatalf("AssetIDOf = %q, %v; want the DB asset %q, true", assetID, ok, target.ID)
	}
	if assetID, ok := production.Services.Database.AssetIDOf(ownerB, connected.ConnID); ok {
		t.Fatalf("cross-owner AssetIDOf resolved %q; want explicit none", assetID)
	}
	resolved, err := resolveScopeToolAsset(loginA, production.Services.Database, production.Services.Sessions, tools.Scope{ConnID: connected.ConnID}, "db_query")
	if err != nil || resolved != target.ID {
		t.Fatalf("resolveScopeToolAsset = %q, %v; want the DB asset %q", resolved, err, target.ID)
	}
	if resolved, err := resolveScopeToolAsset(loginA, production.Services.Database, production.Services.Sessions, tools.Scope{}, "terminal_write"); err != nil || resolved != "" {
		t.Fatalf("session-less terminal scope resolved %q, %v; want no invented identity", resolved, err)
	}

	response = dispatch(loginA, "redis_command", `{"connId":"`+connected.ConnID+`","args":["PING"]}`)
	var pong string
	requireStoreTestResponse(t, response, &pong)
	if pong != "PONG" {
		t.Fatalf("PING through the composed tunnel = %q", pong)
	}
	crossOwner := dispatch(ownerB, "redis_command", `{"connId":"`+connected.ConnID+`","args":["PING"]}`)
	if crossOwner.OK || crossOwner.Error == nil || crossOwner.Error.Code != ipc.CodeForbidden {
		t.Fatalf("cross-owner redis_command = %+v, want forbidden", crossOwner)
	}

	// Stale tunnel: killing the jump session kills the live connection's
	// channel, and a replacement session carries the redial while the recorded
	// DB asset identity stays put.
	if err := production.Services.Sessions.Disconnect(sshSession.ID); err != nil {
		t.Fatal(err)
	}
	if stale := dispatch(loginA, "redis_command", `{"connId":"`+connected.ConnID+`","args":["PING"]}`); stale.OK {
		t.Fatal("redis_command survived the killed jump session")
	}
	if _, err := production.Services.Sessions.Connect(ownerA, session.Asset{ID: via.ID, Kind: session.KindSSH}); err != nil {
		t.Fatal(err)
	}
	response = dispatch(loginA, "redis_command", `{"connId":"`+connected.ConnID+`","args":["PING"]}`)
	pong = ""
	requireStoreTestResponse(t, response, &pong)
	if pong != "PONG" {
		t.Fatalf("PING after replacement jump session = %q", pong)
	}
	mu.Lock()
	if len(transports) != 2 || transports[1].dialed.Load() == 0 {
		t.Fatalf("tunnel redial used transports %d/%d", len(transports), func() int32 {
			if len(transports) > 1 {
				return transports[1].dialed.Load()
			}
			return 0
		}())
	}
	mu.Unlock()
	if assetID, ok := production.Services.Database.AssetIDOf(loginA, connected.ConnID); !ok || assetID != target.ID {
		t.Fatalf("AssetIDOf after tunnel redial = %q, %v; want %q, true", assetID, ok, target.ID)
	}

	// Logout: the account-session seam drops the handle and its identity.
	if err := production.Services.Database.CloseAccountSession("login-a"); err != nil {
		t.Fatal(err)
	}
	if assetID, ok := production.Services.Database.AssetIDOf(loginA, connected.ConnID); ok {
		t.Fatalf("AssetIDOf survived logout: %q", assetID)
	}
	if after := dispatch(loginA, "redis_command", `{"connId":"`+connected.ConnID+`","args":["PING"]}`); after.OK || after.Error == nil || after.Error.Code != ipc.CodeNotFound {
		t.Fatalf("redis_command after logout = %+v, want not_found", after)
	}
}
