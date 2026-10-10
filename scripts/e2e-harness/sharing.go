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
	fmt.Println("== 超管初始化 ==")
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
	h.check("超管初始化", status == 200 && getStr(getMap(body, "user"), "username") == "alice", fmt.Sprintf("HTTP %d %v", status, body))
	alice := newClient(server.port)
	if err := alice.login("alice", alicePassword); err != nil {
		return err
	}

	fmt.Println("== 用户与设备准备 ==")
	status, body, err = alice.request("POST", "/admin/users", map[string]interface{}{"username": "bob", "password": bobPassword}, true)
	if err != nil {
		return err
	}
	h.check("超管创建 bob", status == 200 && getStr(getMap(body, "user"), "username") == "bob", fmt.Sprintf("HTTP %d %v", status, body))
	bobID := getStr(getMap(body, "user"), "id")
	status, body, err = alice.request("POST", "/admin/users", map[string]interface{}{"username": "carol", "password": carolPassword}, true)
	if err != nil {
		return err
	}
	h.check("超管创建 carol", status == 200 && getStr(getMap(body, "user"), "username") == "carol", fmt.Sprintf("HTTP %d %v", status, body))
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
	h.check("配置接入地址", status == 200 && len(getList(body, "base_urls")) == 1, fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = alice.request("POST", "/device/enroll-codes", map[string]interface{}{"ttl_ms": 600000}, true)
	if err != nil {
		return err
	}
	h.check("签发接入码", status == 200 && getStr(body, "code") != "", fmt.Sprintf("HTTP %d %v", status, body))
	enrollCode := getStr(body, "code")

	stdout, rc, stderr := enrollAgent(h, baseURL, enrollCode, agentDir, "sharing-box")
	h.check("agent enroll 子命令", rc == 0 && strings.Contains(stdout, "注册成功"), fmt.Sprintf("rc=%d %s %s", rc, stdout, stderr))
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
	h.check("关闭期望自启动", status == 200 && desiredOK && !desired, fmt.Sprintf("HTTP %d %v", status, body))
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
	h.check("agent 控制通道上线", getStr(getMap(deviceRow(), "agent"), "current_url") == baseURL, fmt.Sprintf("row=%v", deviceRow()))

	fmt.Println("== owner 经桥接建会话 ==")
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
	h.check("owner 桥接创建会话", sessionID != "", fmt.Sprintf("info=%v", info))

	fmt.Println("== 公开只读链接 ==")
	status, body, err = alice.request("POST", "/share/links", map[string]interface{}{
		"device_id": deviceID, "session_id": sessionID, "write": false, "ttl_ms": 3600000,
	}, true)
	if err != nil {
		return err
	}
	h.check("创建只读链接", status == 200 && getStr(body, "permission") == "read" && getStr(body, "token") != "", fmt.Sprintf("HTTP %d %v", status, body))
	readLinkID := getStr(body, "id")
	readToken := getStr(body, "token")

	fmt.Println("== 公开页面 plain GET ==")
	status, _, indexBody, err := plainGet(server.port, "/")
	if err != nil {
		return err
	}
	h.check("根路径 SPA 服务", status == 200 && strings.Contains(indexBody, "<title>NexTerm</title>") &&
		strings.Contains(indexBody, `window.__NEXTERM_TRANSPORT__="web"`), fmt.Sprintf("HTTP %d %s", status, truncate(indexBody, 120)))
	status, cacheControl, tokenBody, err := plainGet(server.port, "/share/public/"+readToken)
	if err != nil {
		return err
	}
	h.check("有效 token plain GET 服务同一 SPA", status == 200 && tokenBody == indexBody, fmt.Sprintf("HTTP %d", status))
	h.check("token URL 响应 no-store", cacheControl == "no-store", fmt.Sprintf("Cache-Control=%q", cacheControl))
	status, _, invalidBody, err := plainGet(server.port, "/share/public/not-a-real-token")
	if err != nil {
		return err
	}
	h.check("无效 token plain GET 同样服务 SPA (不校验 token)", status == 200 && invalidBody == indexBody, fmt.Sprintf("HTTP %d", status))
	postCode, err := postStatus(server.port, "/share/public/"+readToken)
	if err != nil {
		return err
	}
	h.check("POST 公开 token URL 不落入 SPA (405)", postCode == 405, fmt.Sprintf("HTTP %d", postCode))

	viewer, err := newShareViewer(server.port, "/share/public/"+readToken, nil)
	if err != nil {
		return err
	}
	ready, err := viewer.expectReady()
	if err != nil {
		return err
	}
	h.check("只读 ready 帧", getStr(ready, "permission") == "read" && getStr(ready, "session_id") == sessionID &&
		keysSubset(ready, readyFrameKeys), fmt.Sprintf("ready=%v", ready))
	if err := viewer.expectOutput([]byte("READY-42"), 15*time.Second); err != nil {
		return err
	}
	h.check("只读 viewer 收到会话输出", true, "")
	if err := viewer.ws.send(websocket.MessageBinary, []byte("echo nope-42\n")); err != nil {
		return err
	}
	if err := viewer.expectSilent(2 * time.Second); err != nil {
		h.check("只读输入被静默丢弃", false, err.Error())
	} else {
		h.check("只读输入被静默丢弃", true, "")
	}
	viewer.ws.close()

	fmt.Println("== 公开读写链接 ==")
	status, body, err = alice.request("POST", "/share/links", map[string]interface{}{
		"device_id": deviceID, "session_id": sessionID, "write": true, "ttl_ms": 3600000,
	}, true)
	if err != nil {
		return err
	}
	h.check("创建读写链接", status == 200 && getStr(body, "permission") == "read_write", fmt.Sprintf("HTTP %d %v", status, body))
	writeToken := getStr(body, "token")
	rwViewer, err := newShareViewer(server.port, "/share/public/"+writeToken, nil)
	if err != nil {
		return err
	}
	ready, err = rwViewer.expectReady()
	if err != nil {
		return err
	}
	h.check("读写 ready 帧", getStr(ready, "permission") == "read_write", fmt.Sprintf("ready=%v", ready))
	if err := rwViewer.expectOutput([]byte("READY-42"), 15*time.Second); err != nil {
		return err
	}
	if err := rwViewer.ws.send(websocket.MessageBinary, []byte("echo hello-42\n")); err != nil {
		return err
	}
	if err := rwViewer.expectOutput([]byte("hello-42"), 15*time.Second); err != nil {
		h.check("读写输入到达会话并回显", false, err.Error())
	} else {
		h.check("读写输入到达会话并回显", true, "")
	}

	fmt.Println("== 吊销 ==")
	status, body, err = alice.request("POST", fmt.Sprintf("/share/links/%s/revoke", readLinkID), map[string]interface{}{}, true)
	if err != nil {
		return err
	}
	okFlag, _ := getBool(body, "ok")
	h.check("吊销只读链接", status == 200 && okFlag, fmt.Sprintf("HTTP %d %v", status, body))
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
	h.check("吊销读写链接", status == 200 && okFlag, fmt.Sprintf("HTTP %d %v", status, body))
	errorFrame, err := rwViewer.expectError(30 * time.Second)
	if err != nil {
		h.check("吊销停止活跃 viewer (错误帧)", false, err.Error())
	} else {
		h.check("吊销停止活跃 viewer (错误帧)", strings.Contains(getStr(errorFrame, "message"), "吊销"), fmt.Sprintf("frame=%v", errorFrame))
	}
	if err := rwViewer.expectClosed(10 * time.Second); err != nil {
		return err
	}
	revokedStatus := wsHandshakeStatus(server.port, "/share/public/"+writeToken)
	h.check("吊销后 token 不可再用", revokedStatus == 403, fmt.Sprintf("status=%d", revokedStatus))

	fmt.Println("== 过期 ==")
	status, body, err = alice.request("POST", "/share/links", map[string]interface{}{
		"device_id": deviceID, "session_id": sessionID, "write": false, "ttl_ms": 60000,
	}, true)
	if err != nil {
		return err
	}
	h.check("创建 60s 短时效链接", status == 200, fmt.Sprintf("HTTP %d %v", status, body))
	expiringToken := getStr(body, "token")
	expiringViewer, err := newShareViewer(server.port, "/share/public/"+expiringToken, nil)
	if err != nil {
		return err
	}
	if _, err := expiringViewer.expectReady(); err != nil {
		return err
	}
	errorFrame, err = expiringViewer.expectError(100 * time.Second)
	if err != nil {
		h.check("过期停止活跃 viewer", false, err.Error())
	} else {
		h.check("过期停止活跃 viewer", strings.Contains(getStr(errorFrame, "message"), "过期"), fmt.Sprintf("frame=%v", errorFrame))
	}
	if err := expiringViewer.expectClosed(10 * time.Second); err != nil {
		return err
	}
	expiredStatus := wsHandshakeStatus(server.port, "/share/public/"+expiringToken)
	h.check("过期后 token 不可再用", expiredStatus == 403, fmt.Sprintf("status=%d", expiredStatus))

	fmt.Println("== 注册分享打开终端 ==")
	status, body, err = carol.request("GET", fmt.Sprintf("/share/devices/%s/terminal", deviceID), nil, false)
	if err != nil {
		return err
	}
	h.check("无分享用户打开被拒", status == 403, fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = alice.request("POST", "/share/host-shares", map[string]interface{}{
		"device_id": deviceID, "recipient_username": "bob", "write": true, "ttl_ms": 3600000,
	}, true)
	if err != nil {
		return err
	}
	h.check("授予 bob 主机分享", status == 200 && getStr(body, "permission") == "read_write", fmt.Sprintf("HTTP %d %v", status, body))
	shareID := getStr(body, "id")
	if extra := extraKeys(body, hostShareViewKeys); len(extra) > 0 {
		h.check("主机分享视图无多余字段", false, fmt.Sprintf("keys=%v", sortedKeysOf(body)))
	} else {
		h.check("主机分享视图无多余字段", true, "")
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
	h.check("bob 打开新终端", getStr(ready, "permission") == "read_write" && getStr(ready, "session_id") != "", fmt.Sprintf("ready=%v", ready))
	if err := bobViewer.ws.send(websocket.MessageBinary, []byte("echo hi-42\n")); err != nil {
		return err
	}
	if err := bobViewer.expectOutput([]byte("hi-42"), 15*time.Second); err != nil {
		h.check("bob 终端输入输出", false, err.Error())
	} else {
		h.check("bob 终端输入输出", true, "")
	}

	fmt.Println("== 权限收缩 read_write -> read ==")
	if err := bobViewer.ws.send(websocket.MessageBinary, []byte("while true; do echo tick; sleep 0.5; done\n")); err != nil {
		return err
	}
	if err := bobViewer.expectOutput([]byte("tick"), 15*time.Second); err != nil {
		h.check("收缩前终端持续输出", false, err.Error())
	}
	status, body, err = alice.request("POST", "/share/host-shares", map[string]interface{}{
		"device_id": deviceID, "recipient_username": "bob", "write": false, "ttl_ms": 3600000,
	}, true)
	if err != nil {
		return err
	}
	h.check("收缩为只读分享", status == 200 && getStr(body, "permission") == "read", fmt.Sprintf("HTTP %d %v", status, body))
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
	h.check("收缩后输出继续", bytes.Contains(shrinkOutput, []byte("tick")), fmt.Sprintf("output=%q", tailBytes(shrinkOutput, 200)))
	h.check("收缩后输入被丢弃", !bytes.Contains(shrinkOutput, []byte("nope-42")), fmt.Sprintf("output=%q", tailBytes(shrinkOutput, 200)))
	bobViewer.ws.close()

	status, body, err = alice.request("POST", fmt.Sprintf("/share/host-shares/%s/revoke", shareID), map[string]interface{}{}, true)
	if err != nil {
		return err
	}
	okFlag, _ = getBool(body, "ok")
	h.check("吊销主机分享", status == 200 && okFlag, fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = bob.request("GET", fmt.Sprintf("/share/devices/%s/terminal", deviceID), nil, false)
	if err != nil {
		return err
	}
	h.check("吊销后 bob 打开被拒", status == 403, fmt.Sprintf("HTTP %d %v", status, body))

	fmt.Println("== 普通 owner 以用户名创建主机分享 ==")
	status, body, err = alice.request("POST", "/device/enroll-codes", map[string]interface{}{"ttl_ms": 600000, "user_id": bobID}, true)
	if err != nil {
		return err
	}
	h.check("超管代 bob 签发接入码", status == 200 && getStr(body, "code") != "", fmt.Sprintf("HTTP %d %v", status, body))
	stdout, rc, stderr = enrollAgent(h, baseURL, getStr(body, "code"), bobAgentDir, "bob-box")
	h.check("bob agent enroll 子命令", rc == 0 && strings.Contains(stdout, "注册成功"), fmt.Sprintf("rc=%d %s %s", rc, stdout, stderr))
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
	h.check("bob 关闭期望自启动", status == 200 && desiredOK && !desired, fmt.Sprintf("HTTP %d %v", status, body))
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
	h.check("bob agent 控制通道上线", getStr(getMap(bobDeviceRow(), "agent"), "current_url") == baseURL, fmt.Sprintf("row=%v", bobDeviceRow()))
	status, body, err = bob.request("POST", "/share/host-shares", map[string]interface{}{
		"device_id": bobDeviceID, "recipient_username": "no-such-user", "write": false, "ttl_ms": 3600000,
	}, true)
	if err != nil {
		return err
	}
	h.check("普通 owner 无效用户名 404", status == 404 && getStr(getMap(body, "error"), "code") == "not_found", fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = bob.request("POST", "/share/host-shares", map[string]interface{}{
		"device_id": bobDeviceID, "recipient_username": "carol", "write": false, "ttl_ms": 3600000,
	}, true)
	if err != nil {
		return err
	}
	h.check("普通 owner 以用户名创建分享", status == 200 && getStr(body, "permission") == "read" &&
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
	h.check("carol 经 bob 的分享打开新终端", getStr(ready, "permission") == "read" && getStr(ready, "session_id") != "", fmt.Sprintf("ready=%v", ready))
	carolViewer.ws.close()
	bobAgent.stop()
	cleanupHelper(bobAgentDir)

	fmt.Println("== 守护代理离线 ==")
	status, body, err = alice.request("POST", "/share/host-shares", map[string]interface{}{
		"device_id": deviceID, "recipient_username": "bob", "write": true, "ttl_ms": 3600000,
	}, true)
	if err != nil {
		return err
	}
	h.check("重新授予 bob", status == 200, fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = alice.request("POST", "/share/links", map[string]interface{}{
		"device_id": deviceID, "session_id": sessionID, "write": true, "ttl_ms": 3600000,
	}, true)
	if err != nil {
		return err
	}
	h.check("离线前创建读写链接", status == 200, fmt.Sprintf("HTTP %d %v", status, body))
	offlineToken := getStr(body, "token")
	offlineViewer, err := newShareViewer(server.port, "/share/public/"+offlineToken, nil)
	if err != nil {
		return err
	}
	if _, err := offlineViewer.expectReady(); err != nil {
		return err
	}
	agent.stop()
	if err := offlineViewer.expectClosed(15 * time.Second); err != nil {
		h.check("agent 离线停止活跃分享流", false, err.Error())
	} else {
		h.check("agent 离线停止活跃分享流", true, "")
	}
	offlineStatus := wsHandshakeStatus(server.port, "/share/public/"+offlineToken)
	h.check("离线后公开链接 503", offlineStatus == 503, fmt.Sprintf("status=%d", offlineStatus))
	status, body, err = bob.request("GET", fmt.Sprintf("/share/devices/%s/terminal", deviceID), nil, false)
	if err != nil {
		return err
	}
	h.check("离线后注册打开 503", status == 503, fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = alice.request("POST", "/share/links", map[string]interface{}{
		"device_id": deviceID, "session_id": sessionID, "write": false, "ttl_ms": 3600000,
	}, true)
	if err != nil {
		return err
	}
	h.check("离线后创建链接 503", status == 503, fmt.Sprintf("HTTP %d %v", status, body))

	fmt.Println("== 无效 token WS 门禁 ==")
	invalidStatus := wsHandshakeStatus(server.port, "/share/public/not-a-real-token")
	h.check("无效 token WS 握手仍被拒", invalidStatus == 403, fmt.Sprintf("status=%d", invalidStatus))

	fmt.Println("== 披露防线 ==")
	status, body, err = newClient(server.port).request("GET", "/share/links", nil, false)
	if err != nil {
		return err
	}
	h.check("匿名访问分享管理 401", status == 401, fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = newClient(server.port).request("POST", "/sync/v2/pull", map[string]interface{}{"since": 0}, false)
	if err != nil {
		return err
	}
	h.check("匿名访问同步协议被拒", status == 401 || status == 403, fmt.Sprintf("HTTP %d %v", status, body))
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
	h.check("链接列表无 token 字段", status == 200 && leakFree, fmt.Sprintf("extra=%v", leaked))
	return nil
}
