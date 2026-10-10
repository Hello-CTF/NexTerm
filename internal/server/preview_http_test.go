package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/sharing"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
	"github.com/coder/websocket"
)

type previewFixture struct {
	*accountFixture
	manager  *session.Manager
	previews *sharing.Service
}

// newPreviewFixture 装配带只读预览的完整 server: 真 session Manager (本地 sh)
// 经 hub adapter 供输出通道, 真 sharing.Service。
func newPreviewFixture(t *testing.T, mutate ...func(*Config)) *previewFixture {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	accounts := account.New(database.DB())
	previews, err := sharing.New(sharing.Config{DB: database.DB(), Accounts: accounts})
	if err != nil {
		t.Fatal(err)
	}
	connector := session.ConnectorFunc(func(_ context.Context, _ session.Asset, _ uint64) (base.Transport, error) {
		return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
	})
	manager := session.NewManager(session.Config{Connector: connector})
	t.Cleanup(func() { _ = manager.Close() })

	config := testConfig(t, false)
	config.Options.Auth = AuthOn
	config.Accounts = accounts
	config.Previews = previews
	config.Spectator = manager
	config.Channels = NewHubAdapter(manager.Hub())
	for _, apply := range mutate {
		apply(&config)
	}
	server, httpServer := newTestHTTP(t, config)
	return &previewFixture{
		accountFixture: &accountFixture{
			server: server, http: httpServer, accounts: accounts, db: database.DB(),
			client: &http.Client{},
		},
		manager:  manager,
		previews: previews,
	}
}

// openTab 以指定属主打开一个真实终端标签页 (本地 sh), 返回 tab ID。
func (f *previewFixture) openTab(t *testing.T, ownerID string) string {
	t.Helper()
	connectCtx := ipc.WithUserID(context.Background(), ownerID)
	connected, err := f.manager.Connect(connectCtx, session.Asset{ID: "preview-asset-" + ownerID, Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	info, err := f.manager.OpenTab(context.Background(), session.OpenTabOptions{
		SessionID: connected.ID, ClientID: "owner", ChannelID: "owner-" + ids.New(), Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	return info.ID
}

func (f *previewFixture) createUser(t *testing.T, username string) string {
	t.Helper()
	user, err := f.accounts.CreateUser(context.Background(), username, "", "password-"+username)
	if err != nil {
		t.Fatal(err)
	}
	return user.ID
}

func TestPreviewManagementEndpoints(t *testing.T) {
	f := newPreviewFixture(t)
	aliceID := f.createUser(t, "alice")
	f.createUser(t, "bob")
	alice := f.login(t, "alice", "password-alice")
	bob := f.login(t, "bob", "password-bob")
	tabID := f.openTab(t, aliceID)

	// 未登录 401; 缺 CSRF 403。
	if call := f.call(t, http.MethodPost, "/share/previews", map[string]any{"session_id": tabID}, nil, "", nil); call.status != http.StatusUnauthorized {
		t.Fatalf("anonymous create status = %d, want 401", call.status)
	}
	if call := f.call(t, http.MethodPost, "/share/previews", map[string]any{"session_id": tabID}, alice, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("missing csrf status = %d, want 403", call.status)
	}
	// 他人的终端: bob 403。
	if call := f.call(t, http.MethodPost, "/share/previews", map[string]any{"session_id": tabID}, bob, bob.csrf, nil); call.status != http.StatusForbidden {
		t.Fatalf("non-owner create status = %d, want 403", call.status)
	}
	// 不存在的标签页: 404。
	if call := f.call(t, http.MethodPost, "/share/previews", map[string]any{"session_id": ids.New()}, alice, alice.csrf, nil); call.status != http.StatusNotFound {
		t.Fatalf("missing tab create status = %d, want 404", call.status)
	}

	call := f.call(t, http.MethodPost, "/share/previews", map[string]any{"session_id": tabID, "ttl_ms": 60_000}, alice, alice.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("create status = %d body = %v", call.status, call.body)
	}
	token, _ := call.body["token"].(string)
	linkID, _ := call.body["id"].(string)
	if token == "" || linkID == "" {
		t.Fatalf("create body missing token/id: %v", call.body)
	}
	if call.body["session_id"] != tabID {
		t.Fatalf("create body = %v", call.body)
	}

	// 列表: alice 1 条且不含 token; bob 0 条。
	list := f.call(t, http.MethodGet, "/share/previews", nil, alice, "", nil)
	if list.status != http.StatusOK || len(list.body["links"].([]any)) != 1 {
		t.Fatalf("alice list = %d %v", list.status, list.body)
	}
	row := list.body["links"].([]any)[0].(map[string]any)
	for _, forbidden := range []string{"token", "token_hash", "secret"} {
		if _, exists := row[forbidden]; exists {
			t.Fatalf("list view leaks %q: %v", forbidden, row)
		}
	}
	list = f.call(t, http.MethodGet, "/share/previews", nil, bob, "", nil)
	if list.status != http.StatusOK || len(list.body["links"].([]any)) != 0 {
		t.Fatalf("bob list = %d %v", list.status, list.body)
	}

	// 吊销: bob 403; alice 200; 之后 token 不可用 (WS 握手 403)。
	if call := f.call(t, http.MethodPost, "/share/previews/"+linkID+"/revoke", nil, bob, bob.csrf, nil); call.status != http.StatusForbidden {
		t.Fatalf("non-owner revoke status = %d, want 403", call.status)
	}
	if call := f.call(t, http.MethodPost, "/share/previews/"+linkID+"/revoke", nil, alice, alice.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("owner revoke status = %d body = %v", call.status, call.body)
	}
	if status := dialPreviewStatus(t, f.http.URL, token); status != http.StatusForbidden {
		t.Fatalf("revoked token WS handshake = %d, want 403", status)
	}
}

func TestSharePreviewPageServesSPA(t *testing.T) {
	root := t.TempDir()
	index := "<html><head><title>NexTerm</title></head><body>app</body></html>"
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newPreviewFixture(t, func(config *Config) { config.Options.WebRoot = root })

	response, err := f.client.Get(f.http.URL + "/share/preview/any-token")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "app") {
		t.Fatalf("plain GET = %d %q", response.StatusCode, body)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", response.Header.Get("Cache-Control"))
	}
	// 无效 token 的普通 GET 同样直接服务 SPA (不校验 token)。
	response, err = f.client.Get(f.http.URL + "/share/preview/not-a-token")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("plain GET invalid token = %d, want 200", response.StatusCode)
	}
	// WS upgrade 才走 token 把关。
	if status := dialPreviewStatus(t, f.http.URL, "not-a-token"); status != http.StatusForbidden {
		t.Fatalf("invalid token WS handshake = %d, want 403", status)
	}
}

func dialPreviewStatus(t *testing.T, httpURL, token string) int {
	t.Helper()
	conn, handshake, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(httpURL, "http")+"/share/preview/"+token, nil)
	if err == nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return http.StatusSwitchingProtocols
	}
	if handshake == nil {
		t.Fatalf("dial: %v", err)
	}
	return handshake.StatusCode
}

type previewTestFrame struct {
	kind websocket.MessageType
	data []byte
}

func readPreviewFrame(t *testing.T, conn *websocket.Conn, timeout time.Duration) previewTestFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	kind, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return previewTestFrame{kind: kind, data: data}
}

func readPreviewJSON(t *testing.T, conn *websocket.Conn, timeout time.Duration) map[string]any {
	t.Helper()
	frame := readPreviewFrame(t, conn, timeout)
	if frame.kind != websocket.MessageText {
		t.Fatalf("frame kind = %v, want text: %q", frame.kind, frame.data)
	}
	var decoded map[string]any
	if err := json.Unmarshal(frame.data, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

// expectPreviewError 读帧直到错误帧出现 (实时输出帧可与之交叠), 校验终止码。
func expectPreviewError(t *testing.T, conn *websocket.Conn, code string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		frame := readPreviewFrame(t, conn, timeout)
		if frame.kind != websocket.MessageText {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal(frame.data, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded["type"] == "ready" || decoded["type"] == "snapshot" {
			continue
		}
		if decoded["type"] != "error" || decoded["code"] != code {
			t.Fatalf("error frame = %v, want code %q", decoded, code)
		}
		return
	}
	t.Fatalf("no error frame with code %q within %s", code, timeout)
}

func dialPreviewViewer(t *testing.T, httpURL, token string) *websocket.Conn {
	t.Helper()
	conn, handshake, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(httpURL, "http")+"/share/preview/"+token, nil)
	if err != nil {
		status := 0
		if handshake != nil {
			status = handshake.StatusCode
		}
		t.Fatalf("viewer dial: %v (status %d)", err, status)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	return conn
}

func createPreviewViaAPI(t *testing.T, f *previewFixture, session *accountTestSession, tabID string) (string, string) {
	t.Helper()
	call := f.call(t, http.MethodPost, "/share/previews", map[string]any{"session_id": tabID, "ttl_ms": 3_600_000}, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("create status = %d body = %v", call.status, call.body)
	}
	token, _ := call.body["token"].(string)
	linkID, _ := call.body["id"].(string)
	return linkID, token
}

// TestSharePreviewTerminalEndToEnd 钉住只读订阅的完整数据面: ready 帧声明
// read, 随后是当前屏幕快照帧, 之后实时输出以二进制到达; viewer 上行的字节
// 不进终端 (协议层没有输入帧), 输出不受影响。
func TestSharePreviewTerminalEndToEnd(t *testing.T) {
	f := newPreviewFixture(t)
	aliceID := f.createUser(t, "alice")
	alice := f.login(t, "alice", "password-alice")
	tabID := f.openTab(t, aliceID)
	_, token := createPreviewViaAPI(t, f, alice, tabID)

	conn := dialPreviewViewer(t, f.http.URL, token)
	ready := readPreviewJSON(t, conn, 10*time.Second)
	if ready["type"] != "ready" || ready["permission"] != "read" || ready["session_id"] != tabID {
		t.Fatalf("ready frame = %v", ready)
	}
	snapshot := readPreviewJSON(t, conn, 10*time.Second)
	if snapshot["type"] != "snapshot" {
		t.Fatalf("second frame = %v, want snapshot", snapshot)
	}
	lines, ok := snapshot["lines"].([]any)
	if !ok || snapshot["cols"].(float64) != 80 || snapshot["rows"].(float64) != 24 {
		t.Fatalf("snapshot frame = %v", snapshot)
	}
	_ = lines

	// viewer 上行输入: 协议层丢弃, 不进入终端。
	if err := conn.Write(context.Background(), websocket.MessageBinary, []byte("echo nope-42\n")); err != nil {
		t.Fatal(err)
	}
	// 属主正常输出应继续到达 viewer。
	if err := f.manager.Write(context.Background(), tabID, "owner", []byte("echo after-42\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	seenAfter := false
	for time.Now().Before(deadline) && !seenAfter {
		frame := readPreviewFrame(t, conn, 10*time.Second)
		if strings.Contains(string(frame.data), "nope-42") {
			t.Fatalf("viewer input reached terminal output: %q", frame.data)
		}
		if frame.kind == websocket.MessageBinary && strings.Contains(string(frame.data), "after-42") {
			seenAfter = true
		}
	}
	if !seenAfter {
		t.Fatal("owner output never reached viewer")
	}
}

func TestSharePreviewRevokeStopsStream(t *testing.T) {
	f := newPreviewFixture(t)
	f.server.previewRevalidateInterval = 100 * time.Millisecond
	aliceID := f.createUser(t, "alice")
	alice := f.login(t, "alice", "password-alice")
	tabID := f.openTab(t, aliceID)
	linkID, token := createPreviewViaAPI(t, f, alice, tabID)

	conn := dialPreviewViewer(t, f.http.URL, token)
	readPreviewJSON(t, conn, 10*time.Second)
	readPreviewJSON(t, conn, 10*time.Second)

	if call := f.call(t, http.MethodPost, "/share/previews/"+linkID+"/revoke", nil, alice, alice.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("revoke: HTTP %d %v", call.status, call.body)
	}
	expectPreviewError(t, conn, "revoked", 10*time.Second)
}

func TestSharePreviewExpiryStopsStream(t *testing.T) {
	f := newPreviewFixture(t)
	f.server.previewRevalidateInterval = 100 * time.Millisecond
	aliceID := f.createUser(t, "alice")
	alice := f.login(t, "alice", "password-alice")
	tabID := f.openTab(t, aliceID)
	_, token := createPreviewViaAPI(t, f, alice, tabID)

	conn := dialPreviewViewer(t, f.http.URL, token)
	readPreviewJSON(t, conn, 10*time.Second)
	readPreviewJSON(t, conn, 10*time.Second)

	if _, err := f.db.ExecContext(context.Background(), "UPDATE share_preview_link SET expires_at = ?", time.Now().UnixMilli()-1000); err != nil {
		t.Fatal(err)
	}
	expectPreviewError(t, conn, "expired", 10*time.Second)
}

// TestSharePreviewTabCloseEndsStream: 标签页关闭后订阅通道终止, viewer 收到
// ended 错误帧。
func TestSharePreviewTabCloseEndsStream(t *testing.T) {
	f := newPreviewFixture(t)
	aliceID := f.createUser(t, "alice")
	alice := f.login(t, "alice", "password-alice")
	tabID := f.openTab(t, aliceID)
	_, token := createPreviewViaAPI(t, f, alice, tabID)

	conn := dialPreviewViewer(t, f.http.URL, token)
	readPreviewJSON(t, conn, 10*time.Second)
	readPreviewJSON(t, conn, 10*time.Second)

	if err := f.manager.CloseTab(tabID); err != nil {
		t.Fatal(err)
	}
	expectPreviewError(t, conn, "ended", 10*time.Second)
}

func TestSharePreviewResolveRateLimited(t *testing.T) {
	f := newPreviewFixture(t)
	if status := dialPreviewStatus(t, f.http.URL, "not-a-token"); status != http.StatusForbidden {
		t.Fatalf("first invalid token = %d, want 403", status)
	}
	if status := dialPreviewStatus(t, f.http.URL, "not-a-token"); status != http.StatusTooManyRequests {
		t.Fatalf("second invalid token = %d, want 429", status)
	}
}

func TestSharePreviewAbsentInSyncOnly(t *testing.T) {
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	accounts := account.New(database.DB())
	previews, err := sharing.New(sharing.Config{DB: database.DB(), Accounts: accounts})
	if err != nil {
		t.Fatal(err)
	}
	config := testConfig(t, true)
	config.Options.Auth = AuthOn
	config.Accounts = accounts
	config.Previews = previews
	config.Spectator = session.NewManager(session.Config{})
	_, httpServer := newTestHTTP(t, config)

	for _, path := range []string{"/share/previews", "/share/preview/x"} {
		response, err := httpServer.Client().Get(httpServer.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s (sync-only) status = %d, want 404", path, response.StatusCode)
		}
	}
}
