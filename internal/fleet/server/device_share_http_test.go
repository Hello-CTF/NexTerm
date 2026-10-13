//go:build unix

package fleetserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/sharing"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
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

	// Anonymous and CSRF boundaries.
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

	// Effect time at/after expiry: 400.
	if call := f.call(t, "POST", "/share/device-links", map[string]any{
		"device_id": device.deviceID, "write": false, "not_before_ms": time.Now().UnixMilli() + 3600000, "ttl_ms": 60000,
	}, ownerSession, ownerSession.csrf); call.status != http.StatusBadRequest {
		t.Fatalf("not_before >= expiry: HTTP %d %v", call.status, call.body)
	}

	// Non-owner revoke is rejected; the non-owner's list is empty.
	otherSession := f.session(t, other)
	if call := f.call(t, "POST", "/share/device-links/"+linkID+"/revoke", nil, otherSession, otherSession.csrf); call.status != http.StatusForbidden {
		t.Fatalf("other revoke: HTTP %d", call.status)
	}
	if call := f.call(t, "GET", "/share/device-links", nil, otherSession, ""); call.status != http.StatusOK || len(call.body["links"].([]any)) != 0 {
		t.Fatalf("other list: HTTP %d %v", call.status, call.body)
	}

	// The owner list contains the link and never carries token/hash; device links have no session_id.
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
	// After revocation the token is instantly unusable (audit reason=revoked); the gate goes through the WS upgrade path.
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

	// Once the effect time arrives, the same token works (push the row's not_before forward directly).
	deviceShareLink := query.Use(f.service.db).DeviceShareLink
	if _, err := deviceShareLink.WithContext(context.Background()).
		Where(deviceShareLink.TokenHash.Eq(hashSecret(token))).
		UpdateColumns(map[string]any{"not_before": time.Now().UnixMilli() - 1000}); err != nil {
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
	// The ready frame carries the terminal session the data plane created for the visitor (not any existing session).
	ready := collector.expectReady(t, "read_write")
	if ready.SessionID == "" {
		t.Fatal("device terminal ready has no session id")
	}

	// Input produces output: the marker only appears if the command actually executed (the terminal echoes the command text).
	collector.sendBinary(t, []byte("echo dev-$((40+2))\n"))
	collector.expectOutput(t, "dev-42", 10*time.Second)

	// The input action was audited (allow) and carries no token.
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
	device.waitSessionMissing(t, ready.SessionID)
}

func TestShareTerminalReadyFailureReclaimsCreatedSession(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "readyfail-owner")
	device := startShareTestAgent(t, f, owner, "readyfail-box")
	client, server := wsPair(t)
	_ = client.CloseNow()
	_ = server.CloseNow()
	grant := &sharing.Grant{
		DeviceID: device.deviceID, Permission: sharing.PermissionReadWrite,
		ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
	}
	viewer := &shareTerminalWS{conn: server}
	request := httptest.NewRequest(http.MethodGet, "/share/devices/"+device.deviceID+"/terminal", nil)
	sessionID := f.service.serveShareTerminalWithViewer(request, viewer, grant, sharing.NewGate(grant))
	if sessionID == "" {
		t.Fatal("ready failure did not create a guest session")
	}
	device.waitSessionMissing(t, sessionID)
}

func TestSharePublicDeviceTerminalReadOnlyAndShrink(t *testing.T) {
	f := newHTTPFixture(t, false, WithShareRevalidateInterval(100*time.Millisecond))
	owner := f.createUser(t, "devro-owner")
	device := startShareTestAgent(t, f, owner, "devro-box")
	ownerSession := f.session(t, owner)

	// Read-only: input is silently discarded (the terminal echoes the command text, but the execution marker never appears), and the connection stays up.
	_, readToken := createDeviceShareLink(t, f, ownerSession, device.deviceID, false, 0)
	conn := dialShareViewer(t, f, "/share/public/device/"+readToken, nil)
	collector := watchShareConn(conn)
	collector.expectReady(t, "read")
	collector.sendBinary(t, []byte("echo nope-$((40+2))\n"))
	if output := collector.collectOutput(t, 1500*time.Millisecond); strings.Contains(string(output), "nope-42") {
		t.Fatalf("read-only input executed: %q", output)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")

	// Read-write: after permission shrinkage (read_write -> read, simulating the row being changed), input stops immediately and existing output continues.
	_, writeToken := createDeviceShareLink(t, f, ownerSession, device.deviceID, true, 0)
	conn2 := dialShareViewer(t, f, "/share/public/device/"+writeToken, nil)
	collector2 := watchShareConn(conn2)
	collector2.expectReady(t, "read_write")
	collector2.sendBinary(t, []byte("while true; do echo tick; sleep 0.2; done\n"))
	collector2.expectOutput(t, "tick", 10*time.Second)

	deviceShareLink := query.Use(f.service.db).DeviceShareLink
	if _, err := deviceShareLink.WithContext(context.Background()).
		Where(deviceShareLink.TokenHash.Eq(hashSecret(writeToken))).
		UpdateColumns(map[string]any{"permission": "read"}); err != nil {
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
	ready := collector.expectReady(t, "read_write")

	if call := f.call(t, "POST", "/share/device-links/"+linkID+"/revoke", nil, ownerSession, ownerSession.csrf); call.status != http.StatusOK {
		t.Fatalf("revoke: HTTP %d %v", call.status, call.body)
	}
	errorFrame := collector.expectErrorFrame(t, 10*time.Second)
	if errorFrame.Code != "forbidden" || !strings.Contains(errorFrame.Message, "吊销") {
		t.Fatalf("error frame = %+v", errorFrame)
	}
	collector.expectClosed(t, 5*time.Second)
	device.waitSessionMissing(t, ready.SessionID)

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

	// Plain GET of a device link (browser navigation) likewise gets the SPA directly: no token verification, no audit.
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

	// Token gating on the upgrade path is unchanged: a WS handshake with an invalid token still gets 403.
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

	// Agent offline: the established share stream stops with the bridge disconnecting.
	device.stop()
	collector.expectClosed(t, 10*time.Second)

	// Opens after offline always get 503 (the probe judges offline instantly, without waiting for last_seen to expire).
	if status := dialShareViewerStatus(t, f, "/share/public/device/"+token); status != http.StatusServiceUnavailable {
		t.Fatalf("offline resolve: HTTP %d", status)
	}
	// Creates after offline always get 503.
	if call := f.call(t, "POST", "/share/device-links", map[string]any{
		"device_id": device.deviceID, "write": false, "ttl_ms": 3600000,
	}, ownerSession, ownerSession.csrf); call.status != http.StatusServiceUnavailable {
		t.Fatalf("offline create: HTTP %d %v", call.status, call.body)
	}
}

// Anonymous resolve rate limiting: a valid token success resets the window; a random token gets 403 with an audit on first try,
// then enters backoff by client IP (429), and throttled attempts no longer write audit_log.
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
	// Backoff applies by client IP: a real token within the window also gets 429 (rate limiting does not distinguish token validity).
	if status := dialShareViewerStatus(t, f, "/share/public/device/"+token); status != http.StatusTooManyRequests {
		t.Fatalf("valid token within backoff = %d, want 429", status)
	}
}
