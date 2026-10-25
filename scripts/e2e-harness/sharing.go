package main

import (
	"bytes"
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
	stateDigest, err := localStateDigest(filepath.Join(agentDir, "durable", "supervisor"))
	if err != nil {
		return err
	}
	if err := waitUntil(20*time.Second, "agent control channel online", func() bool {
		agent := getMap(deviceRow(), "agent")
		return getStr(agent, "current_url") == baseURL && getStr(agent, "state_digest") == stateDigest
	}); err != nil {
		return err
	}
	h.check("agent control channel comes online", getStr(getMap(deviceRow(), "agent"), "current_url") == baseURL &&
		getStr(getMap(deviceRow(), "agent"), "state_digest") == stateDigest, fmt.Sprintf("row=%v", deviceRow()))

	fmt.Println("== owner creates a session through the bridge ==")
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

	fmt.Println("== removed legacy public links ==")
	status, body, err = alice.request("POST", "/share/links", map[string]interface{}{
		"device_id": deviceID, "session_id": sessionID, "write": false, "ttl_ms": 3600000,
	}, true)
	if err != nil {
		return err
	}
	h.check("legacy link creation is not routed", status == http.StatusMethodNotAllowed || status == http.StatusNotFound, fmt.Sprintf("HTTP %d %v", status, body))
	status, _, indexBody, err := plainGet(server.port, "/")
	if err != nil {
		return err
	}
	h.check("root path serves the SPA", status == 200 && strings.Contains(indexBody, "<title>NexTerm</title>") &&
		strings.Contains(indexBody, `window.__NEXTERM_TRANSPORT__="web"`), fmt.Sprintf("HTTP %d %s", status, truncate(indexBody, 120)))
	status, cacheControl, tokenBody, err := plainGet(server.port, "/share/public/not-a-real-token")
	if err != nil {
		return err
	}
	h.check("legacy public URL answers 410 without the SPA", status == http.StatusGone && tokenBody != indexBody, fmt.Sprintf("HTTP %d", status))
	h.check("legacy public URL responds with no-store", cacheControl == "no-store", fmt.Sprintf("Cache-Control=%q", cacheControl))
	goneStatus := wsHandshakeStatus(server.port, "/share/public/not-a-real-token")
	h.check("legacy public WS handshake answers 410", goneStatus == http.StatusGone, fmt.Sprintf("status=%d", goneStatus))

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
	ready, err := bobViewer.expectReady()
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
	okFlag, _ := getBool(body, "ok")
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
	bobStateDigest, err := localStateDigest(filepath.Join(bobAgentDir, "durable", "supervisor"))
	if err != nil {
		return err
	}
	if err := waitUntil(20*time.Second, "bob agent control channel online", func() bool {
		agent := getMap(bobDeviceRow(), "agent")
		return getStr(agent, "current_url") == baseURL && getStr(agent, "state_digest") == bobStateDigest
	}); err != nil {
		return err
	}
	h.check("bob agent control channel comes online", getStr(getMap(bobDeviceRow(), "agent"), "current_url") == baseURL &&
		getStr(getMap(bobDeviceRow(), "agent"), "state_digest") == bobStateDigest, fmt.Sprintf("row=%v", bobDeviceRow()))
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
	agent.stop()
	status, body, err = bob.request("GET", fmt.Sprintf("/share/devices/%s/terminal", deviceID), nil, false)
	if err != nil {
		return err
	}
	h.check("registered open returns 503 after going offline", status == 503, fmt.Sprintf("HTTP %d %v", status, body))
	fmt.Println("== disclosure defenses ==")
	status, body, err = newClient(server.port).request("GET", "/share/host-shares", nil, false)
	if err != nil {
		return err
	}
	h.check("anonymous access to share management returns 401", status == 401, fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = newClient(server.port).request("POST", "/sync/v2/pull", map[string]interface{}{"since": 0}, false)
	if err != nil {
		return err
	}
	h.check("anonymous access to sync protocol is rejected", status == 401 || status == 403, fmt.Sprintf("HTTP %d %v", status, body))
	return nil
}
