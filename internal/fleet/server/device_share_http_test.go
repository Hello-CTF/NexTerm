//go:build unix

package fleetserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func createDeviceShareLink(t *testing.T, f *httpFixture, session *httpSession, deviceID string, write bool, notBeforeMS int64) (string, string) {
	t.Helper()
	call := f.call(t, "POST", "/share/device-links", map[string]any{
		"device_id": deviceID, "write": write, "not_before_ms": notBeforeMS, "ttl_ms": 3600000,
	}, session, session.csrf)
	if call.status != http.StatusOK {
		t.Fatalf("create device link: HTTP %d %v", call.status, call.body)
	}
	link, _ := call.body["id"].(string)
	token, _ := call.body["token"].(string)
	if link == "" || token == "" || call.body["permission"] == "" {
		t.Fatalf("create device link body = %v", call.body)
	}
	return link, token
}

func TestDeviceShareLinkManagement(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "devlink-owner")
	other := f.createUser(t, "devlink-other")
	device := startShareTestAgent(t, f, owner, "devlink-box")
	ownerSession := f.session(t, owner)

	// 匿名与 CSRF 边界。
	if call := f.call(t, "GET", "/share/device-links", nil, nil, ""); call.status != http.StatusUnauthorized {
		t.Fatalf("anonymous list: HTTP %d", call.status)
	}
	if call := f.call(t, "POST", "/share/device-links", map[string]any{"device_id": device.deviceID}, ownerSession, ""); call.status != http.StatusForbidden {
		t.Fatalf("missing csrf: HTTP %d", call.status)
	}

	linkID, token := createDeviceShareLink(t, f, ownerSession, device.deviceID, false, 0)
	if token == "" {
		t.Fatal("empty token")
	}

	// 生效时刻不早于过期时刻: 400。
	if call := f.call(t, "POST", "/share/device-links", map[string]any{
		"device_id": device.deviceID, "write": false, "not_before_ms": time.Now().UnixMilli() + 3600000, "ttl_ms": 60000,
	}, ownerSession, ownerSession.csrf); call.status != http.StatusBadRequest {
		t.Fatalf("not_before >= expiry: HTTP %d %v", call.status, call.body)
	}

	// 非 owner 吊销被拒; 非 owner 的列表为空。
	otherSession := f.session(t, other)
	if call := f.call(t, "POST", "/share/device-links/"+linkID+"/revoke", nil, otherSession, otherSession.csrf); call.status != http.StatusForbidden {
		t.Fatalf("other revoke: HTTP %d", call.status)
	}
	if call := f.call(t, "GET", "/share/device-links", nil, otherSession, ""); call.status != http.StatusOK || len(call.body["links"].([]any)) != 0 {
		t.Fatalf("other list: HTTP %d %v", call.status, call.body)
	}

	// owner 列表含该链接且绝不携带 token/hash; 设备链接没有 session_id。
	call := f.call(t, "GET", "/share/device-links", nil, ownerSession, "")
	if call.status != http.StatusOK {
		t.Fatalf("owner list: HTTP %d %v", call.status, call.body)
	}
	links := call.body["links"].([]any)
	if len(links) != 1 {
		t.Fatalf("owner links = %v", links)
	}
	row := links[0].(map[string]any)
	for _, forbidden := range []string{"token", "token_hash", "secret", "session_id"} {
		if _, exists := row[forbidden]; exists {
			t.Fatalf("device link view leaks %q: %v", forbidden, row)
		}
	}
	if row["permission"] != "read" || row["device_id"] != device.deviceID || row["not_before"] == nil {
		t.Fatalf("device link view = %v", row)
	}

	if call := f.call(t, "POST", "/share/device-links/"+linkID+"/revoke", nil, ownerSession, ownerSession.csrf); call.status != http.StatusOK {
		t.Fatalf("owner revoke: HTTP %d %v", call.status, call.body)
	}
	// 吊销后 token 立即不可用 (审计 reason=revoked); 把关走 WS upgrade 路径。
	if status := dialShareViewerStatus(t, f, "/share/public/device/"+token); status != http.StatusForbidden {
		t.Fatalf("revoked resolve: HTTP %d", status)
	}
	rows := auditPayloads(t, f, "device_share_link_access")
	last := rows[len(rows)-1]
	if last["outcome"] != "deny" || last["reason"] != "revoked" {
		t.Fatalf("revoke audit = %v", last)
	}
}

func TestDeviceShareLinkNotYetValidStopsHandshake(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "nb-owner")
	device := startShareTestAgent(t, f, owner, "nb-box")
	ownerSession := f.session(t, owner)

	_, token := createDeviceShareLink(t, f, ownerSession, device.deviceID, true, time.Now().UnixMilli()+15*time.Minute.Milliseconds())
	if status := dialShareViewerStatus(t, f, "/share/public/device/"+token); status != http.StatusForbidden {
		t.Fatalf("not-yet-valid handshake: HTTP %d", status)
	}
	rows := auditPayloads(t, f, "device_share_link_access")
	last := rows[len(rows)-1]
	if last["outcome"] != "deny" || last["reason"] != "not_yet_valid" {
		t.Fatalf("not_yet_valid audit = %v", last)
	}

	// 生效时刻到达后同一 token 可用 (直接推进行的 not_before)。
	if _, err := f.service.db.ExecContext(context.Background(), "UPDATE device_share_link SET not_before = ?", time.Now().UnixMilli()-1000); err != nil {
		t.Fatal(err)
	}
	conn := dialShareViewer(t, f, "/share/public/device/"+token, nil)
	collector := watchShareConn(conn)
	collector.expectReady(t, "read_write")
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

func TestSharePublicDeviceTerminalReadWrite(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "devterm-owner")
	device := startShareTestAgent(t, f, owner, "devterm-box")
	ownerSession := f.session(t, owner)
	_, token := createDeviceShareLink(t, f, ownerSession, device.deviceID, true, 0)

	conn := dialShareViewer(t, f, "/share/public/device/"+token, nil)
	collector := watchShareConn(conn)
	// ready 帧携带的是数据面为访客新建的终端会话 (不是任何既有会话)。
	ready := collector.expectReady(t, "read_write")
	if ready.SessionID == "" {
		t.Fatal("device terminal ready has no session id")
	}

	// 输入产生输出: 标记只有真正执行才会出现 (终端回显的是命令原文)。
	collector.sendBinary(t, []byte("echo dev-$((40+2))\n"))
	collector.expectOutput(t, "dev-42", 10*time.Second)

	// 输入动作已写审计 (allow), 且不携带 token。
	inputs := auditPayloads(t, f, "device_share_link_input")
	if len(inputs) == 0 {
		t.Fatal("no device_share_link_input audit rows")
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

func TestSharePublicDeviceTerminalReadOnlyAndShrink(t *testing.T) {
	f := newHTTPFixture(t, false, WithShareRevalidateInterval(100*time.Millisecond))
	owner := f.createUser(t, "devro-owner")
	device := startShareTestAgent(t, f, owner, "devro-box")
	ownerSession := f.session(t, owner)

	// 只读: 输入被静默丢弃 (终端回显命令原文, 但执行标记不出现), 连接保持。
	_, readToken := createDeviceShareLink(t, f, ownerSession, device.deviceID, false, 0)
	conn := dialShareViewer(t, f, "/share/public/device/"+readToken, nil)
	collector := watchShareConn(conn)
	collector.expectReady(t, "read")
	collector.sendBinary(t, []byte("echo nope-$((40+2))\n"))
	if output := collector.collectOutput(t, 1500*time.Millisecond); strings.Contains(string(output), "nope-42") {
		t.Fatalf("read-only input executed: %q", output)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")

	// 读写: 权限收缩 (read_write -> read, 模拟行被改) 后输入立即停止, 已有输出继续。
	_, writeToken := createDeviceShareLink(t, f, ownerSession, device.deviceID, true, 0)
	conn2 := dialShareViewer(t, f, "/share/public/device/"+writeToken, nil)
	collector2 := watchShareConn(conn2)
	collector2.expectReady(t, "read_write")
	collector2.sendBinary(t, []byte("while true; do echo tick; sleep 0.2; done\n"))
	collector2.expectOutput(t, "tick", 10*time.Second)

	if _, err := f.service.db.ExecContext(context.Background(), "UPDATE device_share_link SET permission = 'read' WHERE token_hash = ?", hashSecret(writeToken)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	collector2.sendBinary(t, []byte("echo nope-$((40+2))\n"))
	output := collector2.collectOutput(t, 1500*time.Millisecond)
	if !strings.Contains(string(output), "tick") {
		t.Fatalf("output did not continue after shrink: %q", output)
	}
	if strings.Contains(string(output), "nope-42") {
		t.Fatalf("input leaked after shrink: %q", output)
	}
	_ = conn2.Close(websocket.StatusNormalClosure, "")
}

func TestSharePublicDeviceTerminalRevokeStopsStream(t *testing.T) {
	f := newHTTPFixture(t, false, WithShareRevalidateInterval(100*time.Millisecond))
	owner := f.createUser(t, "devrev-owner")
	device := startShareTestAgent(t, f, owner, "devrev-box")
	ownerSession := f.session(t, owner)
	linkID, token := createDeviceShareLink(t, f, ownerSession, device.deviceID, true, 0)

	conn := dialShareViewer(t, f, "/share/public/device/"+token, nil)
	collector := watchShareConn(conn)
	collector.expectReady(t, "read_write")

	if call := f.call(t, "POST", "/share/device-links/"+linkID+"/revoke", nil, ownerSession, ownerSession.csrf); call.status != http.StatusOK {
		t.Fatalf("revoke: HTTP %d %v", call.status, call.body)
	}
	errorFrame := collector.expectErrorFrame(t, 10*time.Second)
	if errorFrame.Code != "forbidden" || !strings.Contains(errorFrame.Message, "吊销") {
		t.Fatalf("error frame = %+v", errorFrame)
	}
	collector.expectClosed(t, 5*time.Second)

	if status := dialShareViewerStatus(t, f, "/share/public/device/"+token); status != http.StatusForbidden {
		t.Fatalf("resolve after revoke: HTTP %d", status)
	}
}

func TestSharePublicDevicePageServesSPA(t *testing.T) {
	f := newHTTPFixture(t, false)
	const page = "<html><head><title>NexTerm</title></head><body>share-viewer</body></html>"
	f.service.SetSharePublicPage(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))

	// 设备链接的普通 GET (浏览器导航) 同样直接拿到 SPA, 不校验 token, 不写审计。
	response, err := f.client.Get(f.http.URL + "/share/public/device/some-token")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("plain GET = %d Cache-Control %q", response.StatusCode, response.Header.Get("Cache-Control"))
	}
	if rows := auditPayloads(t, f, "device_share_link_access"); len(rows) != 0 {
		t.Fatalf("plain GET wrote device_share_link_access audit rows: %v", rows)
	}

	// upgrade 路径的 token 把关不变: 无效 token 的 WS 握手仍 403。
	if status := dialShareViewerStatus(t, f, "/share/public/device/not-a-token"); status != http.StatusForbidden {
		t.Fatalf("invalid token WS handshake = %d, want 403", status)
	}
}

func TestShareDaemonOfflineStopsDeviceLink(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "devoff-owner")
	device := startShareTestAgent(t, f, owner, "devoff-box")
	ownerSession := f.session(t, owner)
	_, token := createDeviceShareLink(t, f, ownerSession, device.deviceID, true, 0)

	conn := dialShareViewer(t, f, "/share/public/device/"+token, nil)
	collector := watchShareConn(conn)
	collector.expectReady(t, "read_write")

	// agent 掉线: 已建立的分享流随桥接断开而停止。
	device.stop()
	collector.expectClosed(t, 10*time.Second)

	// 掉线后打开一律 503 (探针即时判离线, 不等 last_seen 过期)。
	if status := dialShareViewerStatus(t, f, "/share/public/device/"+token); status != http.StatusServiceUnavailable {
		t.Fatalf("offline resolve: HTTP %d", status)
	}
	// 掉线后创建一律 503。
	if call := f.call(t, "POST", "/share/device-links", map[string]any{
		"device_id": device.deviceID, "write": false, "ttl_ms": 3600000,
	}, ownerSession, ownerSession.csrf); call.status != http.StatusServiceUnavailable {
		t.Fatalf("offline create: HTTP %d %v", call.status, call.body)
	}
}

// 匿名 resolve 限流: 有效 token 成功重置窗口; 随机 token 首次 403 并记审计,
// 随后按客户端 IP 进入退避 (429), 被限流的尝试不再写 audit_log。
func TestSharePublicDeviceResolveRateLimited(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "ratelimit-owner")
	device := startShareTestAgent(t, f, owner, "ratelimit-box")
	ownerSession := f.session(t, owner)
	_, token := createDeviceShareLink(t, f, ownerSession, device.deviceID, false, 0)

	conn := dialShareViewer(t, f, "/share/public/device/"+token, nil)
	_ = conn.Close(websocket.StatusNormalClosure, "")

	if status := dialShareViewerStatus(t, f, "/share/public/device/not-a-token"); status != http.StatusForbidden {
		t.Fatalf("first invalid resolve = %d, want 403", status)
	}
	before := len(auditPayloads(t, f, "device_share_link_access"))
	for attempt := 0; attempt < 3; attempt++ {
		if status := dialShareViewerStatus(t, f, "/share/public/device/not-a-token"); status != http.StatusTooManyRequests {
			t.Fatalf("flooded resolve attempt %d = %d, want 429", attempt, status)
		}
	}
	if after := len(auditPayloads(t, f, "device_share_link_access")); after != before {
		t.Fatalf("throttled attempts still wrote audit rows: before=%d after=%d", before, after)
	}
	// 退避按客户端 IP 生效: 窗口内真实 token 同样 429 (限流不区分 token 真伪)。
	if status := dialShareViewerStatus(t, f, "/share/public/device/"+token); status != http.StatusTooManyRequests {
		t.Fatalf("valid token within backoff = %d, want 429", status)
	}
}
