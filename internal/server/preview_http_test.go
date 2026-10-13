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

// newPreviewFixture assembles a full server with read-only previews: a real session Manager (local sh)
// providing output channels via a hub adapter, and a real sharing.Service.
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

// openTab opens a real terminal tab (local sh) with the given owner, returning the tab ID.
func (f *previewFixture) openTab(t *testing.T, ownerID string) string {
	t.Helper()
	tabID, _ := f.openTabWithChannel(t, ownerID)
	return tabID
}

// openTabWithChannel is like openTab, additionally returning the owner channel ID (detaching it clears the controller,
// for tests of the guest control arbitration path).
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

	// Not logged in 401; missing CSRF 403.
	if call := f.call(t, http.MethodPost, "/share/previews", map[string]any{"session_id": tabID}, nil, "", nil); call.status != http.StatusUnauthorized {
		t.Fatalf("anonymous create status = %d, want 401", call.status)
	}
	if call := f.call(t, http.MethodPost, "/share/previews", map[string]any{"session_id": tabID}, alice, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("missing csrf status = %d, want 403", call.status)
	}
	// Someone else's terminal: bob 403.
	if call := f.call(t, http.MethodPost, "/share/previews", map[string]any{"session_id": tabID}, bob, bob.csrf, nil); call.status != http.StatusForbidden {
		t.Fatalf("non-owner create status = %d, want 403", call.status)
	}
	// Nonexistent tab: 404.
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
	// write omitted defaults to read-only.
	if call.body["permission"] != "read" {
		t.Fatalf("default create permission = %v, want read", call.body["permission"])
	}

	// write: true explicitly grants read-write.
	writable := f.call(t, http.MethodPost, "/share/previews", map[string]any{"session_id": tabID, "ttl_ms": 60_000, "write": true}, alice, alice.csrf, nil)
	if writable.status != http.StatusOK {
		t.Fatalf("write create status = %d body = %v", writable.status, writable.body)
	}
	if writable.body["permission"] != "read_write" {
		t.Fatalf("write create permission = %v, want read_write", writable.body["permission"])
	}

	// List: alice has 2 entries with real permission and no token; bob has 0.
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

	// Revoke: bob 403; alice 200; the token is unusable afterwards (WS handshake 403).
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
	// A plain GET with an invalid token likewise serves the SPA directly (no token check).
	response, err = f.client.Get(f.http.URL + "/share/preview/not-a-token")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("plain GET invalid token = %d, want 200", response.StatusCode)
	}
	// Only the WS upgrade goes through token checks.
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

// expectPreviewError reads frames until an error frame appears (live output frames may interleave), checking the termination code.
func expectPreviewError(t *testing.T, conn *websocket.Conn, code string, timeout time.Duration) {
	t.Helper()
	expectPreviewErrorFrame(t, conn, code, "", timeout)
}

// expectPreviewErrorFrame is like expectPreviewError, additionally pinning the message text
// (when message is empty, only the code is checked).
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

// TestSharePreviewTerminalEndToEnd pins the read-only subscription's full data plane: the ready frame declares
// read and lease_ms, followed by the current screen snapshot frame, then live output arriving as binary; read-only
// links install no input handling, the viewer's upstream bytes are discarded by the read loop and never reach the terminal, and output is unaffected.
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

	// Viewer upstream input: dropped at the protocol layer, never reaching the terminal.
	if err := conn.Write(context.Background(), websocket.MessageBinary, []byte("echo nope-42\n")); err != nil {
		t.Fatal(err)
	}
	// The owner's normal output should keep arriving at the viewer.
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

// TestSharePreviewTabCloseEndsStream: after the tab closes, the subscription channel terminates and the viewer receives
// an ended error frame.
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

// awaitPreviewControl reads frames until a control frame appears (binary output frames may interleave), returning the decoded frame.
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

// expectPreviewBinaryMarker reads frames until binary output contains the marker; it fails if forbidden bytes
// (echo of rejected input) or an error frame appear meanwhile, pinning "rejected input never reaches the terminal and the connection stays open".
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

// TestSharePreviewWriteInputReachesTerminal pins the writable preview's full input direction: ready
// declares read_write and lease_ms, viewer binary input wins control via arbitration (guest control
// frame carries lease_until), bytes land in the terminal and come back to the viewer as echo output.
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

// TestSharePreviewWriteOwnerControls pins owner-held arbitration: while the owner client holds the controller,
// guest input is turned back with ErrGuestControlOwner (owner control frame), bytes never reach the terminal,
// and the connection and output are unaffected.
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

// TestSharePreviewWriteGuestBusy pins guest-to-guest arbitration: while another guest's lease is still within the disconnect
// grace window (controller orphan-cleaned but lease not expired), guest input is turned back with ErrGuestControlBusy
// (busy control frame), and bytes never reach the terminal.
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

	// Guest A takes control via the manager and then drops its subscription channel: orphan cleanup removes the controller,
	// and the lease is kept until expiry (disconnect grace).
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

// TestSharePreviewShrinkToReadRejectsInput pins permission shrink taking effect on the input side: after a read_write
// connection is established, the link is changed to read; subsequent input is re-checked against the current row and rejected,
// delivering only a non-terminal read_only error frame, the connection stays open, and bytes never reach the terminal.
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

	// Non-terminal: the connection continues, the owner's output after taking over keeps arriving, and the rejected input never reached the terminal.
	if _, err := f.manager.Claim(tabID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.Write(context.Background(), tabID, "owner", []byte("echo after-ro-1\n")); err != nil {
		t.Fatal(err)
	}
	expectPreviewBinaryMarker(t, conn, "after-ro-1", "denied-ro-1", 10*time.Second)
}

// TestSharePreviewDisconnectReleasesGuest pins ReleaseGuest at WS end: after guest A disconnects, its control and lease are
// explicitly released (rather than left to expire), and guest B on another link can take control immediately
// (proven by the busy-then-guest probes).
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
		// busy: A's lease is not yet released; keep probing.
	}
	t.Fatal("guest B never acquired control after guest A disconnected")
}

// TestSharePreviewRevokeTerminatesWritable pins revocation terminating a writable connection: the termination frame matches the
// read-only path (revoked), and guest control is released at WS end.
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

// TestSharePreviewTextFrameDropped pins invalid upstream frame handling: text frames are not a contracted input type,
// they are silently discarded with no error frame at all (not even read_only), and the connection stays open; subsequent
// valid binary input is admitted normally.
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
	// Any response to a text frame would arrive before the control frame of the binary input (the read loop processes in order,
	// queue is FIFO); awaitPreviewControl fails on error frames, pinning the silent-discard semantics.
	if control := awaitPreviewControl(t, conn, 10*time.Second); control["state"] != "guest" {
		t.Fatalf("control frame = %v, want guest", control)
	}
	expectPreviewBinaryMarker(t, conn, "text-ok-1", "nope", 10*time.Second)
}

// TestSharePreviewRevokedInputFrame pins the input frame mapping after revocation: after a read_write connection is established,
// the link is revoked, and input re-checked against the current row is rejected with a non-terminal revoked frame (same code and text
// as the output-side termination frame), never misreported as read_only; before the output-side periodic recheck terminates
// (set to 1 hour in this test, not firing), the connection continues and rejected input never reaches the terminal.
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

	// Non-terminal: the connection continues, the owner's output after taking over keeps arriving, and the rejected input never reached the terminal.
	if _, err := f.manager.Claim(tabID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.Write(context.Background(), tabID, "owner", []byte("echo after-rev-1\n")); err != nil {
		t.Fatal(err)
	}
	expectPreviewBinaryMarker(t, conn, "after-rev-1", "denied-rev-1", 10*time.Second)
}

// TestSharePreviewExpiredInputFrame pins the input frame mapping after expiry: after a read_write connection is established,
// the link expires, and input re-checked against the current row is rejected with a non-terminal expired frame, never misreported
// as read_only; the connection continues until the output-side periodic recheck (not fired in this test) terminates it.
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
