//go:build unix

package fleetserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/coder/websocket"
)

// shareTestAgent is an in-process fake agent: the control channel hello reports a real state digest,
// and bridge requests go through agent.BridgePipe (the same path as runtime.handleBridge) to a
// real in-process supervisor server, so the whole share data plane (viewer WS ->
// Gate -> bridge -> supervisor protocol) is the real implementation.
type shareTestAgent struct {
	fixture    *httpFixture
	deviceID   string
	secret     string
	stateDir   string
	socketPath string
	digest     string
	supervisor *supervisor.Supervisor
	server     *supervisor.Server

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func startShareTestAgent(t *testing.T, f *httpFixture, owner *account.User, name string) *shareTestAgent {
	t.Helper()
	deviceID, secret := enrollAgent(t, f, owner, name)
	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sup, err := supervisor.New(supervisor.Config{StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "nx-share-sup-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	socketPath := filepath.Join(runDir, "supervisor.sock")
	server, err := supervisor.NewServer(sup, socketPath)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := supervisor.StateDigest(stateDir, strconv.Itoa(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &shareTestAgent{
		fixture: f, deviceID: deviceID, secret: secret, stateDir: stateDir,
		socketPath: socketPath, digest: digest, supervisor: sup, server: server, ctx: ctx, cancel: cancel,
	}
	t.Cleanup(a.stop)

	control := dialDeviceWS(t, f)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret, StateDigest: digest,
	}); response.Type != "hello_ok" {
		t.Fatalf("control hello = %+v", response)
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		for {
			var message controlMessage
			_, raw, err := control.Read(a.ctx)
			if err != nil {
				return
			}
			if err := json.Unmarshal(raw, &message); err != nil {
				continue
			}
			if message.Type == "bridge" && message.BridgeID != "" {
				a.handleBridge(message.BridgeID)
			}
		}
	}()
	return a
}

func (a *shareTestAgent) handleBridge(bridgeID string) {
	conn, err := agent.DialBridge(a.ctx, "ws"+strings.TrimPrefix(a.fixture.http.URL, "http")+agent.PathDeviceWS, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: a.deviceID, Secret: a.secret, BridgeID: bridgeID,
	}, false)
	if err != nil {
		return
	}
	spawn := func(ctx context.Context, _ string) (io.ReadWriteCloser, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", a.socketPath)
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		_ = agent.BridgePipe(a.ctx, conn, spawn, a.stateDir)
	}()
}

func (a *shareTestAgent) stop() {
	a.cancel()
	a.wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = a.server.Shutdown(ctx)
	_ = a.supervisor.Close()
}

// createSession creates a session directly on the fake agent's supervisor (the share target of public links).
func (a *shareTestAgent) createSession(t *testing.T, command ...string) string {
	t.Helper()
	client := supervisor.NewClient(a.socketPath, a.stateDir)
	info, err := client.Create(context.Background(), supervisor.CreateOptions{Command: command})
	if err != nil {
		t.Fatal(err)
	}
	return info.ID
}

func (a *shareTestAgent) sessionExists(id string) (bool, error) {
	infos, err := supervisor.NewClient(a.socketPath, a.stateDir).List(context.Background())
	if err != nil {
		return false, err
	}
	for _, info := range infos {
		if info.ID == id {
			return true, nil
		}
	}
	return false, nil
}

func (a *shareTestAgent) waitSessionMissing(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		exists, err := a.sessionExists(id)
		if err == nil && !exists {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s was not reclaimed: exists=%v err=%v", id, exists, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (a *shareTestAgent) requireSession(t *testing.T, id string) {
	t.Helper()
	exists, err := a.sessionExists(id)
	if err != nil || !exists {
		t.Fatalf("existing session %s was killed: exists=%v err=%v", id, exists, err)
	}
}

// shareConnCollector continuously reads the viewer WS (blocking reads, no timeout reads - a timeout read poisons the
// coder/websocket connection), splitting messages into the data/text channels, with closes/errors going to errCh.
type shareConnCollector struct {
	conn *websocket.Conn

	data  chan []byte
	text  chan []byte
	errCh chan error
}

func watchShareConn(conn *websocket.Conn) *shareConnCollector {
	collector := &shareConnCollector{
		conn: conn, data: make(chan []byte, 64), text: make(chan []byte, 8), errCh: make(chan error, 1),
	}
	go func() {
		for {
			kind, payload, err := conn.Read(context.Background())
			if err != nil {
				collector.errCh <- err
				return
			}
			if kind == websocket.MessageBinary {
				collector.data <- payload
			} else {
				collector.text <- payload
			}
		}
	}()
	return collector
}

type shareCollectedFrame struct {
	kind    websocket.MessageType
	payload []byte
	err     error
}

func (c *shareConnCollector) next(timeout time.Duration) shareCollectedFrame {
	select {
	case payload := <-c.data:
		return shareCollectedFrame{kind: websocket.MessageBinary, payload: payload}
	case payload := <-c.text:
		return shareCollectedFrame{kind: websocket.MessageText, payload: payload}
	case err := <-c.errCh:
		return shareCollectedFrame{err: err}
	case <-time.After(timeout):
		return shareCollectedFrame{err: errShareReadTimeout}
	}
}

var errShareReadTimeout = &shareReadTimeoutError{}

type shareReadTimeoutError struct{}

func (*shareReadTimeoutError) Error() string { return "share read timed out" }

// expectReady waits on the control channel without consuming or reordering binary terminal output.
func (c *shareConnCollector) expectReady(t *testing.T, permission string) shareReadyFrame {
	t.Helper()
	var payload []byte
	select {
	case payload = <-c.text:
	case err := <-c.errCh:
		t.Fatalf("read ready frame: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatalf("read ready frame: %v", errShareReadTimeout)
	}
	var ready shareReadyFrame
	if err := json.Unmarshal(payload, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.Type != "ready" || ready.Permission != permission || ready.SessionID == "" || ready.ExpiresAt <= 0 {
		t.Fatalf("ready frame = %+v", ready)
	}
	return ready
}

// expectOutput accumulates binary output until it contains substr or times out.
func (c *shareConnCollector) expectOutput(t *testing.T, substr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var seen []byte
	for time.Now().Before(deadline) {
		frame := c.next(time.Until(deadline))
		if frame.err != nil {
			t.Fatalf("waiting for %q: %v (seen %q)", substr, frame.err, seen)
		}
		if frame.kind != websocket.MessageBinary {
			continue
		}
		seen = append(seen, frame.payload...)
		if strings.Contains(string(seen), substr) {
			return
		}
	}
	t.Fatalf("output %q never contained %q", seen, substr)
}

// drain consumes all output already arrived before input, so trailing bytes of the previous command are not mistaken for new output.
func (c *shareConnCollector) drain(t *testing.T, duration time.Duration) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		frame := c.next(time.Until(deadline))
		if frame.err == errShareReadTimeout {
			return
		}
		if frame.err != nil {
			t.Fatalf("drain output: %v", frame.err)
		}
	}
}

// expectSilent asserts that no binary output arrives within duration (used when input is discarded under read-only/after shrinkage).
func (c *shareConnCollector) expectSilent(t *testing.T, duration time.Duration) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		frame := c.next(time.Until(deadline))
		if frame.err == errShareReadTimeout {
			return
		}
		if frame.err != nil {
			t.Fatalf("expect silent: %v", frame.err)
		}
		if frame.kind == websocket.MessageBinary && len(frame.payload) > 0 {
			t.Fatalf("expect silent, got binary payload %q", frame.payload)
		}
	}
}

// expectTerminated reads until the server closes the connection with the share-terminated status and returns the error
// control frame if one arrived first: pumpShareTerminal writes the frame only best-effort before closing, so under load
// the close may legitimately arrive alone; the terminal condition of a stopped share is the close itself.
func (c *shareConnCollector) expectTerminated(t *testing.T, timeout time.Duration) *shareErrorFrame {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var errorFrame *shareErrorFrame
	for time.Now().Before(deadline) {
		frame := c.next(time.Until(deadline))
		if frame.err != nil {
			if frame.err == errShareReadTimeout {
				break
			}
			var closeError websocket.CloseError
			if !errors.As(frame.err, &closeError) || closeError.Code != websocket.StatusPolicyViolation || closeError.Reason != "share terminated" {
				t.Fatalf("share closed with %v; want StatusPolicyViolation %q", frame.err, "share terminated")
			}
			return errorFrame
		}
		if frame.kind != websocket.MessageText {
			continue
		}
		var received shareErrorFrame
		if err := json.Unmarshal(frame.payload, &received); err != nil {
			t.Fatal(err)
		}
		if received.Type == "error" {
			if errorFrame != nil {
				t.Fatalf("second error frame = %+v after %+v", received, *errorFrame)
			}
			errorFrame = &received
		}
	}
	t.Fatal("connection was not closed in time")
	return nil
}

// expectClosed drains pending messages, then asserts the connection is closed within timeout.
func (c *shareConnCollector) expectClosed(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		frame := c.next(time.Until(deadline))
		if frame.err == errShareReadTimeout {
			t.Fatal("connection was not closed in time")
		}
		if frame.err != nil {
			return
		}
	}
	t.Fatal("connection was not closed in time")
}

// collectOutput accumulates all binary output within duration and returns it.
func (c *shareConnCollector) collectOutput(t *testing.T, duration time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(duration)
	var seen []byte
	for time.Now().Before(deadline) {
		frame := c.next(time.Until(deadline))
		if frame.err == errShareReadTimeout {
			return seen
		}
		if frame.err != nil {
			t.Fatalf("collect output: %v", frame.err)
		}
		if frame.kind == websocket.MessageBinary {
			seen = append(seen, frame.payload...)
		}
	}
	return seen
}

func (c *shareConnCollector) sendBinary(t *testing.T, payload []byte) {
	t.Helper()
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.conn.Write(writeCtx, websocket.MessageBinary, payload); err != nil {
		t.Fatal(err)
	}
}

func dialShareViewer(t *testing.T, f *httpFixture, path string, session *httpSession) *websocket.Conn {
	t.Helper()
	options := &websocket.DialOptions{}
	if session != nil {
		options.HTTPHeader = http.Header{"Cookie": []string{session.cookie.Name + "=" + session.cookie.Value}}
	}
	options.CompressionMode = websocket.CompressionContextTakeover
	conn, response, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(f.http.URL, "http")+path, options)
	if err != nil {
		t.Fatal(err)
	}
	if got := response.Header.Get("Sec-WebSocket-Extensions"); !strings.Contains(got, "permessage-deflate") {
		t.Fatalf("compression extensions = %q, want permessage-deflate", got)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	return conn
}

func expectShareViewerStatus(t *testing.T, f *httpFixture, path string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := dialShareViewerStatus(t, f, path)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %d, want %d", got, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// dialShareViewerStatus starts a WS handshake and returns the HTTP status code the server rejects with; 0
// if the handshake unexpectedly succeeds. Used to assert that token/auth gating only applies on the upgrade path (plain GETs
// are taken over by the SPA diversion).
func dialShareViewerStatus(t *testing.T, f *httpFixture, path string) int {
	t.Helper()
	conn, response, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(f.http.URL, "http")+path, nil)
	if err == nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return 0
	}
	if response == nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	return response.StatusCode
}

func createShareLink(t *testing.T, f *httpFixture, session *httpSession, deviceID, sessionID string, write bool) (string, string) {
	t.Helper()
	identity, err := f.accounts.ValidateSession(context.Background(), session.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	link, token, err := f.service.sharing.CreateLink(context.Background(), identity, deviceID, sessionID, write, time.Hour)
	if err != nil {
		t.Fatalf("seed legacy link: %v", err)
	}
	return link.ID, token
}

func auditPayloads(t *testing.T, f *httpFixture, kind string) []map[string]any {
	t.Helper()
	return auditPayloadRows(t, f.service.db, "sharing", kind)
}

func TestShareLinkManagement(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "link-owner")
	other := f.createUser(t, "link-other")
	device := startShareTestAgent(t, f, owner, "link-box")
	sessionID := device.createSession(t, "sh", "-c", "echo READY; exec cat")
	ownerSession := f.session(t, owner)

	if call := f.call(t, "GET", "/share/links", nil, nil, ""); call.status != http.StatusUnauthorized {
		t.Fatalf("anonymous list: HTTP %d", call.status)
	}
	if call := f.call(t, "POST", "/share/links", map[string]any{
		"device_id": device.deviceID, "session_id": sessionID, "write": false, "ttl_ms": 3600000,
	}, ownerSession, ownerSession.csrf); call.status != http.StatusMethodNotAllowed {
		t.Fatalf("retired create route: HTTP %d %v, want 405", call.status, call.body)
	}

	linkID, token := createShareLink(t, f, ownerSession, device.deviceID, sessionID, false)
	if token == "" {
		t.Fatal("empty token")
	}

	// Non-owner revoke is rejected with an audit written.
	otherSession := f.session(t, other)
	if call := f.call(t, "POST", "/share/links/"+linkID+"/revoke", nil, otherSession, otherSession.csrf); call.status != http.StatusForbidden {
		t.Fatalf("other revoke: HTTP %d", call.status)
	}
	// The non-owner's list is empty.
	if call := f.call(t, "GET", "/share/links", nil, otherSession, ""); call.status != http.StatusOK || len(call.body["links"].([]any)) != 0 {
		t.Fatalf("other list: HTTP %d %v", call.status, call.body)
	}

	// The owner list contains the link and never carries token/hash.
	call := f.call(t, "GET", "/share/links", nil, ownerSession, "")
	if call.status != http.StatusOK {
		t.Fatalf("owner list: HTTP %d %v", call.status, call.body)
	}
	links := call.body["links"].([]any)
	if len(links) != 1 {
		t.Fatalf("owner links = %v", links)
	}
	row := links[0].(map[string]any)
	for _, forbidden := range []string{"token", "token_hash", "secret"} {
		if _, exists := row[forbidden]; exists {
			t.Fatalf("link view leaks %q: %v", forbidden, row)
		}
	}
	if row["permission"] != "read" || row["session_id"] != sessionID || row["device_id"] != device.deviceID {
		t.Fatalf("link view = %v", row)
	}

	if call := f.call(t, "POST", "/share/links/"+linkID+"/revoke", nil, ownerSession, ownerSession.csrf); call.status != http.StatusOK {
		t.Fatalf("owner revoke: HTTP %d %v", call.status, call.body)
	}
	// After revocation the token is instantly unusable (audit reason=revoked); the gate goes through the WS upgrade path.
	if status := dialShareViewerStatus(t, f, "/share/public/"+token); status != http.StatusForbidden {
		t.Fatalf("revoked resolve: HTTP %d", status)
	}
	rows := auditPayloads(t, f, "share_link_access")
	last := rows[len(rows)-1]
	if last["outcome"] != "deny" || last["reason"] != "revoked" {
		t.Fatalf("revoke audit = %v", last)
	}
}

func TestSharePublicPageServesSPA(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "page-owner")
	device := startShareTestAgent(t, f, owner, "page-box")
	sessionID := device.createSession(t, "sh", "-c", "echo READY-42; exec cat")
	ownerSession := f.session(t, owner)
	_, token := createShareLink(t, f, ownerSession, device.deviceID, sessionID, false)

	const page = "<html><head><title>NexTerm</title></head><body>share-viewer</body></html>"
	var gotPath, gotRawPath, gotRawQuery string
	f.service.SetSharePublicPage(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotRawPath, gotRawQuery = r.URL.Path, r.URL.RawPath, r.URL.RawQuery
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(page))
	}))

	// Plain GETs with valid/invalid tokens both get the SPA directly: no token verification, no audit.
	for _, path := range []string{"/share/public/" + token, "/share/public/not-a-token"} {
		response, err := f.client.Get(f.http.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK || string(body) != page {
			t.Fatalf("GET %s = %d %q", path, response.StatusCode, body)
		}
		if response.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s Cache-Control = %q, want no-store", path, response.Header.Get("Cache-Control"))
		}
	}
	if rows := auditPayloads(t, f, "share_link_access"); len(rows) != 0 {
		t.Fatalf("plain GET wrote share_link_access audit rows: %v", rows)
	}

	// The path the static entry receives is pinned to "/" (no file lookup on token paths), with the query preserved.
	request, err := http.NewRequest(http.MethodGet, f.http.URL+"/share/public/not-a-token?next=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if gotPath != "/" || gotRawPath != "" || gotRawQuery != "next=1" {
		t.Fatalf("static entry got path %q rawPath %q query %q, want /, empty, next=1", gotPath, gotRawPath, gotRawQuery)
	}

	// Token gating on the upgrade path is unchanged: a WS handshake with an invalid token still gets 403.
	if status := dialShareViewerStatus(t, f, "/share/public/not-a-token"); status != http.StatusForbidden {
		t.Fatalf("invalid token WS handshake = %d, want 403", status)
	}
}

func TestSharePublicPageWithoutStaticEntry(t *testing.T) {
	f := newHTTPFixture(t, false)
	// Without a configured static entry: plain GET 404s; the upgrade contract is unaffected (invalid token still 403).
	response, err := f.client.Get(f.http.URL + "/share/public/whatever")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("plain GET without static entry = %d, want 404", response.StatusCode)
	}
	if status := dialShareViewerStatus(t, f, "/share/public/whatever"); status != http.StatusForbidden {
		t.Fatalf("invalid token WS handshake = %d, want 403", status)
	}
}

func TestSharePublicTerminalReadOnly(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "ro-owner")
	device := startShareTestAgent(t, f, owner, "ro-box")
	sessionID := device.createSession(t, "sh", "-c", "echo READY-42; exec cat")
	ownerSession := f.session(t, owner)
	_, token := createShareLink(t, f, ownerSession, device.deviceID, sessionID, false)

	conn := dialShareViewer(t, f, "/share/public/"+token, nil)
	collector := watchShareConn(conn)
	ready := collector.expectReady(t, "read")
	if ready.SessionID != sessionID {
		t.Fatalf("ready session = %q want %q", ready.SessionID, sessionID)
	}
	collector.expectOutput(t, "READY-42", 10*time.Second)

	collector.drain(t, 100*time.Millisecond)
	// Read-only: input is silently discarded, the connection stays up, and no output is produced.
	collector.sendBinary(t, []byte("echo nope-42\n"))
	collector.expectSilent(t, 1500*time.Millisecond)
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

func TestSharePublicTerminalReadWriteAndInputAudit(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "rw-owner")
	device := startShareTestAgent(t, f, owner, "rw-box")
	sessionID := device.createSession(t, "sh", "-c", "echo READY-42; exec cat")
	ownerSession := f.session(t, owner)
	_, token := createShareLink(t, f, ownerSession, device.deviceID, sessionID, true)

	conn := dialShareViewer(t, f, "/share/public/"+token, nil)
	collector := watchShareConn(conn)
	collector.expectReady(t, "read_write")
	collector.expectOutput(t, "READY-42", 10*time.Second)

	collector.sendBinary(t, []byte("echo hello-42\n"))
	collector.expectOutput(t, "hello-42", 10*time.Second)

	// The input action was audited (allow) and carries no token.
	inputs := auditPayloads(t, f, "share_link_input")
	if len(inputs) == 0 {
		t.Fatal("no share_link_input audit rows")
	}
	allowed := false
	for _, payload := range inputs {
		if payload["outcome"] == "allow" {
			allowed = true
		}
		if _, leaks := payload["token"]; leaks {
			t.Fatalf("input audit leaks token: %v", payload)
		}
	}
	if !allowed {
		t.Fatalf("input audit rows = %v", inputs)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
	time.Sleep(100 * time.Millisecond)
	device.requireSession(t, sessionID)
}

func TestSharePublicTerminalRevokeStopsStream(t *testing.T) {
	f := newHTTPFixture(t, false, WithShareRevalidateInterval(100*time.Millisecond))
	owner := f.createUser(t, "revoke-owner")
	device := startShareTestAgent(t, f, owner, "revoke-box")
	sessionID := device.createSession(t, "sh", "-c", "echo READY-42; exec cat")
	ownerSession := f.session(t, owner)
	linkID, token := createShareLink(t, f, ownerSession, device.deviceID, sessionID, true)

	conn := dialShareViewer(t, f, "/share/public/"+token, nil)
	collector := watchShareConn(conn)
	collector.expectReady(t, "read_write")

	if call := f.call(t, "POST", "/share/links/"+linkID+"/revoke", nil, ownerSession, ownerSession.csrf); call.status != http.StatusOK {
		t.Fatalf("revoke: HTTP %d %v", call.status, call.body)
	}
	errorFrame := collector.expectTerminated(t, 15*time.Second)
	if errorFrame != nil && (errorFrame.Code != "forbidden" || !strings.Contains(errorFrame.Message, "吊销")) {
		t.Fatalf("error frame = %+v", *errorFrame)
	}

	// After revocation, new connections are rejected before the upgrade.
	if status := dialShareViewerStatus(t, f, "/share/public/"+token); status != http.StatusForbidden {
		t.Fatalf("resolve after revoke: HTTP %d", status)
	}
}

func TestSharePublicTerminalExpiryStopsStream(t *testing.T) {
	f := newHTTPFixture(t, false, WithShareRevalidateInterval(100*time.Millisecond))
	owner := f.createUser(t, "expiry-owner")
	device := startShareTestAgent(t, f, owner, "expiry-box")
	sessionID := device.createSession(t, "sh", "-c", "echo READY-42; exec cat")
	ownerSession := f.session(t, owner)
	_, token := createShareLink(t, f, ownerSession, device.deviceID, sessionID, false)

	conn := dialShareViewer(t, f, "/share/public/"+token, nil)
	collector := watchShareConn(conn)
	// The read-only link goes through the discardInput path, covering the regression where a canceled read ctx kills the connection (SHARE138).
	collector.expectReady(t, "read")

	shareLink := query.Use(f.service.db).ShareLink
	if _, err := shareLink.WithContext(context.Background()).
		Where(shareLink.DeviceID.Eq(device.deviceID)).
		UpdateColumns(map[string]any{"expires_at": time.Now().UnixMilli() - 1000}); err != nil {
		t.Fatal(err)
	}
	errorFrame := collector.expectTerminated(t, 15*time.Second)
	if errorFrame != nil && !strings.Contains(errorFrame.Message, "过期") {
		t.Fatalf("error frame = %+v", *errorFrame)
	}
}

func TestShareHostTerminalOpenAndShrink(t *testing.T) {
	f := newHTTPFixture(t, false, WithShareRevalidateInterval(100*time.Millisecond))
	owner := f.createUser(t, "host-owner")
	recipient := f.createUser(t, "host-recipient")
	stranger := f.createUser(t, "host-stranger")
	device := startShareTestAgent(t, f, owner, "host-box")
	ownerSession := f.session(t, owner)
	recipientSession := f.session(t, recipient)

	// A registered user without a share is denied open (403, with an audit written).
	if call := f.call(t, "GET", "/share/devices/"+device.deviceID+"/terminal", nil, f.session(t, stranger), ""); call.status != http.StatusForbidden {
		t.Fatalf("stranger open: HTTP %d %v", call.status, call.body)
	}

	call := f.call(t, "POST", "/share/host-shares", map[string]any{
		"device_id": device.deviceID, "recipient_username": "host-recipient", "write": true, "ttl_ms": 3600000,
	}, ownerSession, ownerSession.csrf)
	if call.status != http.StatusOK {
		t.Fatalf("create host share: HTTP %d %v", call.status, call.body)
	}
	if call.body["permission"] != "read_write" || call.body["recipient_username"] != "host-recipient" {
		t.Fatalf("host share view = %v", call.body)
	}

	// The recipient sees the share in the list (as the recipient), and list rows carry both usernames.
	listCall := f.call(t, "GET", "/share/host-shares", nil, recipientSession, "")
	if listCall.status != http.StatusOK || len(listCall.body["shares"].([]any)) != 1 {
		t.Fatalf("recipient list: HTTP %d %v", listCall.status, listCall.body)
	}
	listed := listCall.body["shares"].([]any)[0].(map[string]any)
	if listed["owner_username"] != "host-owner" || listed["recipient_username"] != "host-recipient" {
		t.Fatalf("host share list view = %v", listed)
	}

	conn := dialShareViewer(t, f, "/share/devices/"+device.deviceID+"/terminal", recipientSession)
	collector := watchShareConn(conn)
	ready := collector.expectReady(t, "read_write")
	if ready.SessionID == "" {
		t.Fatal("host terminal ready has no session id")
	}

	// The expected values are produced by shell arithmetic, so command echo alone cannot satisfy the assertions.
	marker := strconv.FormatInt(time.Now().UnixNano(), 36)
	liveOutput := "live-" + marker + "-42"
	collector.sendBinary(t, []byte("while true; do echo live-"+marker+"-$((40+2)); sleep 0.2; done\n"))
	collector.expectOutput(t, liveOutput, 10*time.Second)

	call = f.call(t, "POST", "/share/host-shares", map[string]any{
		"device_id": device.deviceID, "recipient_username": "host-recipient", "write": false, "ttl_ms": 3600000,
	}, ownerSession, ownerSession.csrf)
	if call.status != http.StatusOK || call.body["permission"] != "read" {
		t.Fatalf("shrink share: HTTP %d %v", call.status, call.body)
	}

	// The periodic revalidation publishes the shrunk permission on the same terminal session.
	updated := collector.expectReady(t, "read")
	if updated.SessionID != ready.SessionID {
		t.Fatalf("updated ready session = %q, want %q", updated.SessionID, ready.SessionID)
	}
	// After the shrink takes effect, input is discarded and existing output keeps flowing.
	blockedOutput := "blocked-" + marker + "-42"
	collector.sendBinary(t, []byte("echo blocked-"+marker+"-$((40+2))\n"))
	output := collector.collectOutput(t, 1500*time.Millisecond)
	if !strings.Contains(string(output), liveOutput) {
		t.Fatalf("output did not continue after shrink: %q", output)
	}
	if strings.Contains(string(output), blockedOutput) {
		t.Fatalf("input leaked after shrink: %q", output)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")

	// After revocation, open is denied.
	call = f.call(t, "GET", "/share/host-shares", nil, ownerSession, "")
	if call.status != http.StatusOK {
		t.Fatalf("owner host list: HTTP %d", call.status)
	}
	shareID := call.body["shares"].([]any)[0].(map[string]any)["id"].(string)
	if call := f.call(t, "POST", "/share/host-shares/"+shareID+"/revoke", nil, ownerSession, ownerSession.csrf); call.status != http.StatusOK {
		t.Fatalf("revoke host share: HTTP %d %v", call.status, call.body)
	}
	if call := f.call(t, "GET", "/share/devices/"+device.deviceID+"/terminal", nil, recipientSession, ""); call.status != http.StatusForbidden {
		t.Fatalf("open after revoke: HTTP %d", call.status)
	}
}

func TestShareHostTerminalRecipientSessionInvalidation(t *testing.T) {
	tests := []struct {
		name       string
		invalidate func(t *testing.T, f *httpFixture, recipient *account.User)
	}{
		{
			name: "logout-all",
			invalidate: func(t *testing.T, f *httpFixture, recipient *account.User) {
				if _, err := f.accounts.RevokeUserSessions(context.Background(), recipient.ID, ""); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "disabled",
			invalidate: func(t *testing.T, f *httpFixture, recipient *account.User) {
				if err := f.accounts.SetUserDisabled(context.Background(), recipient.ID, true); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "expired",
			invalidate: func(t *testing.T, f *httpFixture, recipient *account.User) {
				userSession := query.Use(f.service.db).UserSession
				if _, err := userSession.WithContext(context.Background()).Where(userSession.UserID.Eq(recipient.ID)).
					UpdateColumns(map[string]any{"expires_at": time.Now().Add(-time.Second).UnixMilli()}); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newHTTPFixture(t, false, WithShareRevalidateInterval(100*time.Millisecond))
			owner := f.createUser(t, "session-owner-"+test.name)
			recipient := f.createUser(t, "session-recipient-"+test.name)
			device := startShareTestAgent(t, f, owner, "session-box-"+test.name)
			ownerSession := f.session(t, owner)
			recipientSession := f.session(t, recipient)
			call := f.call(t, "POST", "/share/host-shares", map[string]any{
				"device_id": device.deviceID, "recipient_username": recipient.Username, "write": true, "ttl_ms": 3600000,
			}, ownerSession, ownerSession.csrf)
			if call.status != http.StatusOK {
				t.Fatalf("create host share: HTTP %d %v", call.status, call.body)
			}
			conn := dialShareViewer(t, f, "/share/devices/"+device.deviceID+"/terminal", recipientSession)
			collector := watchShareConn(conn)
			ready := collector.expectReady(t, "read_write")
			collector.sendBinary(t, []byte("echo before-$((40+2))\n"))
			collector.expectOutput(t, "before-42", 10*time.Second)

			test.invalidate(t, f, recipient)
			if failure := collector.expectTerminated(t, 15*time.Second); failure != nil && failure.Code != "forbidden" {
				t.Fatalf("session invalidation error = %+v, want forbidden", failure)
			}
			device.waitSessionMissing(t, ready.SessionID)
			writeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := conn.Write(writeCtx, websocket.MessageBinary, []byte("echo after-42\n")); err == nil {
				t.Fatal("input succeeded after the recipient session was invalidated")
			}
		})
	}
}

func TestShareHostShareCreateByUsername(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "byname-owner")
	recipient := f.createUser(t, "byname-recipient")
	disabled := f.createUser(t, "byname-disabled")
	stranger := f.createUser(t, "byname-stranger")
	if err := f.accounts.SetUserDisabled(context.Background(), disabled.ID, true); err != nil {
		t.Fatal(err)
	}
	device := startShareTestAgent(t, f, owner, "byname-box")
	ownerSession := f.session(t, owner)

	// A plain device owner creates by username (case-insensitive), with no user-directory permission needed throughout.
	call := f.call(t, "POST", "/share/host-shares", map[string]any{
		"device_id": device.deviceID, "recipient_username": "ByName-Recipient", "write": false, "ttl_ms": 3600000,
	}, ownerSession, ownerSession.csrf)
	if call.status != http.StatusOK {
		t.Fatalf("create by username: HTTP %d %v", call.status, call.body)
	}
	if call.body["recipient_id"] != recipient.ID || call.body["recipient_username"] != "byname-recipient" || call.body["permission"] != "read" {
		t.Fatalf("host share view = %v", call.body)
	}

	// Unknown username: the existing not_found safe error, and no share is created.
	if call := f.call(t, "POST", "/share/host-shares", map[string]any{
		"device_id": device.deviceID, "recipient_username": "no-such-user", "write": false, "ttl_ms": 3600000,
	}, ownerSession, ownerSession.csrf); call.status != http.StatusNotFound {
		t.Fatalf("unknown recipient: HTTP %d %v", call.status, call.body)
	}

	// Disabled recipient: the existing forbidden safe error (decided and audited by the sharing layer), and no share is created.
	if call := f.call(t, "POST", "/share/host-shares", map[string]any{
		"device_id": device.deviceID, "recipient_username": "byname-disabled", "write": false, "ttl_ms": 3600000,
	}, ownerSession, ownerSession.csrf); call.status != http.StatusForbidden {
		t.Fatalf("disabled recipient: HTTP %d %v", call.status, call.body)
	}

	// A plain user who is not the device owner cannot create on that device (the server gates on device owner only).
	strangerSession := f.session(t, stranger)
	if call := f.call(t, "POST", "/share/host-shares", map[string]any{
		"device_id": device.deviceID, "recipient_username": "byname-recipient", "write": false, "ttl_ms": 3600000,
	}, strangerSession, strangerSession.csrf); call.status != http.StatusForbidden {
		t.Fatalf("stranger create: HTTP %d %v", call.status, call.body)
	}

	// Unauthorized + nonexistent username: authorization and the deny audit come before username resolution, so it is still 403 not_owner
	// (never degrading into an unaudited 404, never revealing whether the recipient exists).
	if call := f.call(t, "POST", "/share/host-shares", map[string]any{
		"device_id": device.deviceID, "recipient_username": "no-such-user", "write": false, "ttl_ms": 3600000,
	}, strangerSession, strangerSession.csrf); call.status != http.StatusForbidden {
		t.Fatalf("stranger create with unknown recipient: HTTP %d %v", call.status, call.body)
	}

	// The failure paths left no share rows: the list still has only the original one.
	listCall := f.call(t, "GET", "/share/host-shares", nil, ownerSession, "")
	if listCall.status != http.StatusOK || len(listCall.body["shares"].([]any)) != 1 {
		t.Fatalf("owner host list: HTTP %d %v", listCall.status, listCall.body)
	}

	// Audit semantics hold: one allow with share_id for the successful creation; one deny/not_owner for each of the two unauthorized attempts.
	audits := auditPayloads(t, f, "host_share_create")
	allowWithShare, denyNotOwner := 0, 0
	for _, payload := range audits {
		if payload["outcome"] == "allow" && payload["share_id"] != nil && payload["share_id"] != "" {
			allowWithShare++
		}
		if payload["outcome"] == "deny" && payload["reason"] == "not_owner" {
			denyNotOwner++
		}
	}
	if allowWithShare != 1 || denyNotOwner != 2 {
		t.Fatalf("host_share_create audits = %v", audits)
	}
}

func TestShareDaemonOfflineStopsOpenAndStream(t *testing.T) {
	f := newHTTPFixture(t, false, WithShareRevalidateInterval(100*time.Millisecond))
	owner := f.createUser(t, "offline-owner")
	recipient := f.createUser(t, "offline-recipient")
	device := startShareTestAgent(t, f, owner, "offline-box")
	sessionID := device.createSession(t, "sh", "-c", "echo READY-42; exec cat")
	ownerSession := f.session(t, owner)
	recipientSession := f.session(t, recipient)

	if call := f.call(t, "POST", "/share/host-shares", map[string]any{
		"device_id": device.deviceID, "recipient_username": "offline-recipient", "write": true, "ttl_ms": 3600000,
	}, ownerSession, ownerSession.csrf); call.status != http.StatusOK {
		t.Fatalf("create host share: HTTP %d %v", call.status, call.body)
	}
	_, token := createShareLink(t, f, ownerSession, device.deviceID, sessionID, true)

	conn := dialShareViewer(t, f, "/share/public/"+token, nil)
	collector := watchShareConn(conn)
	collector.expectReady(t, "read_write")

	// Agent offline: the established share stream stops with the bridge disconnecting.
	device.stop()
	collector.expectClosed(t, 10*time.Second)

	// Opens after offline always get 503 (the probe judges offline instantly, without waiting for last_seen to expire).
	expectShareViewerStatus(t, f, "/share/public/"+token, http.StatusServiceUnavailable)
	if call := f.call(t, "GET", "/share/devices/"+device.deviceID+"/terminal", nil, recipientSession, ""); call.status != http.StatusServiceUnavailable {
		t.Fatalf("offline host open: HTTP %d %v", call.status, call.body)
	}
}

func TestShareRoutesAuthOff(t *testing.T) {
	f := newHTTPFixture(t, true)
	session := f.session(t, f.createUser(t, "off-user"))
	calls := []httpCall{
		f.call(t, "GET", "/share/links", nil, session, ""),
		f.call(t, "POST", "/share/links/x/revoke", nil, session, session.csrf),
		f.call(t, "POST", "/share/host-shares", map[string]any{"device_id": "d", "recipient_username": "r"}, session, session.csrf),
		f.call(t, "GET", "/share/host-shares", nil, session, ""),
		f.call(t, "POST", "/share/host-shares/x/revoke", nil, session, session.csrf),
		f.call(t, "GET", "/share/public/sometoken", nil, nil, ""),
		f.call(t, "GET", "/share/devices/d/terminal", nil, session, ""),
	}
	for i, call := range calls {
		if call.status != http.StatusForbidden {
			t.Fatalf("call %d status = %d want 403 (body %v)", i, call.status, call.body)
		}
	}
}

// The peer accepts the WS and never reads: once the kernel write buffer fills up, writing to the viewer must fail and return
// within the timeout, instead of hanging the output pump and the connection teardown indefinitely (which would also prevent revocation from landing).
func TestShareViewerWriteTimeoutUnblocksStalledViewer(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		<-release
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(func() { close(release); server.Close() })

	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	viewer := &shareTerminalWS{conn: conn, writeTimeout: 100 * time.Millisecond}
	payload := bytes.Repeat([]byte{0xab}, 1<<20)
	start := time.Now()
	var writeErr error
	for range 64 {
		if writeErr = viewer.writeMessage(context.Background(), websocket.MessageBinary, payload); writeErr != nil {
			break
		}
	}
	if writeErr == nil {
		t.Fatal("stalled viewer write never failed")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("stalled write took %s to unblock, want the per-message timeout", elapsed)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// Session public links and device public links share the same rate limit: a random token gets 403 on first try, then 429.
func TestSharePublicLinkResolveRateLimited(t *testing.T) {
	f := newHTTPFixture(t, false)
	if status := dialShareViewerStatus(t, f, "/share/public/not-a-token"); status != http.StatusForbidden {
		t.Fatalf("first invalid resolve = %d, want 403", status)
	}
	if status := dialShareViewerStatus(t, f, "/share/public/not-a-token"); status != http.StatusTooManyRequests {
		t.Fatalf("flooded resolve = %d, want 429", status)
	}
}

func TestSharePublicPageRetiredDeviceNotFound(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "retired-page-owner")
	device := startShareTestAgent(t, f, owner, "retired-page-box")
	sessionID := device.createSession(t, "sh", "-c", "echo READY; exec cat")
	ownerSession := f.session(t, owner)
	_, token := createShareLink(t, f, ownerSession, device.deviceID, sessionID, false)

	const page = "<html><head><title>NexTerm</title></head><body>share-viewer</body></html>"
	var gotPath string
	f.service.SetSharePublicPage(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(page))
	}))

	// Valid and nonexistent tokens keep the SPA diversion (no regression).
	for _, path := range []string{"/share/public/" + token, "/share/public/not-a-token"} {
		response, err := f.client.Get(f.http.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK || string(body) != page {
			t.Fatalf("GET %s = %d %q", path, response.StatusCode, body)
		}
		if response.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s Cache-Control = %q, want no-store", path, response.Header.Get("Cache-Control"))
		}
	}
	if gotPath != "/" {
		t.Fatalf("static entry got path %q, want /", gotPath)
	}

	// Retire the device: the public page now 404s with no-store before the SPA fallback.
	identity, err := f.accounts.ValidateSession(context.Background(), ownerSession.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.RevokeDevice(context.Background(), identity, device.deviceID); err != nil {
		t.Fatal(err)
	}
	gotPath = ""
	response, err := f.client.Get(f.http.URL + "/share/public/" + token)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("retired device page = %d %q, want 404", response.StatusCode, body)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("retired device page Cache-Control = %q, want no-store", response.Header.Get("Cache-Control"))
	}
	if gotPath != "" {
		t.Fatalf("retired device page reached the static entry with path %q", gotPath)
	}

	// The upgrade path still denies the retired device with 403 (data plane unchanged).
	if status := dialShareViewerStatus(t, f, "/share/public/"+token); status != http.StatusForbidden {
		t.Fatalf("retired device WS handshake = %d, want 403", status)
	}

	// Page loads stay audit-free: only the WS resolve wrote an audit row (device_revoked).
	rows := auditPayloads(t, f, "share_link_access")
	if len(rows) != 1 || rows[0]["reason"] != "device_revoked" {
		t.Fatalf("share_link_access audit rows = %v, want only the WS device_revoked denial", rows)
	}
}
