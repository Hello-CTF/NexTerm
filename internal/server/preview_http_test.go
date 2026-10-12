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
	accounts := account.New(database.GORM())
	previews, err := sharing.New(sharing.Config{DB: database.GORM(), Accounts: accounts})
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
	tabID, _ := f.openTabWithChannel(t, ownerID)
	return tabID
}

// openTabWithChannel 同 openTab, 额外返回属主通道 ID (detach 后可清空 controller,
// 供访客控制仲裁路径的测试使用)。
func (f *previewFixture) openTabWithChannel(t *testing.T, ownerID string) (string, string) {
	t.Helper()
	connectCtx := ipc.WithUserID(context.Background(), ownerID)
	connected, err := f.manager.Connect(connectCtx, session.Asset{ID: "preview-asset-" + ownerID, Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	channelID := "owner-" + ids.New()
	info, err := f.manager.OpenTab(context.Background(), session.OpenTabOptions{
		SessionID: connected.ID, ClientID: "owner", ChannelID: channelID, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	return info.ID, channelID
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
	// write 省略默认只读。
	if call.body["permission"] != "read" {
		t.Fatalf("default create permission = %v, want read", call.body["permission"])
	}

	// write: true 显式授予读写。
	writable := f.call(t, http.MethodPost, "/share/previews", map[string]any{"session_id": tabID, "ttl_ms": 60_000, "write": true}, alice, alice.csrf, nil)
	if writable.status != http.StatusOK {
		t.Fatalf("write create status = %d body = %v", writable.status, writable.body)
	}
	if writable.body["permission"] != "read_write" {
		t.Fatalf("write create permission = %v, want read_write", writable.body["permission"])
	}

	// 列表: alice 2 条且带真实 permission、不含 token; bob 0 条。
	list := f.call(t, http.MethodGet, "/share/previews", nil, alice, "", nil)
	if list.status != http.StatusOK || len(list.body["links"].([]any)) != 2 {
		t.Fatalf("alice list = %d %v", list.status, list.body)
	}
	permissions := map[string]bool{}
	for _, raw := range list.body["links"].([]any) {
		row := raw.(map[string]any)
		for _, forbidden := range []string{"token", "token_hash", "secret"} {
			if _, exists := row[forbidden]; exists {
				t.Fatalf("list view leaks %q: %v", forbidden, row)
			}
		}
		permission, _ := row["permission"].(string)
		permissions[permission] = true
	}
	if !permissions["read"] || !permissions["read_write"] {
		t.Fatalf("list permissions = %v, want read + read_write", permissions)
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
	expectPreviewErrorFrame(t, conn, code, "", timeout)
}

// expectPreviewErrorFrame 同 expectPreviewError, 额外钉住 message 文案
// (message 为空串时只校验 code)。
func expectPreviewErrorFrame(t *testing.T, conn *websocket.Conn, code, message string, timeout time.Duration) {
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
		if message != "" && decoded["message"] != message {
			t.Fatalf("error message = %v, want %q", decoded["message"], message)
		}
		return
	}
	t.Fatalf("no error frame with code %q within %s", code, timeout)
}

func dialPreviewViewer(t *testing.T, httpURL, token string) *websocket.Conn {
	t.Helper()
	conn, handshake, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(httpURL, "http")+"/share/preview/"+token, &websocket.DialOptions{CompressionMode: websocket.CompressionContextTakeover})
	if err != nil {
		status := 0
		if handshake != nil {
			status = handshake.StatusCode
		}
		t.Fatalf("viewer dial: %v (status %d)", err, status)
	}
	if got := handshake.Header.Get("Sec-WebSocket-Extensions"); !strings.Contains(got, "permessage-deflate") {
		t.Fatalf("compression extensions = %q, want permessage-deflate", got)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	return conn
}

func createPreviewViaAPI(t *testing.T, f *previewFixture, session *accountTestSession, tabID string) (string, string) {
	t.Helper()
	return createPreviewWithWrite(t, f, session, tabID, false)
}

func createPreviewWithWrite(t *testing.T, f *previewFixture, session *accountTestSession, tabID string, write bool) (string, string) {
	t.Helper()
	call := f.call(t, http.MethodPost, "/share/previews", map[string]any{"session_id": tabID, "ttl_ms": 3_600_000, "write": write}, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("create status = %d body = %v", call.status, call.body)
	}
	token, _ := call.body["token"].(string)
	linkID, _ := call.body["id"].(string)
	return linkID, token
}

// TestSharePreviewTerminalEndToEnd 钉住只读订阅的完整数据面: ready 帧声明
// read 与 lease_ms, 随后是当前屏幕快照帧, 之后实时输出以二进制到达; 只读链接
// 不安装输入处理, viewer 上行的字节被读循环丢弃不进终端, 输出不受影响。
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
	if ready["lease_ms"] != float64(3000) {
		t.Fatalf("ready lease_ms = %v, want 3000", ready["lease_ms"])
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
	accounts := account.New(database.GORM())
	previews, err := sharing.New(sharing.Config{DB: database.GORM(), Accounts: accounts})
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

// awaitPreviewControl 读帧直到 control 帧出现 (输出二进制帧可与之交叠), 返回解码帧。
func awaitPreviewControl(t *testing.T, conn *websocket.Conn, timeout time.Duration) map[string]any {
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
		if decoded["type"] == "control" {
			return decoded
		}
		if decoded["type"] == "error" {
			t.Fatalf("unexpected error frame while awaiting control: %v", decoded)
		}
	}
	t.Fatal("no control frame before deadline")
	return nil
}

// expectPreviewBinaryMarker 读帧直到二进制输出包含 marker; 期间若出现 forbidden
// 字节 (被拒输入的回显) 或 error 帧即失败, 用于钉住"被拒输入不落终端且连接不断"。
func expectPreviewBinaryMarker(t *testing.T, conn *websocket.Conn, marker, forbidden string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		frame := readPreviewFrame(t, conn, timeout)
		if forbidden != "" && strings.Contains(string(frame.data), forbidden) {
			t.Fatalf("rejected input reached terminal output: %q", frame.data)
		}
		if frame.kind == websocket.MessageText {
			var decoded map[string]any
			if err := json.Unmarshal(frame.data, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded["type"] == "error" {
				t.Fatalf("unexpected terminal error frame: %v", decoded)
			}
		}
		if frame.kind == websocket.MessageBinary && strings.Contains(string(frame.data), marker) {
			return
		}
	}
	t.Fatalf("no binary output containing %q within %s", marker, timeout)
}

func writePreviewInput(t *testing.T, conn *websocket.Conn, data string) {
	t.Helper()
	if err := conn.Write(context.Background(), websocket.MessageBinary, []byte(data)); err != nil {
		t.Fatal(err)
	}
}

func previewTabController(t *testing.T, f *previewFixture, tabID string) string {
	t.Helper()
	tab, err := f.manager.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	return tab.Info().Controller
}

// TestSharePreviewWriteInputReachesTerminal 钉住可写预览的完整输入方向: ready
// 声明 read_write 与 lease_ms, viewer 二进制输入经仲裁取得控制权 (guest control
// 帧携带 lease_until), 字节落入终端并随回显输出回到 viewer。
func TestSharePreviewWriteInputReachesTerminal(t *testing.T) {
	f := newPreviewFixture(t)
	aliceID := f.createUser(t, "alice")
	alice := f.login(t, "alice", "password-alice")
	tabID, ownerChannel := f.openTabWithChannel(t, aliceID)
	if err := f.manager.DetachChannel(ownerChannel); err != nil {
		t.Fatal(err)
	}
	linkID, token := createPreviewWithWrite(t, f, alice, tabID, true)

	conn := dialPreviewViewer(t, f.http.URL, token)
	ready := readPreviewJSON(t, conn, 10*time.Second)
	if ready["type"] != "ready" || ready["permission"] != "read_write" || ready["session_id"] != tabID {
		t.Fatalf("ready frame = %v", ready)
	}
	if ready["lease_ms"] != float64(3000) {
		t.Fatalf("ready lease_ms = %v, want 3000", ready["lease_ms"])
	}
	if snapshot := readPreviewJSON(t, conn, 10*time.Second); snapshot["type"] != "snapshot" {
		t.Fatalf("second frame = %v, want snapshot", snapshot)
	}

	writePreviewInput(t, conn, "echo guest-rw-42\n")
	control := awaitPreviewControl(t, conn, 10*time.Second)
	if control["state"] != "guest" {
		t.Fatalf("control frame = %v, want guest", control)
	}
	if leaseUntil, ok := control["lease_until"].(float64); !ok || leaseUntil <= 0 {
		t.Fatalf("guest control lease_until = %v, want positive epoch ms", control["lease_until"])
	}
	if controller := previewTabController(t, f, tabID); controller != "preview-"+linkID {
		t.Fatalf("controller = %q, want preview-%s", controller, linkID)
	}
	expectPreviewBinaryMarker(t, conn, "guest-rw-42", "", 10*time.Second)
}

// TestSharePreviewWriteOwnerControls 钉住属主占有仲裁: 属主客户端持有 controller
// 时访客输入被 ErrGuestControlOwner 挡回 (owner control 帧), 字节不落终端,
// 连接与输出不受影响。
func TestSharePreviewWriteOwnerControls(t *testing.T) {
	f := newPreviewFixture(t)
	aliceID := f.createUser(t, "alice")
	alice := f.login(t, "alice", "password-alice")
	tabID := f.openTab(t, aliceID)
	_, token := createPreviewWithWrite(t, f, alice, tabID, true)

	conn := dialPreviewViewer(t, f.http.URL, token)
	readPreviewJSON(t, conn, 10*time.Second)
	readPreviewJSON(t, conn, 10*time.Second)

	writePreviewInput(t, conn, "echo denied-ow-1\n")
	if control := awaitPreviewControl(t, conn, 10*time.Second); control["state"] != "owner" {
		t.Fatalf("control frame = %v, want owner", control)
	}
	if controller := previewTabController(t, f, tabID); controller != "owner" {
		t.Fatalf("controller = %q, want owner", controller)
	}
	if err := f.manager.Write(context.Background(), tabID, "owner", []byte("echo owner-flow-1\n")); err != nil {
		t.Fatal(err)
	}
	expectPreviewBinaryMarker(t, conn, "owner-flow-1", "denied-ow-1", 10*time.Second)
}

// TestSharePreviewWriteGuestBusy 钉住访客间仲裁: 另一访客的租约仍在断线宽限窗内
// (controller 已被 orphan 清理但租约未到期) 时, 访客输入被 ErrGuestControlBusy
// 挡回 (busy control 帧), 字节不落终端。
func TestSharePreviewWriteGuestBusy(t *testing.T) {
	f := newPreviewFixture(t)
	aliceID := f.createUser(t, "alice")
	alice := f.login(t, "alice", "password-alice")
	tabID, ownerChannel := f.openTabWithChannel(t, aliceID)
	if err := f.manager.DetachChannel(ownerChannel); err != nil {
		t.Fatal(err)
	}
	linkA, _ := createPreviewWithWrite(t, f, alice, tabID, true)
	_, tokenB := createPreviewWithWrite(t, f, alice, tabID, true)

	// 访客 A 经 manager 取得控制后断开订阅通道: orphan 清理摘掉 controller,
	// 租约保留至到期 (断线宽限)。
	if _, err := f.manager.AttachSpectator(context.Background(), tabID, session.SpectateOptions{
		ClientID: "preview-" + linkA, ChannelID: "spec-a",
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.WriteGuest(context.Background(), tabID, "preview-"+linkA, []byte("a\n"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.DetachChannel("spec-a"); err != nil {
		t.Fatal(err)
	}
	if controller := previewTabController(t, f, tabID); controller != "" {
		t.Fatalf("precondition: controller = %q, want empty", controller)
	}

	conn := dialPreviewViewer(t, f.http.URL, tokenB)
	readPreviewJSON(t, conn, 10*time.Second)
	readPreviewJSON(t, conn, 10*time.Second)
	writePreviewInput(t, conn, "echo denied-b-1\n")
	if control := awaitPreviewControl(t, conn, 10*time.Second); control["state"] != "busy" {
		t.Fatalf("control frame = %v, want busy", control)
	}
	if _, err := f.manager.Claim(tabID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.Write(context.Background(), tabID, "owner", []byte("echo owner-flow-2\n")); err != nil {
		t.Fatal(err)
	}
	expectPreviewBinaryMarker(t, conn, "owner-flow-2", "denied-b-1", 10*time.Second)
}

// TestSharePreviewShrinkToReadRejectsInput 钉住权限收缩的输入侧生效: read_write
// 连接建立后链接被改成 read, 后续输入按当前行重查被拒, 只下非终止 read_only
// 错误帧, 连接不断, 字节不落终端。
func TestSharePreviewShrinkToReadRejectsInput(t *testing.T) {
	f := newPreviewFixture(t)
	aliceID := f.createUser(t, "alice")
	alice := f.login(t, "alice", "password-alice")
	tabID, ownerChannel := f.openTabWithChannel(t, aliceID)
	if err := f.manager.DetachChannel(ownerChannel); err != nil {
		t.Fatal(err)
	}
	_, token := createPreviewWithWrite(t, f, alice, tabID, true)

	conn := dialPreviewViewer(t, f.http.URL, token)
	if ready := readPreviewJSON(t, conn, 10*time.Second); ready["permission"] != "read_write" {
		t.Fatalf("ready frame = %v, want read_write", ready)
	}
	readPreviewJSON(t, conn, 10*time.Second)

	if _, err := f.db.ExecContext(context.Background(), "UPDATE share_preview_link SET permission = 'read'"); err != nil {
		t.Fatal(err)
	}
	writePreviewInput(t, conn, "echo denied-ro-1\n")
	expectPreviewErrorFrame(t, conn, "read_only", "预览当前为只读", 10*time.Second)

	// 非终止: 连接继续, 属主接管后的输出照常到达, 被拒输入从未落终端。
	if _, err := f.manager.Claim(tabID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.Write(context.Background(), tabID, "owner", []byte("echo after-ro-1\n")); err != nil {
		t.Fatal(err)
	}
	expectPreviewBinaryMarker(t, conn, "after-ro-1", "denied-ro-1", 10*time.Second)
}

// TestSharePreviewDisconnectReleasesGuest 钉住 WS 结束的 ReleaseGuest: 访客 A
// 断连后其控制权与租约被显式释放 (而非留到租约到期), 另一链接的访客 B 输入
// 立即可取得控制 (先 busy 后 guest 的探针证明释放生效)。
func TestSharePreviewDisconnectReleasesGuest(t *testing.T) {
	f := newPreviewFixture(t)
	aliceID := f.createUser(t, "alice")
	alice := f.login(t, "alice", "password-alice")
	tabID, ownerChannel := f.openTabWithChannel(t, aliceID)
	if err := f.manager.DetachChannel(ownerChannel); err != nil {
		t.Fatal(err)
	}
	linkA, tokenA := createPreviewWithWrite(t, f, alice, tabID, true)
	linkB, tokenB := createPreviewWithWrite(t, f, alice, tabID, true)

	connA := dialPreviewViewer(t, f.http.URL, tokenA)
	readPreviewJSON(t, connA, 10*time.Second)
	readPreviewJSON(t, connA, 10*time.Second)
	writePreviewInput(t, connA, "x")
	if control := awaitPreviewControl(t, connA, 10*time.Second); control["state"] != "guest" {
		t.Fatalf("guest A control = %v, want guest", control)
	}
	if controller := previewTabController(t, f, tabID); controller != "preview-"+linkA {
		t.Fatalf("controller = %q, want preview-%s", controller, linkA)
	}
	if err := connA.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatal(err)
	}

	connB := dialPreviewViewer(t, f.http.URL, tokenB)
	readPreviewJSON(t, connB, 10*time.Second)
	readPreviewJSON(t, connB, 10*time.Second)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		writePreviewInput(t, connB, "true\n")
		readCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		kind, data, err := connB.Read(readCtx)
		cancel()
		if err != nil || kind != websocket.MessageText {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded["type"] != "control" {
			continue
		}
		if decoded["state"] == "guest" {
			if controller := previewTabController(t, f, tabID); controller != "preview-"+linkB {
				t.Fatalf("controller = %q, want preview-%s", controller, linkB)
			}
			return
		}
		// busy: A 的租约尚未释放, 继续探针。
	}
	t.Fatal("guest B never acquired control after guest A disconnected")
}

// TestSharePreviewRevokeTerminatesWritable 钉住可写连接的吊销终止: 终止帧与只读
// 路径一致 (revoked), WS 结束后访客控制权被释放。
func TestSharePreviewRevokeTerminatesWritable(t *testing.T) {
	f := newPreviewFixture(t)
	f.server.previewRevalidateInterval = 100 * time.Millisecond
	aliceID := f.createUser(t, "alice")
	alice := f.login(t, "alice", "password-alice")
	tabID, ownerChannel := f.openTabWithChannel(t, aliceID)
	if err := f.manager.DetachChannel(ownerChannel); err != nil {
		t.Fatal(err)
	}
	linkID, token := createPreviewWithWrite(t, f, alice, tabID, true)

	conn := dialPreviewViewer(t, f.http.URL, token)
	readPreviewJSON(t, conn, 10*time.Second)
	readPreviewJSON(t, conn, 10*time.Second)
	writePreviewInput(t, conn, "x")
	if control := awaitPreviewControl(t, conn, 10*time.Second); control["state"] != "guest" {
		t.Fatalf("control = %v, want guest", control)
	}

	if call := f.call(t, http.MethodPost, "/share/previews/"+linkID+"/revoke", nil, alice, alice.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("revoke: HTTP %d %v", call.status, call.body)
	}
	expectPreviewError(t, conn, "revoked", 10*time.Second)
	waitFor(t, func() bool {
		tab, err := f.manager.Tab(tabID)
		return err == nil && tab.Info().Controller == ""
	})
}

// TestSharePreviewTextFrameDropped 钉住非法上行帧处理: 文本帧不是合同输入类型,
// 静默丢弃且不下任何错误帧 (read_only 都不发), 连接不断; 随后的合法二进制输入
// 正常放行。
func TestSharePreviewTextFrameDropped(t *testing.T) {
	f := newPreviewFixture(t)
	aliceID := f.createUser(t, "alice")
	alice := f.login(t, "alice", "password-alice")
	tabID, ownerChannel := f.openTabWithChannel(t, aliceID)
	if err := f.manager.DetachChannel(ownerChannel); err != nil {
		t.Fatal(err)
	}
	_, token := createPreviewWithWrite(t, f, alice, tabID, true)

	conn := dialPreviewViewer(t, f.http.URL, token)
	readPreviewJSON(t, conn, 10*time.Second)
	readPreviewJSON(t, conn, 10*time.Second)

	if err := conn.Write(context.Background(), websocket.MessageText, []byte(`{"type":"input","data":"nope"}`)); err != nil {
		t.Fatal(err)
	}
	writePreviewInput(t, conn, "echo text-ok-1\n")
	// 文本帧若产生任何响应都会先于二进制输入的 control 帧到达 (读循环顺序处理,
	// 队列先进先出); awaitPreviewControl 遇到 error 帧即失败, 钉住静默丢弃语义。
	if control := awaitPreviewControl(t, conn, 10*time.Second); control["state"] != "guest" {
		t.Fatalf("control frame = %v, want guest", control)
	}
	expectPreviewBinaryMarker(t, conn, "text-ok-1", "nope", 10*time.Second)
}

// TestSharePreviewRevokedInputFrame 钉住吊销后的输入帧映射: read_write 连接建立
// 后链接被吊销, 输入按当前行重查被拒, 下非终止 revoked 帧 (与输出侧终止帧同码
// 同文案), 不误报 read_only; 输出侧周期复查 (测试内调至 1 小时, 不触发) 终止前
// 连接继续, 被拒输入不落终端。
func TestSharePreviewRevokedInputFrame(t *testing.T) {
	f := newPreviewFixture(t)
	f.server.previewRevalidateInterval = time.Hour
	aliceID := f.createUser(t, "alice")
	alice := f.login(t, "alice", "password-alice")
	tabID, ownerChannel := f.openTabWithChannel(t, aliceID)
	if err := f.manager.DetachChannel(ownerChannel); err != nil {
		t.Fatal(err)
	}
	linkID, token := createPreviewWithWrite(t, f, alice, tabID, true)

	conn := dialPreviewViewer(t, f.http.URL, token)
	readPreviewJSON(t, conn, 10*time.Second)
	readPreviewJSON(t, conn, 10*time.Second)

	if call := f.call(t, http.MethodPost, "/share/previews/"+linkID+"/revoke", nil, alice, alice.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("revoke: HTTP %d %v", call.status, call.body)
	}
	writePreviewInput(t, conn, "echo denied-rev-1\n")
	expectPreviewErrorFrame(t, conn, "revoked", "分享链接已吊销", 10*time.Second)

	// 非终止: 连接继续, 属主接管后的输出照常到达, 被拒输入从未落终端。
	if _, err := f.manager.Claim(tabID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.Write(context.Background(), tabID, "owner", []byte("echo after-rev-1\n")); err != nil {
		t.Fatal(err)
	}
	expectPreviewBinaryMarker(t, conn, "after-rev-1", "denied-rev-1", 10*time.Second)
}

// TestSharePreviewExpiredInputFrame 钉住过期后的输入帧映射: read_write 连接建立
// 后链接过期, 输入按当前行重查被拒, 下非终止 expired 帧, 不误报 read_only;
// 连接在输出侧周期复查 (测试内不触发) 终止前继续。
func TestSharePreviewExpiredInputFrame(t *testing.T) {
	f := newPreviewFixture(t)
	f.server.previewRevalidateInterval = time.Hour
	aliceID := f.createUser(t, "alice")
	alice := f.login(t, "alice", "password-alice")
	tabID, ownerChannel := f.openTabWithChannel(t, aliceID)
	if err := f.manager.DetachChannel(ownerChannel); err != nil {
		t.Fatal(err)
	}
	_, token := createPreviewWithWrite(t, f, alice, tabID, true)

	conn := dialPreviewViewer(t, f.http.URL, token)
	readPreviewJSON(t, conn, 10*time.Second)
	readPreviewJSON(t, conn, 10*time.Second)

	if _, err := f.db.ExecContext(context.Background(), "UPDATE share_preview_link SET expires_at = ?", time.Now().UnixMilli()-1000); err != nil {
		t.Fatal(err)
	}
	writePreviewInput(t, conn, "echo denied-exp-1\n")
	expectPreviewErrorFrame(t, conn, "expired", "分享链接已过期", 10*time.Second)

	if _, err := f.manager.Claim(tabID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.Write(context.Background(), tabID, "owner", []byte("echo after-exp-1\n")); err != nil {
		t.Fatal(err)
	}
	expectPreviewBinaryMarker(t, conn, "after-exp-1", "denied-exp-1", 10*time.Second)
}
