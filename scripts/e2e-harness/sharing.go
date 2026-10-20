package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"
	_ "github.com/glebarez/go-sqlite"
)

var (
	linkViewKeys      = keySet("id", "owner_id", "device_id", "session_id", "permission", "created_at", "expires_at", "revoked_at", "last_accessed_at")
	hostShareViewKeys = keySet("id", "owner_id", "owner_username", "device_id", "recipient_id", "recipient_username", "permission", "created_at", "expires_at", "revoked_at")
	readyFrameKeys    = keySet("type", "session_id", "permission", "expires_at")
)

func keySet(keys ...string) map[string]bool {
	set := map[string]bool{}
	for _, key := range keys {
		set[key] = true
	}
	return set
}

func extraKeys(m map[string]interface{}, allowed map[string]bool) []string {
	var extra []string
	for key := range m {
		if !allowed[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	return extra
}

func keysSubset(m map[string]interface{}, allowed map[string]bool) bool {
	for key := range m {
		if !allowed[key] {
			return false
		}
	}
	return true
}

func sortedKeysOf(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type shareViewer struct {
	ws *wsConn
}

func newShareViewer(port int, path string, header http.Header) (*shareViewer, error) {
	ws, _, err := dialWS(port, path, header)
	if err != nil {
		return nil, err
	}
	return &shareViewer{ws: ws}, nil
}

func (v *shareViewer) expectReady() (map[string]interface{}, error) {
	message, err := v.ws.recv(15 * time.Second)
	if err != nil {
		return nil, err
	}
	if message.err != nil {
		return nil, message.err
	}
	if message.kind != websocket.MessageText {
		return nil, fmt.Errorf("ready frame: opcode %v payload=%q", message.kind, tailBytes(message.payload, 200))
	}
	frame := map[string]interface{}{}
	if err := json.Unmarshal(message.payload, &frame); err != nil {
		return nil, err
	}
	if getStr(frame, "type") != "ready" {
		return nil, fmt.Errorf("ready frame: %v", frame)
	}
	return frame, nil
}

func (v *shareViewer) readOutput(timeout time.Duration) ([]byte, error) {
	message, err := v.ws.recv(timeout)
	if err != nil {
		return nil, err
	}
	if message.err != nil {
		return nil, fmt.Errorf("viewer websocket closed: %v", message.err)
	}
	if message.kind == websocket.MessageText {
		frame := map[string]interface{}{}
		json.Unmarshal(message.payload, &frame)
		if getStr(frame, "type") == "error" {
			return nil, fmt.Errorf("viewer error frame: %v", frame)
		}
		return nil, nil
	}
	return message.payload, nil
}

func (v *shareViewer) expectOutput(needle []byte, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var seen []byte
	for time.Now().Before(deadline) {
		chunk, err := v.readOutput(time.Until(deadline))
		if errors.Is(err, errReadTimeout) {
			break
		}
		if err != nil {
			return err
		}
		seen = append(seen, chunk...)
		if bytes.Contains(seen, needle) {
			return nil
		}
	}
	return fmt.Errorf("output never contained %q: %q", needle, tailBytes(seen, 300))
}

func (v *shareViewer) expectSilent(duration time.Duration) error {
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		chunk, err := v.readOutput(time.Until(deadline))
		if errors.Is(err, errReadTimeout) {
			return nil
		}
		if err != nil {
			return err
		}
		if len(chunk) > 0 {
			return fmt.Errorf("expected silence, got %q", chunk)
		}
	}
	return nil
}

func (v *shareViewer) expectError(timeout time.Duration) (map[string]interface{}, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		message, err := v.ws.recv(time.Until(deadline))
		if errors.Is(err, errReadTimeout) {
			break
		}
		if err != nil {
			return nil, err
		}
		if message.err != nil {
			break
		}
		if message.kind == websocket.MessageText {
			frame := map[string]interface{}{}
			json.Unmarshal(message.payload, &frame)
			if getStr(frame, "type") == "error" {
				return frame, nil
			}
		}
	}
	return nil, fmt.Errorf("no error frame arrived")
}

func (v *shareViewer) expectClosed(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		message, err := v.ws.recv(time.Until(deadline))
		if errors.Is(err, errReadTimeout) {
			continue
		}
		if err != nil {
			return err
		}
		if message.err != nil {
			return nil
		}
	}
	return fmt.Errorf("viewer websocket was not closed in time")
}

func seedHistoricalShareLink(server *instance, ownerID, deviceID, sessionID string, write bool, ttl time.Duration) (linkID, token string, err error) {
	if server == nil || ownerID == "" || deviceID == "" || sessionID == "" || ttl <= 0 {
		return "", "", fmt.Errorf("invalid historical share link seed")
	}
	token = randomHex(32)
	if token == "" {
		return "", "", fmt.Errorf("historical share link seed returned an empty token")
	}
	digest := sha256.Sum256([]byte(token))
	permission := "read"
	if write {
		permission = "read_write"
	}
	now := time.Now()
	linkID = syntheticID()
	db, err := sql.Open("sqlite", filepath.Join(server.dataDir, "data.db"))
	if err != nil {
		return "", "", fmt.Errorf("open database for historical share link seed: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	result, err := db.Exec(`INSERT INTO share_link
		(id, owner_id, device_id, session_id, token_hash, permission, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		linkID, ownerID, deviceID, sessionID, fmt.Sprintf("%x", digest), permission, now.UnixMilli(), now.Add(ttl).UnixMilli())
	if err != nil {
		return "", "", fmt.Errorf("seed historical share link: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return "", "", fmt.Errorf("seed historical share link affected %d rows, err=%v", affected, err)
	}
	return linkID, token, nil
}

func runSharing(h *harness) error {
	work, err := os.MkdirTemp("", "nexterm-sharing-e2e-")
	if err != nil {
		return err
	}
	masterKey := "e2e-master-" + randomHex(16)
	webRoot := filepath.Join(work, "web")
	if err := os.MkdirAll(webRoot, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<html><head><title>NexTerm</title></head><body>share-e2e</body></html>"), 0o644); err != nil {
		return err
	}
	server, err := newInstance(h, work, "server", masterKey, "on", webRoot)
	if err != nil {
		return err
	}
	agentDir := filepath.Join(work, "agent")
	bobAgentDir := filepath.Join(work, "agent-bob")
	var agent, bobAgent *agentProcess
	defer func() {
		agent.stop()
		bobAgent.stop()
		cleanupHelper(agentDir)
		cleanupHelper(bobAgentDir)
		server.stop()
	}()
	if err := server.start(); err != nil {
		return err
	}
	fmt.Println("== superadmin initialization ==")
	initCode, err := server.initCode()
	if err != nil {
		return err
	}
	alicePassword := "alice-e2e-pw-123"
	bobPassword := "bob-e2e-pw-123"
	carolPassword := "carol-e2e-pw-123"
	status, body, err := initSuperadmin(h, server.port, initCode, "alice", alicePassword)
	if err != nil {
		return err
	}
	h.check("superadmin initialization", status == 200 && getStr(getMap(body, "user"), "username") == "alice", fmt.Sprintf("HTTP %d %v", status, body))
	alice := newClient(server.port)
	if err := alice.login("alice", alicePassword); err != nil {
		return err
	}

	fmt.Println("== user and device setup ==")
	status, body, err = alice.request("POST", "/admin/users", map[string]interface{}{"username": "bob", "password": bobPassword}, true)
	if err != nil {
		return err
	}
	h.check("superadmin creates bob", status == 200 && getStr(getMap(body, "user"), "username") == "bob", fmt.Sprintf("HTTP %d %v", status, body))
	bobID := getStr(getMap(body, "user"), "id")
	status, body, err = alice.request("POST", "/admin/users", map[string]interface{}{"username": "carol", "password": carolPassword}, true)
	if err != nil {
		return err
	}
	h.check("superadmin creates carol", status == 200 && getStr(getMap(body, "user"), "username") == "carol", fmt.Sprintf("HTTP %d %v", status, body))
	bob := newClient(server.port)
	if err := bob.login("bob", bobPassword); err != nil {
		return err
	}
	carol := newClient(server.port)
	if err := carol.login("carol", carolPassword); err != nil {
		return err
	}

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", server.port)
	status, body, err = alice.request("PUT", "/fleet/base-urls", map[string]interface{}{"base_urls": []map[string]string{{"url": baseURL}}}, true)
	if err != nil {
		return err
	}
	h.check("configure server base URLs", status == 200 && len(getList(body, "base_urls")) == 1, fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = alice.request("POST", "/device/enroll-codes", map[string]interface{}{"ttl_ms": 600000}, true)
	if err != nil {
		return err
	}
	h.check("issue enrollment code", status == 200 && getStr(body, "code") != "", fmt.Sprintf("HTTP %d %v", status, body))
	enrollCode := getStr(body, "code")

	stdout, rc, stderr := enrollAgent(h, baseURL, enrollCode, agentDir, "sharing-box")
	h.check("agent enroll subcommand", rc == 0 && strings.Contains(stdout, "注册成功"), fmt.Sprintf("rc=%d %s %s", rc, stdout, stderr))
	configPath := filepath.Join(agentDir, "fleet", "agent.json")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	agentConfig := map[string]interface{}{}
	if err := json.Unmarshal(raw, &agentConfig); err != nil {
		return err
	}
	deviceID := getStr(agentConfig, "device_id")

	status, body, err = alice.request("POST", fmt.Sprintf("/fleet/devices/%s/autostart", deviceID), map[string]interface{}{"desired": false}, true)
	if err != nil {
		return err
	}
	desired, desiredOK := getBool(body, "desired_autostart")
	h.check("disable desired autostart", status == 200 && desiredOK && !desired, fmt.Sprintf("HTTP %d %v", status, body))
	agentConfig["desired_autostart"] = false
	agentConfig["metrics_interval_ms"] = 5000
	encoded, err := json.Marshal(agentConfig)
	if err != nil {
		return err
	}
	if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
		return err
	}

	agent, err = startAgent(h, agentDir, filepath.Join(work, "agent.log"))
	if err != nil {
		return err
	}
	deviceRow := func() map[string]interface{} {
		_, body, err := alice.request("GET", "/fleet/devices", nil, false)
		if err != nil {
			return nil
		}
		for _, row := range getList(body, "devices") {
			if entry, ok := row.(map[string]interface{}); ok && getStr(entry, "id") == deviceID {
				return entry
			}
		}
		return nil
	}
	if err := waitUntil(20*time.Second, "agent control channel online", func() bool {
		return getStr(getMap(deviceRow(), "agent"), "current_url") != ""
	}); err != nil {
		return err
	}
	h.check("agent control channel comes online", getStr(getMap(deviceRow(), "agent"), "current_url") == baseURL, fmt.Sprintf("row=%v", deviceRow()))

	fmt.Println("== owner creates a session through the bridge ==")
	stateDigest, err := localStateDigest(filepath.Join(agentDir, "durable", "supervisor"))
	if err != nil {
		return err
	}
	relayHeader := http.Header{}
	relayHeader.Set("Cookie", sessionCookie+"="+alice.cookie)
	relay, _, err := dialWS(server.port, fmt.Sprintf("/fleet/devices/%s/bridge", deviceID), relayHeader)
	if err != nil {
		return err
	}
	stream := &supervisorStream{ws: relay}
	if err := stream.hello(stateDigest); err != nil {
		return err
	}
	info, err := stream.create([]string{"sh", "-c", "echo READY-42; exec cat"}, 80, 24)
	if err != nil {
		return err
	}
	sessionID := getStr(info, "id")
	if err := stream.detach(); err != nil {
		return err
	}
	relay.close()
	h.check("owner creates a session through the bridge", sessionID != "", fmt.Sprintf("info=%v", info))

	fmt.Println("== retired public link creation ==")
	status, body, err = alice.request("POST", "/share/links", map[string]interface{}{
		"device_id": deviceID, "session_id": sessionID, "write": false, "ttl_ms": 3600000,
	}, true)
	if err != nil {
		return err
	}
	h.check("public session link creation returns 405", status == http.StatusMethodNotAllowed, fmt.Sprintf("HTTP %d %v", status, body))

	fmt.Println("== public read-only link ==")
	readLinkID, readToken, err := seedHistoricalShareLink(server, alice.userID, deviceID, sessionID, false, time.Hour)
	if err != nil {
		return fmt.Errorf("seed read-only link: %w", err)
	}

	fmt.Println("== public page plain GET ==")
	status, _, indexBody, err := plainGet(server.port, "/")
	if err != nil {
		return err
	}
	h.check("root path serves the SPA", status == 200 && strings.Contains(indexBody, "<title>NexTerm</title>") &&
		strings.Contains(indexBody, `window.__NEXTERM_TRANSPORT__="web"`), fmt.Sprintf("HTTP %d %s", status, truncate(indexBody, 120)))
	status, cacheControl, tokenBody, err := plainGet(server.port, "/share/public/"+readToken)
	if err != nil {
		return err
	}
	h.check("valid token plain GET serves the same SPA", status == 200 && tokenBody == indexBody, fmt.Sprintf("HTTP %d", status))
	h.check("token URL responds with no-store", cacheControl == "no-store", fmt.Sprintf("Cache-Control=%q", cacheControl))
	status, _, invalidBody, err := plainGet(server.port, "/share/public/not-a-real-token")
	if err != nil {
		return err
	}
	h.check("invalid token plain GET also serves the SPA (token is not validated)", status == 200 && invalidBody == indexBody, fmt.Sprintf("HTTP %d", status))
	postCode, err := postStatus(server.port, "/share/public/"+readToken)
	if err != nil {
		return err
	}
	h.check("POST to public token URL does not fall through to SPA (405)", postCode == 405, fmt.Sprintf("HTTP %d", postCode))

	viewer, err := newShareViewer(server.port, "/share/public/"+readToken, nil)
	if err != nil {
		return err
	}
	ready, err := viewer.expectReady()
	if err != nil {
		return err
	}
	h.check("read-only ready frame", getStr(ready, "permission") == "read" && getStr(ready, "session_id") == sessionID &&
		keysSubset(ready, readyFrameKeys), fmt.Sprintf("ready=%v", ready))
	if err := viewer.expectOutput([]byte("READY-42"), 15*time.Second); err != nil {
		return err
	}
	h.check("read-only viewer receives session output", true, "")
	if err := viewer.ws.send(websocket.MessageBinary, []byte("echo nope-42\n")); err != nil {
		return err
	}
	if err := viewer.expectSilent(2 * time.Second); err != nil {
		h.check("read-only input is silently discarded", false, err.Error())
	} else {
		h.check("read-only input is silently discarded", true, "")
	}
	viewer.ws.close()

	fmt.Println("== public read-write link ==")
	_, writeToken, err := seedHistoricalShareLink(server, alice.userID, deviceID, sessionID, true, time.Hour)
	if err != nil {
		return fmt.Errorf("seed read-write link: %w", err)
	}
	rwViewer, err := newShareViewer(server.port, "/share/public/"+writeToken, nil)
	if err != nil {
		return err
	}
	ready, err = rwViewer.expectReady()
	if err != nil {
		return err
	}
	h.check("read-write ready frame", getStr(ready, "permission") == "read_write", fmt.Sprintf("ready=%v", ready))
	if err := rwViewer.expectOutput([]byte("READY-42"), 15*time.Second); err != nil {
		return err
	}
	if err := rwViewer.ws.send(websocket.MessageBinary, []byte("echo hello-42\n")); err != nil {
		return err
	}
	if err := rwViewer.expectOutput([]byte("hello-42"), 15*time.Second); err != nil {
		h.check("read-write input reaches the session and echoes", false, err.Error())
	} else {
		h.check("read-write input reaches the session and echoes", true, "")
	}

	fmt.Println("== revocation ==")
	status, body, err = alice.request("POST", fmt.Sprintf("/share/links/%s/revoke", readLinkID), map[string]interface{}{}, true)
	if err != nil {
		return err
	}
	okFlag, _ := getBool(body, "ok")
	h.check("revoke read-only link", status == 200 && okFlag, fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = alice.request("GET", "/share/links", nil, false)
	if err != nil {
		return err
	}
	writeLinkID := ""
	for _, row := range getList(body, "links") {
		if entry, ok := row.(map[string]interface{}); ok && getStr(entry, "permission") == "read_write" {
			writeLinkID = getStr(entry, "id")
		}
	}
	status, body, err = alice.request("POST", fmt.Sprintf("/share/links/%s/revoke", writeLinkID), map[string]interface{}{}, true)
	if err != nil {
		return err
	}
	okFlag, _ = getBool(body, "ok")
	h.check("revoke read-write link", status == 200 && okFlag, fmt.Sprintf("HTTP %d %v", status, body))
	errorFrame, err := rwViewer.expectError(30 * time.Second)
	if err != nil {
		h.check("revocation stops active viewers (error frame)", false, err.Error())
	} else {
		h.check("revocation stops active viewers (error frame)", strings.Contains(getStr(errorFrame, "message"), "吊销"), fmt.Sprintf("frame=%v", errorFrame))
	}
	if err := rwViewer.expectClosed(10 * time.Second); err != nil {
		return err
	}
	revokedStatus := wsHandshakeStatus(server.port, "/share/public/"+writeToken)
	h.check("token cannot be reused after revocation", revokedStatus == 403, fmt.Sprintf("status=%d", revokedStatus))

	fmt.Println("== expiration ==")
	_, expiringToken, err := seedHistoricalShareLink(server, alice.userID, deviceID, sessionID, false, time.Minute)
	if err != nil {
		return fmt.Errorf("seed short-lived link: %w", err)
	}
	expiringViewer, err := newShareViewer(server.port, "/share/public/"+expiringToken, nil)
	if err != nil {
		return err
	}
	if _, err := expiringViewer.expectReady(); err != nil {
		return err
	}
	errorFrame, err = expiringViewer.expectError(100 * time.Second)
	if err != nil {
		h.check("expiration stops active viewers", false, err.Error())
	} else {
		h.check("expiration stops active viewers", strings.Contains(getStr(errorFrame, "message"), "过期"), fmt.Sprintf("frame=%v", errorFrame))
	}
	if err := expiringViewer.expectClosed(10 * time.Second); err != nil {
		return err
	}
	expiredStatus := wsHandshakeStatus(server.port, "/share/public/"+expiringToken)
	h.check("token cannot be reused after expiration", expiredStatus == 403, fmt.Sprintf("status=%d", expiredStatus))

	fmt.Println("== open a terminal through a registered share ==")
	status, body, err = carol.request("GET", fmt.Sprintf("/share/devices/%s/terminal", deviceID), nil, false)
	if err != nil {
		return err
	}
	h.check("user without a share is rejected when opening", status == 403, fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = alice.request("POST", "/share/host-shares", map[string]interface{}{
		"device_id": deviceID, "recipient_username": "bob", "write": true, "ttl_ms": 3600000,
	}, true)
	if err != nil {
		return err
	}
	h.check("grant bob a host share", status == 200 && getStr(body, "permission") == "read_write", fmt.Sprintf("HTTP %d %v", status, body))
	shareID := getStr(body, "id")
	if extra := extraKeys(body, hostShareViewKeys); len(extra) > 0 {
		h.check("host share view has no extra fields", false, fmt.Sprintf("keys=%v", sortedKeysOf(body)))
	} else {
		h.check("host share view has no extra fields", true, "")
	}

	bobHeader := http.Header{}
	bobHeader.Set("Cookie", sessionCookie+"="+bob.cookie)
	bobViewer, err := newShareViewer(server.port, fmt.Sprintf("/share/devices/%s/terminal", deviceID), bobHeader)
	if err != nil {
		return err
	}
	ready, err = bobViewer.expectReady()
	if err != nil {
		return err
	}
	h.check("bob opens a new terminal", getStr(ready, "permission") == "read_write" && getStr(ready, "session_id") != "", fmt.Sprintf("ready=%v", ready))
	if err := bobViewer.ws.send(websocket.MessageBinary, []byte("echo hi-42\n")); err != nil {
		return err
	}
	if err := bobViewer.expectOutput([]byte("hi-42"), 15*time.Second); err != nil {
		h.check("bob terminal input and output", false, err.Error())
	} else {
		h.check("bob terminal input and output", true, "")
	}

	fmt.Println("== permission reduction read_write -> read ==")
	if err := bobViewer.ws.send(websocket.MessageBinary, []byte("while true; do echo tick; sleep 0.5; done\n")); err != nil {
		return err
	}
	if err := bobViewer.expectOutput([]byte("tick"), 15*time.Second); err != nil {
		h.check("terminal keeps producing output before reduction", false, err.Error())
	}
	status, body, err = alice.request("POST", "/share/host-shares", map[string]interface{}{
		"device_id": deviceID, "recipient_username": "bob", "write": false, "ttl_ms": 3600000,
	}, true)
	if err != nil {
		return err
	}
	h.check("reduce to a read-only share", status == 200 && getStr(body, "permission") == "read", fmt.Sprintf("HTTP %d %v", status, body))
	time.Sleep(3 * time.Second)
	if err := bobViewer.ws.send(websocket.MessageBinary, []byte("echo nope-42\n")); err != nil {
		return err
	}
	var shrinkOutput []byte
	shrinkDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(shrinkDeadline) {
		chunk, err := bobViewer.readOutput(time.Until(shrinkDeadline))
		if errors.Is(err, errReadTimeout) {
			break
		}
		if err != nil {
			return err
		}
		shrinkOutput = append(shrinkOutput, chunk...)
	}
	h.check("output continues after reduction", bytes.Contains(shrinkOutput, []byte("tick")), fmt.Sprintf("output=%q", tailBytes(shrinkOutput, 200)))
	h.check("input is discarded after reduction", !bytes.Contains(shrinkOutput, []byte("nope-42")), fmt.Sprintf("output=%q", tailBytes(shrinkOutput, 200)))
	bobViewer.ws.close()

	status, body, err = alice.request("POST", fmt.Sprintf("/share/host-shares/%s/revoke", shareID), map[string]interface{}{}, true)
	if err != nil {
		return err
	}
	okFlag, _ = getBool(body, "ok")
	h.check("revoke host share", status == 200 && okFlag, fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = bob.request("GET", fmt.Sprintf("/share/devices/%s/terminal", deviceID), nil, false)
	if err != nil {
		return err
	}
	h.check("bob is rejected when opening after revocation", status == 403, fmt.Sprintf("HTTP %d %v", status, body))

	fmt.Println("== regular owner creates a host share by username ==")
	status, body, err = alice.request("POST", "/device/enroll-codes", map[string]interface{}{"ttl_ms": 600000, "user_id": bobID}, true)
	if err != nil {
		return err
	}
	h.check("superadmin issues an enrollment code for bob", status == 200 && getStr(body, "code") != "", fmt.Sprintf("HTTP %d %v", status, body))
	stdout, rc, stderr = enrollAgent(h, baseURL, getStr(body, "code"), bobAgentDir, "bob-box")
	h.check("bob agent enroll subcommand", rc == 0 && strings.Contains(stdout, "注册成功"), fmt.Sprintf("rc=%d %s %s", rc, stdout, stderr))
	bobConfigPath := filepath.Join(bobAgentDir, "fleet", "agent.json")
	raw, err = os.ReadFile(bobConfigPath)
	if err != nil {
		return err
	}
	bobConfig := map[string]interface{}{}
	if err := json.Unmarshal(raw, &bobConfig); err != nil {
		return err
	}
	bobDeviceID := getStr(bobConfig, "device_id")
	status, body, err = bob.request("POST", fmt.Sprintf("/fleet/devices/%s/autostart", bobDeviceID), map[string]interface{}{"desired": false}, true)
	if err != nil {
		return err
	}
	desired, desiredOK = getBool(body, "desired_autostart")
	h.check("bob disables desired autostart", status == 200 && desiredOK && !desired, fmt.Sprintf("HTTP %d %v", status, body))
	bobConfig["desired_autostart"] = false
	encoded, err = json.Marshal(bobConfig)
	if err != nil {
		return err
	}
	if err := os.WriteFile(bobConfigPath, encoded, 0o600); err != nil {
		return err
	}
	bobAgent, err = startAgent(h, bobAgentDir, filepath.Join(work, "agent-bob.log"))
	if err != nil {
		return err
	}
	bobDeviceRow := func() map[string]interface{} {
		_, body, err := bob.request("GET", "/fleet/devices", nil, false)
		if err != nil {
			return nil
		}
		for _, row := range getList(body, "devices") {
			if entry, ok := row.(map[string]interface{}); ok && getStr(entry, "id") == bobDeviceID {
				return entry
			}
		}
		return nil
	}
	if err := waitUntil(20*time.Second, "bob agent control channel online", func() bool {
		return getStr(getMap(bobDeviceRow(), "agent"), "current_url") != ""
	}); err != nil {
		return err
	}
	h.check("bob agent control channel comes online", getStr(getMap(bobDeviceRow(), "agent"), "current_url") == baseURL, fmt.Sprintf("row=%v", bobDeviceRow()))
	status, body, err = bob.request("POST", "/share/host-shares", map[string]interface{}{
		"device_id": bobDeviceID, "recipient_username": "no-such-user", "write": false, "ttl_ms": 3600000,
	}, true)
	if err != nil {
		return err
	}
	h.check("regular owner gets 404 for an invalid username", status == 404 && getStr(getMap(body, "error"), "code") == "not_found", fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = bob.request("POST", "/share/host-shares", map[string]interface{}{
		"device_id": bobDeviceID, "recipient_username": "carol", "write": false, "ttl_ms": 3600000,
	}, true)
	if err != nil {
		return err
	}
	h.check("regular owner creates a share by username", status == 200 && getStr(body, "permission") == "read" &&
		getStr(body, "recipient_username") == "carol" && getStr(body, "owner_username") == "bob", fmt.Sprintf("HTTP %d %v", status, body))
	carolHeader := http.Header{}
	carolHeader.Set("Cookie", sessionCookie+"="+carol.cookie)
	carolViewer, err := newShareViewer(server.port, fmt.Sprintf("/share/devices/%s/terminal", bobDeviceID), carolHeader)
	if err != nil {
		return err
	}
	ready, err = carolViewer.expectReady()
	if err != nil {
		return err
	}
	h.check("carol opens a new terminal through bob's share", getStr(ready, "permission") == "read" && getStr(ready, "session_id") != "", fmt.Sprintf("ready=%v", ready))
	carolViewer.ws.close()
	bobAgent.stop()
	cleanupHelper(bobAgentDir)

	fmt.Println("== agent daemon offline ==")
	status, body, err = alice.request("POST", "/share/host-shares", map[string]interface{}{
		"device_id": deviceID, "recipient_username": "bob", "write": true, "ttl_ms": 3600000,
	}, true)
	if err != nil {
		return err
	}
	h.check("re-grant bob", status == 200, fmt.Sprintf("HTTP %d %v", status, body))
	_, offlineToken, err := seedHistoricalShareLink(server, alice.userID, deviceID, sessionID, true, time.Hour)
	if err != nil {
		return fmt.Errorf("seed link before going offline: %w", err)
	}
	offlineViewer, err := newShareViewer(server.port, "/share/public/"+offlineToken, nil)
	if err != nil {
		return err
	}
	if _, err := offlineViewer.expectReady(); err != nil {
		return err
	}
	agent.stop()
	if err := offlineViewer.expectClosed(15 * time.Second); err != nil {
		h.check("agent offline stops active share streams", false, err.Error())
	} else {
		h.check("agent offline stops active share streams", true, "")
	}
	offlineStatus := wsHandshakeStatus(server.port, "/share/public/"+offlineToken)
	h.check("public link returns 503 after going offline", offlineStatus == 503, fmt.Sprintf("status=%d", offlineStatus))
	status, body, err = bob.request("GET", fmt.Sprintf("/share/devices/%s/terminal", deviceID), nil, false)
	if err != nil {
		return err
	}
	h.check("registered open returns 503 after going offline", status == 503, fmt.Sprintf("HTTP %d %v", status, body))
	fmt.Println("== invalid token WS gate ==")
	invalidStatus := wsHandshakeStatus(server.port, "/share/public/not-a-real-token")
	h.check("invalid token WS handshake is still rejected", invalidStatus == 403, fmt.Sprintf("status=%d", invalidStatus))

	fmt.Println("== disclosure defenses ==")
	status, body, err = newClient(server.port).request("GET", "/share/links", nil, false)
	if err != nil {
		return err
	}
	h.check("anonymous access to share management returns 401", status == 401, fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = newClient(server.port).request("POST", "/sync/v2/pull", map[string]interface{}{"since": 0}, false)
	if err != nil {
		return err
	}
	h.check("anonymous access to sync protocol is rejected", status == 401 || status == 403, fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = alice.request("GET", "/share/links", nil, false)
	if err != nil {
		return err
	}
	var leaked [][]string
	for _, row := range getList(body, "links") {
		if entry, ok := row.(map[string]interface{}); ok {
			leaked = append(leaked, extraKeys(entry, linkViewKeys))
		}
	}
	leakFree := true
	for _, extra := range leaked {
		if len(extra) > 0 {
			leakFree = false
		}
	}
	h.check("link list has no token field", status == 200 && leakFree, fmt.Sprintf("extra=%v", leaked))
	return nil
}
