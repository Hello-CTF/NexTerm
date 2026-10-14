package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const agentExitRevoked = 3

type agentProcess struct {
	cmd     *exec.Cmd
	waitCh  chan error
	exited  bool
	exitErr error
	logFile *os.File
}

func startAgent(h *harness, dataDir, logPath string) (*agentProcess, error) {
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(h.bin, "agent", "run", "--data-dir", dataDir)
	cmd.Dir = h.root
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, err
	}
	agent := &agentProcess{cmd: cmd, waitCh: make(chan error, 1), logFile: logFile}
	go func() { agent.waitCh <- cmd.Wait() }()
	return agent, nil
}

func (a *agentProcess) result(timeout time.Duration) (int, error) {
	if !a.exited {
		select {
		case err := <-a.waitCh:
			a.exited = true
			a.exitErr = err
		case <-time.After(timeout):
			return -1, fmt.Errorf("agent did not exit within %s", timeout)
		}
	}
	return exitCodeOf(a.exitErr), nil
}

func (a *agentProcess) stop() {
	if a == nil {
		return
	}
	if a.cmd != nil && !a.exited {
		terminateProcess(a.cmd.Process)
		select {
		case err := <-a.waitCh:
			a.exited = true
			a.exitErr = err
		case <-time.After(5 * time.Second):
			a.cmd.Process.Kill()
			select {
			case err := <-a.waitCh:
				a.exited = true
				a.exitErr = err
			case <-time.After(3 * time.Second):
			}
		}
	}
	a.cmd = nil
	if a.logFile != nil {
		a.logFile.Close()
		a.logFile = nil
	}
}

func cleanupHelper(agentDir string) {
	raw, err := os.ReadFile(filepath.Join(agentDir, "durable", "supervisor", "helper.pid"))
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return
	}
	terminatePID(pid)
}

func enrollAgent(h *harness, serverURL, code, dataDir, name string) (string, int, string) {
	cmd := exec.Command(h.bin, "agent", "enroll", "--server", serverURL, "--code", code, "--data-dir", dataDir, "--name", name)
	cmd.Dir = h.root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), exitCodeOf(err), stderr.String()
}

func runDevice(h *harness) error {
	work, err := os.MkdirTemp("", "nexterm-device-e2e-")
	if err != nil {
		return err
	}
	masterKey := "e2e-master-" + randomHex(16)
	server, err := newInstance(h, work, "server", masterKey, "on", "")
	if err != nil {
		return err
	}
	agentDir := filepath.Join(work, "agent")
	var agent *agentProcess
	defer func() {
		agent.stop()
		cleanupHelper(agentDir)
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
	status, body, err := initSuperadmin(h, server.port, initCode, "alice", alicePassword)
	if err != nil {
		return err
	}
	h.check("superadmin initialization", status == 200 && getStr(getMap(body, "user"), "username") == "alice", fmt.Sprintf("HTTP %d %v", status, body))
	alice := newClient(server.port)
	if err := alice.login("alice", alicePassword); err != nil {
		return err
	}

	fmt.Println("== enrollment ==")
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", server.port)
	status, body, err = alice.request("PUT", "/fleet/base-urls", map[string]interface{}{"base_urls": []map[string]string{{"url": baseURL}}}, true)
	if err != nil {
		return err
	}
	h.check("configure server base URLs", status == 200 && len(getList(body, "base_urls")) == 1, fmt.Sprintf("HTTP %d %v", status, body))

	status, body, err = alice.request("POST", "/device/enroll-codes", map[string]interface{}{"ttl_ms": 600000}, false)
	if err != nil {
		return err
	}
	h.check("write request without CSRF is rejected", status == 403, fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = alice.request("POST", "/device/enroll-codes", map[string]interface{}{"ttl_ms": 600000}, true)
	if err != nil {
		return err
	}
	h.check("issue enrollment code", status == 200 && getStr(body, "code") != "", fmt.Sprintf("HTTP %d %v", status, body))
	enrollCode := getStr(body, "code")

	stdout, rc, stderr := enrollAgent(h, baseURL, enrollCode, agentDir, "e2e-box")
	h.check("agent enroll subcommand", rc == 0 && strings.Contains(stdout, "注册成功"), fmt.Sprintf("rc=%d %s %s", rc, stdout, stderr))
	configPath := filepath.Join(agentDir, "fleet", "agent.json")
	mode := 0
	if info, err := os.Stat(configPath); err == nil {
		mode = int(info.Mode().Perm())
	}
	h.check("enroll config is 0600", mode == 0o600, fmt.Sprintf("mode=%#o", mode))
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	agentConfig := map[string]interface{}{}
	if err := json.Unmarshal(raw, &agentConfig); err != nil {
		return err
	}
	deviceID := getStr(agentConfig, "device_id")
	deviceSecret := getStr(agentConfig, "secret")

	status, body, err = newClient(server.port).request("POST", "/device/enroll", map[string]interface{}{"code": enrollCode, "name": "reuse"}, false)
	if err != nil {
		return err
	}
	h.check("enrollment code is single-use", status == 403, fmt.Sprintf("HTTP %d %v", status, body))

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

	fmt.Println("== agent run online ==")
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
	row := deviceRow()
	h.check("control channel comes online and reports current_url", getStr(getMap(row, "agent"), "current_url") == baseURL, fmt.Sprintf("row=%v", row))

	fmt.Println("== sync metrics ==")
	status, body, err = newClient(server.port).request("POST", "/agent/sync", map[string]interface{}{
		"device_id": deviceID,
		"secret":    deviceSecret,
		"sample": map[string]interface{}{
			"ts": time.Now().UnixMilli(), "cpu_pct": 42.5, "mem_used": 1, "mem_total": 2,
			"disk_used": 3, "disk_total": 4, "uptime_s": 5,
		},
	}, false)
	if err != nil {
		return err
	}
	desired, desiredOK = getBool(body, "desired_autostart")
	terminalEnabled, _ := getBool(body, "terminal_enabled")
	h.check("direct sync report", status == 200 && desiredOK && !desired && terminalEnabled, fmt.Sprintf("HTTP %d %v", status, body))

	metricsSamples := func() []interface{} {
		_, body, err := alice.request("GET", fmt.Sprintf("/fleet/devices/%s/metrics", deviceID), nil, false)
		if err != nil {
			return nil
		}
		return getList(body, "samples")
	}
	cpuOf := func(sample interface{}) float64 {
		entry, _ := sample.(map[string]interface{})
		return getFloat(entry, "cpu_pct")
	}
	sawDirect := func() bool {
		for _, sample := range metricsSamples() {
			if cpuOf(sample) == 42.5 {
				return true
			}
		}
		return false
	}
	if err := waitUntil(15*time.Second, "direct sync sample visible", sawDirect); err != nil {
		return err
	}
	samplesJSON, _ := json.Marshal(metricsSamples())
	h.check("sync metrics can be read back", sawDirect(), fmt.Sprintf("samples=%s", truncate(string(samplesJSON), 300)))
	sawNatural := func() bool {
		for _, sample := range metricsSamples() {
			if cpuOf(sample) != 42.5 {
				return true
			}
		}
		return false
	}
	if err := waitUntil(20*time.Second, "natural agent sync sample", sawNatural); err != nil {
		return err
	}
	samplesJSON, _ = json.Marshal(metricsSamples())
	h.check("natural agent sync is persisted", sawNatural(), fmt.Sprintf("samples=%s", truncate(string(samplesJSON), 300)))

	fmt.Println("== outbound bridge and resume ==")
	stateDigest, err := localStateDigest(filepath.Join(agentDir, "durable", "supervisor"))
	if err != nil {
		return err
	}
	listedDigest := func() string {
		return getStr(getMap(deviceRow(), "agent"), "state_digest")
	}
	if err := waitUntil(10*time.Second, "device list exposes state digest", func() bool { return listedDigest() == stateDigest }); err != nil {
		return err
	}
	h.check("device list returns state digest", listedDigest() == stateDigest, fmt.Sprintf("listed=%q", listedDigest()))

	relayHeader := http.Header{}
	relayHeader.Set("Cookie", sessionCookie+"="+alice.cookie)
	openRelay := func() (*supervisorStream, error) {
		ws, _, err := dialWS(server.port, fmt.Sprintf("/fleet/devices/%s/bridge", deviceID), relayHeader)
		if err != nil {
			return nil, err
		}
		stream := &supervisorStream{ws: ws}
		if err := stream.hello(stateDigest); err != nil {
			ws.close()
			return nil, err
		}
		return stream, nil
	}

	creator, err := openRelay()
	if err != nil {
		return err
	}
	info, err := creator.create([]string{"sh", "-c", "echo READY-42; sleep 3; echo LATE-7; sleep 60"}, 80, 24)
	if err != nil {
		return err
	}
	sessionID := getStr(info, "id")
	incarnation := getStr(info, "incarnation")
	createdAt, err := time.Parse(time.RFC3339Nano, getStr(info, "created_at"))
	if err != nil {
		return err
	}
	createdAtNs := createdAt.UnixNano()
	if err := creator.detach(); err != nil {
		return err
	}
	creator.ws.close()

	first, err := openRelay()
	if err != nil {
		return err
	}
	attached, err := first.attach(sessionID, 0, "")
	if err != nil {
		return err
	}
	h.check("bridge attaches to session", getStr(attached, "id") == sessionID, fmt.Sprintf("info=%v", attached))
	var output []byte
	deadline := time.Now().Add(15 * time.Second)
	for !bytes.Contains(output, []byte("READY-42")) && time.Now().Before(deadline) {
		chunk, err := first.readOutput(5 * time.Second)
		if err != nil {
			return err
		}
		output = append(output, chunk...)
	}
	h.check("bridge reads session output", bytes.Contains(output, []byte("READY-42")), fmt.Sprintf("output=%q", tailBytes(output, 200)))
	if err := first.detach(); err != nil {
		return err
	}
	first.ws.close()

	time.Sleep(4 * time.Second)
	second, err := openRelay()
	if err != nil {
		return err
	}
	resumed, err := second.attach(sessionID, createdAtNs, incarnation)
	if err != nil {
		return err
	}
	h.check("expected incarnation resumes successfully", getStr(resumed, "id") == sessionID, fmt.Sprintf("info=%v", resumed))
	var backlog []byte
	deadline = time.Now().Add(10 * time.Second)
	for !bytes.Contains(backlog, []byte("LATE-7")) && time.Now().Before(deadline) {
		chunk, err := second.readOutput(5 * time.Second)
		if err != nil {
			return err
		}
		backlog = append(backlog, chunk...)
	}
	h.check("resume reads output produced while disconnected", bytes.Contains(backlog, []byte("LATE-7")), fmt.Sprintf("backlog=%q", tailBytes(backlog, 200)))
	if err := second.detach(); err != nil {
		return err
	}
	second.ws.close()

	third, err := openRelay()
	if err != nil {
		return err
	}
	if _, err := third.attach(sessionID, createdAtNs, "wrong-incarnation"); err == nil {
		h.check("wrong incarnation is rejected", false, "attach succeeded with wrong incarnation")
	} else {
		var wire *wireError
		detail := fmt.Sprintf("err=%v", err)
		if errors.As(err, &wire) {
			detail = fmt.Sprintf("code=%s", wire.code)
		}
		h.check("wrong incarnation is rejected", errors.As(err, &wire) && wire.code == "identity", detail)
	}
	third.ws.close()

	killer, err := openRelay()
	if err != nil {
		return err
	}
	if err := killer.killSession(sessionID); err != nil {
		return err
	}
	killer.ws.close()
	h.check("kill_session cleans up the session", true, "")

	fmt.Println("== protocol errors ==")
	helloPayload := func(protocol int, secret string) []byte {
		raw, _ := json.Marshal(map[string]interface{}{"type": "hello", "protocol": protocol, "device_id": deviceID, "secret": secret})
		return raw
	}
	ws, _, err := dialWS(server.port, "/ws/device", nil)
	if err != nil {
		return err
	}
	if err := ws.send(websocket.MessageText, helloPayload(99, deviceSecret)); err != nil {
		return err
	}
	message, err := ws.recv(10 * time.Second)
	if err != nil {
		return err
	}
	errorFrame := map[string]interface{}{}
	if message.err == nil && message.kind == websocket.MessageText {
		json.Unmarshal(message.payload, &errorFrame)
	}
	h.check("protocol mismatch returns version_mismatch", getStr(errorFrame, "code") == "version_mismatch", fmt.Sprintf("frame=%v", errorFrame))
	ws.close()

	ws, _, err = dialWS(server.port, "/ws/device", nil)
	if err != nil {
		return err
	}
	if err := ws.send(websocket.MessageText, helloPayload(1, "wrong")); err != nil {
		return err
	}
	message, err = ws.recv(10 * time.Second)
	if err != nil {
		return err
	}
	errorFrame = map[string]interface{}{}
	if message.err == nil && message.kind == websocket.MessageText {
		json.Unmarshal(message.payload, &errorFrame)
	}
	h.check("invalid credentials return forbidden", getStr(errorFrame, "code") == "forbidden", fmt.Sprintf("frame=%v", errorFrame))
	ws.close()

	fmt.Println("== revocation ==")
	status, body, err = alice.request("POST", fmt.Sprintf("/fleet/devices/%s/revoke", deviceID), map[string]interface{}{}, true)
	if err != nil {
		return err
	}
	okFlag, _ := getBool(body, "ok")
	h.check("revoke device", status == 200 && okFlag, fmt.Sprintf("HTTP %d %v", status, body))
	exitCode, err := agent.result(15 * time.Second)
	if err != nil {
		return err
	}
	h.check("agent receives revoke and exits with code 3", exitCode == agentExitRevoked, fmt.Sprintf("exit=%d", exitCode))
	status, body, err = newClient(server.port).request("POST", "/agent/sync", map[string]interface{}{"device_id": deviceID, "secret": deviceSecret}, false)
	if err != nil {
		return err
	}
	h.check("sync returns 403 after revocation", status == 403, fmt.Sprintf("HTTP %d %v", status, body))

	fmt.Println("== --auth=off disables fleet ==")
	offServer, err := newInstance(h, work, "server-off", masterKey, "off", "")
	if err != nil {
		return err
	}
	if err := offServer.start(); err != nil {
		return err
	}
	defer offServer.stop()
	status, body, err = newClient(offServer.port).request("POST", "/device/enroll", map[string]interface{}{"code": "x", "name": "y"}, false)
	if err != nil {
		return err
	}
	h.check("enroll returns 403 when off", status == 403, fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = newClient(offServer.port).request("POST", "/agent/sync", map[string]interface{}{"device_id": "x", "secret": "y"}, false)
	if err != nil {
		return err
	}
	h.check("agent/sync returns 403 when off", status == 403, fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = newClient(offServer.port).request("GET", "/fleet/devices", nil, false)
	if err != nil {
		return err
	}
	h.check("fleet management returns 403 when off", status == 403, fmt.Sprintf("HTTP %d %v", status, body))
	wsStatus := wsHandshakeStatus(offServer.port, "/ws/device")
	h.check("/ws/device returns 403 when off", wsStatus == 403, fmt.Sprintf("status=%d", wsStatus))
	return nil
}
