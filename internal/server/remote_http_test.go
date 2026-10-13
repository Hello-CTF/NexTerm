package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/sharing"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/coder/websocket"
)

// createRemoteViaAPI creates a remote session shell via the management endpoint, returning the session_id.
func createRemoteViaAPI(t *testing.T, f *previewFixture, session *accountTestSession, title string) string {
	t.Helper()
	call := f.call(t, http.MethodPost, "/share/remote-sessions", map[string]any{"title": title, "cols": 80, "rows": 24}, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("create remote status = %d body = %v", call.status, call.body)
	}
	id, _ := call.body["session_id"].(string)
	if id == "" {
		t.Fatalf("create remote body missing session_id: %v", call.body)
	}
	return id
}

func remoteFeedHeaders(session *accountTestSession) http.Header {
	header := http.Header{}
	header.Set("Cookie", sessionCookieName+"="+session.cookie.Value)
	header.Set(csrfHeaderName, session.csrf)
	return header
}

// dialRemoteFeed connects to the feed WS with the given request headers; on success it returns the connection, on failure the handshake status code.
func dialRemoteFeed(t *testing.T, httpURL, id string, header http.Header) (*websocket.Conn, int) {
	t.Helper()
	conn, handshake, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(httpURL, "http")+"/share/remote-sessions/"+id+"/feed", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		status := 0
		if handshake != nil {
			status = handshake.StatusCode
		}
		if status == 0 {
			t.Fatalf("feed dial: %v (no handshake)", err)
		}
		return nil, status
	}
	return conn, http.StatusSwitchingProtocols
}

// TestRemoteSessionManagementEndpoints pins the remote shell management surface's auth and ownership: not logged in
// 401, missing CSRF 403, others' feed/delete/preview-create 403, owner all-pass; feed after delete is 404.
func TestRemoteSessionManagementEndpoints(t *testing.T) {
	f := newPreviewFixture(t)
	f.createUser(t, "alice")
	f.createUser(t, "bob")
	alice := f.login(t, "alice", "password-alice")
	bob := f.login(t, "bob", "password-bob")

	if call := f.call(t, http.MethodPost, "/share/remote-sessions", map[string]any{"title": "t"}, nil, "", nil); call.status != http.StatusUnauthorized {
		t.Fatalf("anonymous create status = %d, want 401", call.status)
	}
	if call := f.call(t, http.MethodPost, "/share/remote-sessions", map[string]any{"title": "t"}, alice, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("missing csrf status = %d, want 403", call.status)
	}
	id := createRemoteViaAPI(t, f, alice, "alice-terminal")

	// Feed WS handshake: no credentials 401, missing CSRF 403, non-owner 403, owner 101.
	if _, status := dialRemoteFeed(t, f.http.URL, id, nil); status != http.StatusUnauthorized {
		t.Fatalf("anonymous feed handshake = %d, want 401", status)
	}
	noCSRF := http.Header{}
	noCSRF.Set("Cookie", sessionCookieName+"="+alice.cookie.Value)
	if _, status := dialRemoteFeed(t, f.http.URL, id, noCSRF); status != http.StatusForbidden {
		t.Fatalf("missing csrf feed handshake = %d, want 403", status)
	}
	if _, status := dialRemoteFeed(t, f.http.URL, id, remoteFeedHeaders(bob)); status != http.StatusForbidden {
		t.Fatalf("non-owner feed handshake = %d, want 403", status)
	}
	conn, status := dialRemoteFeed(t, f.http.URL, id, remoteFeedHeaders(alice))
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("owner feed handshake = %d, want 101", status)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	// Non-owner creating a preview on / deleting the shell: 403.
	if call := f.call(t, http.MethodPost, "/share/previews", map[string]any{"session_id": id, "ttl_ms": 60_000}, bob, bob.csrf, nil); call.status != http.StatusForbidden {
		t.Fatalf("non-owner preview create status = %d, want 403", call.status)
	}
	if call := f.call(t, http.MethodDelete, "/share/remote-sessions/"+id, nil, bob, bob.csrf, nil); call.status != http.StatusForbidden {
		t.Fatalf("non-owner delete status = %d, want 403", call.status)
	}
	// Delete with missing CSRF: 403.
	if call := f.call(t, http.MethodDelete, "/share/remote-sessions/"+id, nil, alice, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("missing csrf delete status = %d, want 403", call.status)
	}
	// Owner delete: 200; the feed handshake afterwards is 404.
	if call := f.call(t, http.MethodDelete, "/share/remote-sessions/"+id, nil, alice, alice.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("owner delete status = %d body = %v", call.status, call.body)
	}
	if _, status := dialRemoteFeed(t, f.http.URL, id, remoteFeedHeaders(alice)); status != http.StatusNotFound {
		t.Fatalf("deleted feed handshake = %d, want 404", status)
	}
}

// TestRemoteFeedPreviewEndToEnd pins the full path: create shell -> create preview -> feed -> anonymous
// viewer sees snapshot + live output; after the feed disconnects the shell is kept and reconnecting can resume feeding.
func TestRemoteFeedPreviewEndToEnd(t *testing.T) {
	f := newPreviewFixture(t)
	f.createUser(t, "alice")
	alice := f.login(t, "alice", "password-alice")
	id := createRemoteViaAPI(t, f, alice, "alice-terminal")
	_, token := createPreviewViaAPI(t, f, alice, id)

	conn, status := dialRemoteFeed(t, f.http.URL, id, remoteFeedHeaders(alice))
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("feed handshake = %d", status)
	}
	if err := conn.Write(context.Background(), websocket.MessageBinary, []byte("hello-remote\r\n$ ")); err != nil {
		t.Fatal(err)
	}
	// Feed frames are fed into the screen model asynchronously by the server read loop; the viewer's first-frame snapshot is
	// generated at attach time, so the viewer must only be opened after the output has landed in the model, or the snapshot may be empty.
	waitFor(t, func() bool {
		snapshot, err := f.manager.TerminalSnapshot(id)
		return err == nil && strings.Contains(strings.Join(snapshot.Lines, "\n"), "hello-remote")
	})

	viewer := dialPreviewViewer(t, f.http.URL, token)
	ready := readPreviewJSON(t, viewer, 10*time.Second)
	if ready["type"] != "ready" || ready["session_id"] != id {
		t.Fatalf("ready frame = %v", ready)
	}
	snapshot := readPreviewJSON(t, viewer, 10*time.Second)
	if snapshot["type"] != "snapshot" {
		t.Fatalf("second frame = %v, want snapshot", snapshot)
	}
	lines, _ := snapshot["lines"].([]any)
	joined := make([]string, 0, len(lines))
	for _, line := range lines {
		if text, ok := line.(string); ok {
			joined = append(joined, text)
		}
	}
	if !strings.Contains(strings.Join(joined, "\n"), "hello-remote") {
		t.Fatalf("snapshot lines = %v", lines)
	}

	// Live increments: feed frames arrive at the viewer verbatim (binary).
	if err := conn.Write(context.Background(), websocket.MessageBinary, []byte("increment-7\r\n")); err != nil {
		t.Fatal(err)
	}
	frame := readPreviewFrame(t, viewer, 10*time.Second)
	if frame.kind != websocket.MessageBinary || string(frame.data) != "increment-7\r\n" {
		t.Fatalf("live frame = kind %v %q", frame.kind, frame.data)
	}

	// Disconnect keeps the shell: reconnect and resume feeding, and the viewer keeps receiving.
	_ = conn.Close(websocket.StatusNormalClosure, "")
	reconnected, status := dialRemoteFeed(t, f.http.URL, id, remoteFeedHeaders(alice))
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("reconnect feed handshake = %d", status)
	}
	defer func() { _ = reconnected.Close(websocket.StatusNormalClosure, "") }()
	if err := reconnected.Write(context.Background(), websocket.MessageBinary, []byte("after-reconnect\r\n")); err != nil {
		t.Fatal(err)
	}
	frame = readPreviewFrame(t, viewer, 10*time.Second)
	if frame.kind != websocket.MessageBinary || string(frame.data) != "after-reconnect\r\n" {
		t.Fatalf("post-reconnect frame = kind %v %q", frame.kind, frame.data)
	}
}

// TestRemoteFeedEndKeepsSnapshot: the end control frame closes the write direction (connection closes normally, feeding again
// gets a 409 handshake), and the shell with its final screen is kept - a fresh anonymous preview still gets a snapshot with the existing output.
func TestRemoteFeedEndKeepsSnapshot(t *testing.T) {
	f := newPreviewFixture(t)
	f.createUser(t, "alice")
	alice := f.login(t, "alice", "password-alice")
	id := createRemoteViaAPI(t, f, alice, "alice-terminal")

	conn, status := dialRemoteFeed(t, f.http.URL, id, remoteFeedHeaders(alice))
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("feed handshake = %d", status)
	}
	if err := conn.Write(context.Background(), websocket.MessageBinary, []byte("final-screen\r\n")); err != nil {
		t.Fatal(err)
	}
	endFrame, _ := json.Marshal(map[string]any{"type": "end", "exit_code": 0})
	if err := conn.Write(context.Background(), websocket.MessageText, endFrame); err != nil {
		t.Fatal(err)
	}
	// After end, the server closes the connection normally.
	readCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := conn.Read(readCtx); err == nil {
		t.Fatal("feed connection still open after end frame")
	}

	if _, status := dialRemoteFeed(t, f.http.URL, id, remoteFeedHeaders(alice)); status != http.StatusConflict {
		t.Fatalf("feed after end handshake = %d, want 409", status)
	}

	_, token := createPreviewViaAPI(t, f, alice, id)
	viewer := dialPreviewViewer(t, f.http.URL, token)
	readPreviewJSON(t, viewer, 10*time.Second)
	snapshot := readPreviewJSON(t, viewer, 10*time.Second)
	lines, _ := snapshot["lines"].([]any)
	joined := make([]string, 0, len(lines))
	for _, line := range lines {
		if text, ok := line.(string); ok {
			joined = append(joined, text)
		}
	}
	if !strings.Contains(strings.Join(joined, "\n"), "final-screen") {
		t.Fatalf("snapshot after end = %v", lines)
	}
}

// TestRemoteFeedConflictAndBadFrames: a second feed handshake on the same shell gets 409; an invalid control frame
// receives a bad_param error frame and is closed.
func TestRemoteFeedConflictAndBadFrames(t *testing.T) {
	f := newPreviewFixture(t)
	f.createUser(t, "alice")
	alice := f.login(t, "alice", "password-alice")
	id := createRemoteViaAPI(t, f, alice, "alice-terminal")

	first, status := dialRemoteFeed(t, f.http.URL, id, remoteFeedHeaders(alice))
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("first feed handshake = %d", status)
	}
	defer func() { _ = first.Close(websocket.StatusNormalClosure, "") }()
	if _, status := dialRemoteFeed(t, f.http.URL, id, remoteFeedHeaders(alice)); status != http.StatusConflict {
		t.Fatalf("second feed handshake = %d, want 409", status)
	}

	if err := first.Write(context.Background(), websocket.MessageText, []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	frame := readPreviewFrame(t, first, 10*time.Second)
	if frame.kind != websocket.MessageText {
		t.Fatalf("error frame kind = %v, want text", frame.kind)
	}
	var decoded map[string]any
	if err := json.Unmarshal(frame.data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["type"] != "error" || decoded["code"] != "bad_param" {
		t.Fatalf("error frame = %v, want bad_param", decoded)
	}
	readCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := first.Read(readCtx); err == nil {
		t.Fatal("feed connection still open after bad frame")
	}
}

// TestRemoteFeedResize: the resize control frame syncs the shell size, and subsequent preview snapshots reflect the new size.
func TestRemoteFeedResize(t *testing.T) {
	f := newPreviewFixture(t)
	f.createUser(t, "alice")
	alice := f.login(t, "alice", "password-alice")
	id := createRemoteViaAPI(t, f, alice, "alice-terminal")

	conn, status := dialRemoteFeed(t, f.http.URL, id, remoteFeedHeaders(alice))
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("feed handshake = %d", status)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	resizeFrame, _ := json.Marshal(map[string]any{"type": "resize", "cols": 132, "rows": 43})
	if err := conn.Write(context.Background(), websocket.MessageText, resizeFrame); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		info, err := f.manager.RemoteSessionInfo(id)
		if err != nil {
			t.Fatal(err)
		}
		if info.Cols == 132 && info.Rows == 43 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("remote info after resize = %+v", info)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRemoteSessionLimitRejected: exceeding the per-account shell limit returns 403.
func TestRemoteSessionLimitRejected(t *testing.T) {
	f := newPreviewFixture(t)
	f.createUser(t, "alice")
	alice := f.login(t, "alice", "password-alice")
	for i := 0; i < session.MaxRemoteSessionsPerUser; i++ {
		createRemoteViaAPI(t, f, alice, "shell")
	}
	call := f.call(t, http.MethodPost, "/share/remote-sessions", map[string]any{"title": "one-too-many"}, alice, alice.csrf, nil)
	if call.status != http.StatusForbidden {
		t.Fatalf("over-limit create status = %d body = %v, want 403", call.status, call.body)
	}
}

// TestRemoteSessionsAbsentInSyncOnly: sync-only deployments do not mount remote shell endpoints (same lifecycle as previews);
// auth=off uniformly rejects account routes via accountGuard, covered by existing tests.
func TestRemoteSessionsAbsentInSyncOnly(t *testing.T) {
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

	response, err := httpServer.Client().Post(httpServer.URL+"/share/remote-sessions", "application/json", strings.NewReader(`{"title":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("create (sync-only) status = %d, want 404", response.StatusCode)
	}
	request, err := http.NewRequest(http.MethodDelete, httpServer.URL+"/share/remote-sessions/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("delete (sync-only) status = %d, want 404", response.StatusCode)
	}
}
