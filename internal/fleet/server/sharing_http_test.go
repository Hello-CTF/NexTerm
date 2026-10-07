//go:build unix

package fleetserver

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	"github.com/ProbiusOfficial/NexTerm/internal/fleet/agent"
	"github.com/ProbiusOfficial/NexTerm/internal/supervisor"
	"github.com/coder/websocket"
)

// shareTestAgent 是进程内假 agent: 控制通道 hello 上报真实 state digest,
// 桥接请求经 agent.BridgePipe (与 runtime.handleBridge 同一通路) 接到一个
// 真实的进程内 supervisor server, 因此分享数据面全链路 (viewer WS ->
// Gate -> 桥接 -> supervisor 协议) 都是真实实现。
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

// createSession 直接在假 agent 的 supervisor 上建会话 (公开链接的分享目标)。
func (a *shareTestAgent) createSession(t *testing.T, command ...string) string {
	t.Helper()
	client := supervisor.NewClient(a.socketPath, a.stateDir)
	info, err := client.Create(context.Background(), supervisor.CreateOptions{Command: command})
	if err != nil {
		t.Fatal(err)
	}
	return info.ID
}

// shareConnCollector 持续读 viewer WS (阻塞读, 不用超时读 — 超时读会毒化
// coder/websocket 连接), 把消息分到 data/text 通道, 关闭/错误进 errCh。
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

// expectReady 读 ready 控制帧并校验权限声明。
func (c *shareConnCollector) expectReady(t *testing.T, permission string) shareReadyFrame {
	t.Helper()
	frame := c.next(10 * time.Second)
	if frame.err != nil {
		t.Fatalf("read ready frame: %v", frame.err)
	}
	if frame.kind != websocket.MessageText {
		t.Fatalf("ready frame kind = %v payload=%q", frame.kind, frame.payload)
	}
	var ready shareReadyFrame
	if err := json.Unmarshal(frame.payload, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.Type != "ready" || ready.Permission != permission || ready.SessionID == "" || ready.ExpiresAt <= 0 {
		t.Fatalf("ready frame = %+v", ready)
	}
	return ready
}

// expectOutput 累积二进制输出直到包含 substr 或超时。
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

// expectSilent 断言 duration 内没有任何二进制输出 (用于只读/收缩后输入被丢弃)。
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

// expectErrorFrame 读到一个 error 控制帧并返回其内容。
func (c *shareConnCollector) expectErrorFrame(t *testing.T, timeout time.Duration) shareErrorFrame {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		frame := c.next(time.Until(deadline))
		if frame.err != nil {
			t.Fatalf("read error frame: %v", frame.err)
		}
		if frame.kind != websocket.MessageText {
			continue
		}
		var errorFrame shareErrorFrame
		if err := json.Unmarshal(frame.payload, &errorFrame); err != nil {
			t.Fatal(err)
		}
		if errorFrame.Type == "error" {
			return errorFrame
		}
	}
	t.Fatal("no error frame arrived")
	return shareErrorFrame{}
}

// expectClosed 排空滞留消息后断言连接在 timeout 内被关闭。
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

// collectOutput 累积 duration 内的全部二进制输出并返回。
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
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(f.http.URL, "http")+path, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	return conn
}

// dialShareViewerStatus 发起 WS 握手并返回服务端拒绝的 HTTP 状态码; 握手
// 意外成功返回 0。用于断言 token/auth 把关只在 upgrade 路径生效 (普通 GET
// 已被 SPA 分流接管)。
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
	call := f.call(t, "POST", "/share/links", map[string]any{
		"device_id": deviceID, "session_id": sessionID, "write": write, "ttl_ms": 3600000,
	}, session, session.csrf)
	if call.status != http.StatusOK {
		t.Fatalf("create link: HTTP %d %v", call.status, call.body)
	}
	link, _ := call.body["id"].(string)
	token, _ := call.body["token"].(string)
	if link == "" || token == "" || call.body["permission"] == "" {
		t.Fatalf("create link body = %v", call.body)
	}
	return link, token
}

func auditPayloads(t *testing.T, f *httpFixture, kind string) []map[string]any {
	t.Helper()
	rows, err := f.service.db.QueryContext(context.Background(), "SELECT payload_json FROM audit_log WHERE source = ? AND kind = ? ORDER BY id", "sharing", kind)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var payloads []map[string]any
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

func TestShareLinkManagement(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "link-owner")
	other := f.createUser(t, "link-other")
	device := startShareTestAgent(t, f, owner, "link-box")
	sessionID := device.createSession(t, "sh", "-c", "echo READY; exec cat")
	ownerSession := f.session(t, owner)

	// 匿名与 CSRF 边界。
	if call := f.call(t, "GET", "/share/links", nil, nil, ""); call.status != http.StatusUnauthorized {
		t.Fatalf("anonymous list: HTTP %d", call.status)
	}
	if call := f.call(t, "POST", "/share/links", map[string]any{"device_id": device.deviceID, "session_id": sessionID}, ownerSession, ""); call.status != http.StatusForbidden {
		t.Fatalf("missing csrf: HTTP %d", call.status)
	}

	linkID, token := createShareLink(t, f, ownerSession, device.deviceID, sessionID, false)
	if token == "" {
		t.Fatal("empty token")
	}

	// 非 owner 吊销被拒且写审计。
	otherSession := f.session(t, other)
	if call := f.call(t, "POST", "/share/links/"+linkID+"/revoke", nil, otherSession, otherSession.csrf); call.status != http.StatusForbidden {
		t.Fatalf("other revoke: HTTP %d", call.status)
	}
	// 非 owner 的列表为空。
	if call := f.call(t, "GET", "/share/links", nil, otherSession, ""); call.status != http.StatusOK || len(call.body["links"].([]any)) != 0 {
		t.Fatalf("other list: HTTP %d %v", call.status, call.body)
	}

	// owner 列表含该链接且绝不携带 token/hash。
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
	// 吊销后 token 立即不可用 (审计 reason=revoked); 把关走 WS upgrade 路径。
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

	// 有效/无效 token 的普通 GET 都直接拿到 SPA: 不校验 token, 不写审计。
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

	// 静态入口收到的路径固定为 "/" (不做 token 路径的文件查找), query 保留。
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

	// upgrade 路径的 token 把关不变: 无效 token 的 WS 握手仍 403。
	if status := dialShareViewerStatus(t, f, "/share/public/not-a-token"); status != http.StatusForbidden {
		t.Fatalf("invalid token WS handshake = %d, want 403", status)
	}
}

func TestSharePublicPageWithoutStaticEntry(t *testing.T) {
	f := newHTTPFixture(t, false)
	// 未配置静态入口: 普通 GET 404; upgrade 合同不受影响 (无效 token 仍 403)。
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

	// 只读: 输入被静默丢弃, 连接保持, 无任何输出。
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

	// 输入动作已写审计 (allow), 且不携带 token。
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
	errorFrame := collector.expectErrorFrame(t, 10*time.Second)
	if errorFrame.Code != "forbidden" || !strings.Contains(errorFrame.Message, "吊销") {
		t.Fatalf("error frame = %+v", errorFrame)
	}
	collector.expectClosed(t, 5*time.Second)

	// 吊销后新连接在升级前即被拒。
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
	// 只读链接走 discardInput 路径, 覆盖读 ctx 取消误杀连接的回归 (SHARE138)。
	collector.expectReady(t, "read")

	if _, err := f.service.db.ExecContext(context.Background(), "UPDATE share_link SET expires_at = ? WHERE device_id = ?", time.Now().UnixMilli()-1000, device.deviceID); err != nil {
		t.Fatal(err)
	}
	errorFrame := collector.expectErrorFrame(t, 10*time.Second)
	if !strings.Contains(errorFrame.Message, "过期") {
		t.Fatalf("error frame = %+v", errorFrame)
	}
	collector.expectClosed(t, 5*time.Second)
}

func TestShareHostTerminalOpenAndShrink(t *testing.T) {
	f := newHTTPFixture(t, false, WithShareRevalidateInterval(100*time.Millisecond))
	owner := f.createUser(t, "host-owner")
	recipient := f.createUser(t, "host-recipient")
	stranger := f.createUser(t, "host-stranger")
	device := startShareTestAgent(t, f, owner, "host-box")
	ownerSession := f.session(t, owner)
	recipientSession := f.session(t, recipient)

	// 无分享的注册用户打开被拒 (403, 写审计)。
	if call := f.call(t, "GET", "/share/devices/"+device.deviceID+"/terminal", nil, f.session(t, stranger), ""); call.status != http.StatusForbidden {
		t.Fatalf("stranger open: HTTP %d %v", call.status, call.body)
	}

	call := f.call(t, "POST", "/share/host-shares", map[string]any{
		"device_id": device.deviceID, "recipient_id": recipient.ID, "write": true, "ttl_ms": 3600000,
	}, ownerSession, ownerSession.csrf)
	if call.status != http.StatusOK {
		t.Fatalf("create host share: HTTP %d %v", call.status, call.body)
	}
	if call.body["permission"] != "read_write" || call.body["recipient_username"] != "host-recipient" {
		t.Fatalf("host share view = %v", call.body)
	}

	// recipient 列表可见 (作为接收方), 列表行携带双方用户名。
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

	// 输入产生输出: 先启动一个持续输出循环, 随后收缩权限。
	collector.sendBinary(t, []byte("while true; do echo tick; sleep 0.2; done\n"))
	collector.expectOutput(t, "tick", 10*time.Second)

	call = f.call(t, "POST", "/share/host-shares", map[string]any{
		"device_id": device.deviceID, "recipient_id": recipient.ID, "write": false, "ttl_ms": 3600000,
	}, ownerSession, ownerSession.csrf)
	if call.status != http.StatusOK || call.body["permission"] != "read" {
		t.Fatalf("shrink share: HTTP %d %v", call.status, call.body)
	}

	// 收缩生效后输入被丢弃, 已有输出继续流动。
	time.Sleep(600 * time.Millisecond)
	collector.sendBinary(t, []byte("echo nope-42\n"))
	output := collector.collectOutput(t, 1500*time.Millisecond)
	if !strings.Contains(string(output), "tick") {
		t.Fatalf("output did not continue after shrink: %q", output)
	}
	if strings.Contains(string(output), "nope-42") {
		t.Fatalf("input leaked after shrink: %q", output)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")

	// 吊销后打开被拒。
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

func TestShareDaemonOfflineStopsOpenAndStream(t *testing.T) {
	f := newHTTPFixture(t, false, WithShareRevalidateInterval(100*time.Millisecond))
	owner := f.createUser(t, "offline-owner")
	recipient := f.createUser(t, "offline-recipient")
	device := startShareTestAgent(t, f, owner, "offline-box")
	sessionID := device.createSession(t, "sh", "-c", "echo READY-42; exec cat")
	ownerSession := f.session(t, owner)
	recipientSession := f.session(t, recipient)

	if call := f.call(t, "POST", "/share/host-shares", map[string]any{
		"device_id": device.deviceID, "recipient_id": recipient.ID, "write": true, "ttl_ms": 3600000,
	}, ownerSession, ownerSession.csrf); call.status != http.StatusOK {
		t.Fatalf("create host share: HTTP %d %v", call.status, call.body)
	}
	_, token := createShareLink(t, f, ownerSession, device.deviceID, sessionID, true)

	conn := dialShareViewer(t, f, "/share/public/"+token, nil)
	collector := watchShareConn(conn)
	collector.expectReady(t, "read_write")

	// agent 掉线: 已建立的分享流随桥接断开而停止。
	device.stop()
	collector.expectClosed(t, 10*time.Second)

	// 掉线后打开/新建一律 503 (探针即时判离线, 不等 last_seen 过期)。
	if status := dialShareViewerStatus(t, f, "/share/public/"+token); status != http.StatusServiceUnavailable {
		t.Fatalf("offline resolve: HTTP %d", status)
	}
	if call := f.call(t, "GET", "/share/devices/"+device.deviceID+"/terminal", nil, recipientSession, ""); call.status != http.StatusServiceUnavailable {
		t.Fatalf("offline host open: HTTP %d %v", call.status, call.body)
	}
	if call := f.call(t, "POST", "/share/links", map[string]any{
		"device_id": device.deviceID, "session_id": sessionID, "write": false, "ttl_ms": 3600000,
	}, ownerSession, ownerSession.csrf); call.status != http.StatusServiceUnavailable {
		t.Fatalf("offline create: HTTP %d %v", call.status, call.body)
	}
}

func TestShareRoutesAuthOff(t *testing.T) {
	f := newHTTPFixture(t, true)
	session := f.session(t, f.createUser(t, "off-user"))
	calls := []httpCall{
		f.call(t, "POST", "/share/links", map[string]any{"device_id": "d", "session_id": "s"}, session, session.csrf),
		f.call(t, "GET", "/share/links", nil, session, ""),
		f.call(t, "POST", "/share/links/x/revoke", nil, session, session.csrf),
		f.call(t, "POST", "/share/host-shares", map[string]any{"device_id": "d", "recipient_id": "r"}, session, session.csrf),
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
