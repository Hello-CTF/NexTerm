//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type testServerProcess struct {
	cmd    *exec.Cmd
	wait   chan error
	output *syncBuffer
}

func TestServerProcessBootstrapsVaultFromMasterKeyEnv(t *testing.T) {
	binary := buildServerBinary(t)
	dataDir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "--listen", address, "--data-dir", dataDir, "--auth=loopback")
	cmd.Env = serverProcessEnv(t, "NEXTERM_WEB_ROOT=", "NEXTERM_MASTER_KEY=regression-master-key")
	buffer := &syncBuffer{}
	cmd.Stdout = buffer
	cmd.Stderr = buffer
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	process := &testServerProcess{cmd: cmd, wait: make(chan error, 1), output: buffer}
	go func() { process.wait <- cmd.Wait() }()
	defer process.stop(t)
	health := waitForHealth(t, process, address)
	if health.Vault == nil {
		t.Fatal("health has no vault status")
	}
	if !strings.Contains(process.output.String(), "deprecated") {
		t.Fatalf("NEXTERM_MASTER_KEY deprecation warning missing: %q", process.output.String())
	}
	encoded, err := json.Marshal(health.Vault)
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		Initialized bool `json:"initialized"`
		Unlocked    bool `json:"unlocked"`
	}
	if err := json.Unmarshal(encoded, &status); err != nil {
		t.Fatal(err)
	}
	if !status.Initialized || !status.Unlocked {
		t.Fatalf("vault status = %+v, want initialized and unlocked from NEXTERM_MASTER_KEY", status)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	created := requestFullRPC(t, client, address, "vault_set_credential", map[string]any{"args": map[string]any{"name": "regression", "kind": "password", "secret": "s3cret"}})
	var createdData map[string]string
	decodeRPCData(t, created, &createdData)
	if createdData["id"] == "" {
		t.Fatalf("vault_set_credential data = %+v", createdData)
	}
	revealed := requestFullRPC(t, client, address, "vault_reveal_credential", map[string]any{"id": createdData["id"]})
	var revealedData struct {
		Value string `json:"value"`
	}
	decodeRPCData(t, revealed, &revealedData)
	if revealedData.Value != "s3cret" {
		t.Fatalf("vault_reveal_credential value = %q", revealedData.Value)
	}
}

func TestSyncOnlyProcessServesV2ObjectProtocol(t *testing.T) {
	binary := buildServerBinary(t)
	dataDir := t.TempDir()
	process, address := startServerProcess(t, binary, dataDir, true)
	defer process.stop(t)
	health := waitForHealth(t, process, address)
	if !health.OK || !health.SyncOnly || health.Commands != 0 || health.WebRoot != nil || health.Vault == nil || health.Retention == nil {
		t.Fatalf("sync-only health = %+v", health)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	requestURL(t, client, "http://"+address+"/rpc", http.MethodPost, strings.NewReader(`{"cmd":"app_platform","args":null}`), map[string]string{"Content-Type": "application/json"}, http.StatusNotFound)
	requestURL(t, client, "http://"+address+"/", http.MethodGet, nil, nil, http.StatusNotFound)
	requestURL(t, client, "http://"+address+"/sync/v2/push", http.MethodPost, strings.NewReader(`{"protocol":2,"known_head":"","objects":[]}`), map[string]string{"Content-Type": "application/json"}, http.StatusUnauthorized)

	session := initSuperadminThroughProcess(t, process, address, "alice", "alice-pw-123")
	push := requestAccountJSON(t, client, address, "/sync/v2/push", map[string]any{
		"protocol": 2, "known_head": genesisHeadForTest(session.userID), "objects": []map[string]any{},
	}, session, http.StatusOK)
	var pushed struct {
		Head    string `json:"head"`
		Applied int    `json:"applied"`
	}
	decodeAccountData(t, push, &pushed)
	if pushed.Head == "" {
		t.Fatalf("push response = %s", push.body)
	}
	pull := requestAccountJSON(t, client, address, "/sync/v2/pull", map[string]any{"protocol": 2, "since_seq": 0}, session, http.StatusOK)
	if !strings.Contains(string(pull.body), `"objects"`) {
		t.Fatalf("pull response = %s", pull.body)
	}
	ids := requestAccountJSON(t, client, address, "/sync/v2/ids", map[string]any{"protocol": 2}, session, http.StatusOK)
	if !strings.Contains(string(ids.body), `"entries"`) {
		t.Fatalf("ids response = %s", ids.body)
	}
	// 推送必须携带 CSRF 头; 缺失时拒绝。
	requestAccountJSON(t, client, address, "/sync/v2/push", map[string]any{
		"protocol": 2, "known_head": pushed.Head, "objects": []map[string]any{},
	}, &accountSession{cookie: session.cookie}, http.StatusForbidden)
}

func TestFullServerProcessKeepsProductionGrid(t *testing.T) {
	binary := buildServerBinary(t)
	dataDir := t.TempDir()
	process, address := startServerProcess(t, binary, dataDir, false, "--auth=loopback")
	defer process.stop(t)
	health := waitForHealth(t, process, address)
	if !health.OK || health.SyncOnly || health.Commands < 120 {
		t.Fatalf("full health = %+v", health)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response := requestFullRPC(t, client, address, "app_platform", nil)
	var platform string
	decodeRPCData(t, response, &platform)
	if platform != runtime.GOOS {
		t.Fatalf("app_platform = %q, want %q", platform, runtime.GOOS)
	}
	response = requestFullRPC(t, client, address, "session_connect_local", nil)
	var connected struct{ ID string }
	decodeRPCData(t, response, &connected)
	if connected.ID == "" {
		t.Fatal("session_connect_local returned no session")
	}
	response = requestFullRPC(t, client, address, "terminal_attach", map[string]any{"sessionId": connected.ID, "cols": 80, "rows": 24})
	var tabID string
	decodeRPCData(t, response, &tabID)
	if tabID == "" {
		t.Fatal("terminal_attach returned no tab")
	}
	requestFullRPC(t, client, address, "terminal_resize", map[string]any{"tabId": tabID, "cols": 100, "rows": 30})
	response = requestFullRPC(t, client, address, "terminal_resize_flush", map[string]any{"tabId": tabID})
	var flushed any
	decodeRPCData(t, response, &flushed)
	if flushed != nil {
		t.Fatalf("terminal_resize_flush data = %#v", flushed)
	}
	response = requestFullRPC(t, client, address, "terminal_list", nil)
	var tabs []struct {
		TabID string `json:"tabId"`
		Cols  int    `json:"cols"`
		Rows  int    `json:"rows"`
	}
	decodeRPCData(t, response, &tabs)
	if len(tabs) != 1 || tabs[0].TabID != tabID || tabs[0].Cols != 100 || tabs[0].Rows != 30 {
		t.Fatalf("terminal_list = %+v", tabs)
	}
}

type rpcTestResponse struct {
	status int
	body   []byte
}

func TestServerProcessAuthDefaultsOn(t *testing.T) {
	binary := buildServerBinary(t)
	dataDir := t.TempDir()
	process, address := startServerProcess(t, binary, dataDir, false)
	defer process.stop(t)
	waitForHealth(t, process, address)

	client := &http.Client{Timeout: 5 * time.Second}
	requestRPCPath(t, client, address, "/rpc", "app_platform", nil, http.StatusUnauthorized)
	requestURL(t, client, "http://"+address+"/healthz", http.MethodGet, nil, nil, http.StatusOK)

	session := initSuperadminThroughProcess(t, process, address, "alice", "alice-pw-123")
	authenticated := requestAccountJSON(t, client, address, "/rpc", map[string]any{
		"cmd": "app_platform", "args": map[string]any{}, "channel": "process-grid", "clientId": "process-client",
	}, session, http.StatusOK)
	if !strings.Contains(string(authenticated.body), `"ok":true`) {
		t.Fatalf("session RPC response = %s", authenticated.body)
	}
}

func TestServerProcessRequireVault(t *testing.T) {
	binary := buildServerBinary(t)
	dataDir := t.TempDir()

	exitCode := func(args ...string) (int, string) {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Env = serverProcessEnv(t, "NEXTERM_WEB_ROOT=")
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("process succeeded, want failure: %s", output)
		}
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("process error = %v", err)
		}
		return exitErr.ExitCode(), string(output)
	}

	code, output := exitCode("--listen", "127.0.0.1:0", "--data-dir", dataDir, "--require-vault")
	if code != 1 || !strings.Contains(output, "vault") {
		t.Fatalf("require-vault without key = code %d, output %q", code, output)
	}

	keyFile := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyFile, []byte("first-master-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	process, address := startServerProcess(t, binary, dataDir, false, "--require-vault", "--master-key-file", keyFile)
	health := waitForHealth(t, process, address)
	encoded, err := json.Marshal(health.Vault)
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		Initialized bool `json:"initialized"`
		Unlocked    bool `json:"unlocked"`
	}
	if err := json.Unmarshal(encoded, &status); err != nil {
		t.Fatal(err)
	}
	if !status.Initialized || !status.Unlocked {
		t.Fatalf("vault status = %+v", status)
	}
	if strings.Contains(process.output.String(), "deprecated") {
		t.Fatalf("master key file triggered env deprecation warning: %q", process.output.String())
	}
	process.stop(t)

	if err := os.WriteFile(keyFile, []byte("second-master-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, output = exitCode("--listen", "127.0.0.1:0", "--data-dir", dataDir, "--require-vault", "--master-key-file", keyFile)
	if code != 1 || !strings.Contains(output, "vault") {
		t.Fatalf("require-vault with wrong key = code %d, output %q", code, output)
	}

	process, address = startServerProcess(t, binary, dataDir, false, "--master-key-file", keyFile)
	defer process.stop(t)
	health = waitForHealth(t, process, address)
	encoded, err = json.Marshal(health.Vault)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &status); err != nil {
		t.Fatal(err)
	}
	if !status.Initialized || status.Unlocked {
		t.Fatalf("wrong key without require-vault = %+v, want locked vault still serving", status)
	}
}

func TestSystemdUnitsEnforceAuthOn(t *testing.T) {
	for _, unit := range []string{"nexterm-server.service", "nexterm-onlyserver.service"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "systemd", unit))
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		if !strings.Contains(content, "--auth on") {
			t.Fatalf("%s does not enforce --auth on:\n%s", unit, content)
		}
		if strings.Contains(content, "--auth loopback") || strings.Contains(content, "--auth off") {
			t.Fatalf("%s weakens access control:\n%s", unit, content)
		}
	}
}

func TestServerProcessRejectsInvalidAuthMode(t *testing.T) {
	binary := buildServerBinary(t)
	cmd := exec.Command(binary, "--listen", "127.0.0.1:0", "--data-dir", t.TempDir(), "--auth=bogus")
	cmd.Env = serverProcessEnv(t, "NEXTERM_WEB_ROOT=")
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("invalid --auth = %v, output %q", err, output)
	}
}

func TestServerProcessWarnsOnNonLoopbackExposure(t *testing.T) {
	binary := buildServerBinary(t)
	start := func(t *testing.T, listen string, syncOnly bool) (*testServerProcess, string) {
		t.Helper()
		dataDir := t.TempDir()
		args := []string{"--listen", listen, "--data-dir", dataDir}
		if syncOnly {
			args = append(args, "--sync-only")
		}
		cmd := exec.Command(binary, args...)
		cmd.Env = serverProcessEnv(t, "NEXTERM_WEB_ROOT=")
		buffer := &syncBuffer{}
		cmd.Stdout = buffer
		cmd.Stderr = buffer
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		process := &testServerProcess{cmd: cmd, wait: make(chan error, 1), output: buffer}
		go func() { process.wait <- cmd.Wait() }()
		return process, dataDir
	}
	loopbackAddress := func(t *testing.T) string {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		return address
	}

	for _, tc := range []struct {
		name     string
		syncOnly bool
		detail   string
		absent   string
	}{
		{"full", false, "完整版已启用访问控制", "onlyServer 模式"},
		{"sync-only", true, "onlyServer 模式", "完整版已启用访问控制"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			healthAddress := loopbackAddress(t)
			listen := strings.Replace(healthAddress, "127.0.0.1", "0.0.0.0", 1)
			process, _ := start(t, listen, tc.syncOnly)
			defer process.stop(t)
			waitForHealth(t, process, healthAddress)
			output := process.output.String()
			if !strings.Contains(output, "WARNING: NexTerm is listening on non-loopback address "+listen) || !strings.Contains(output, tc.detail) {
				t.Fatalf("exposure warning missing: %q", output)
			}
			if strings.Contains(output, tc.absent) {
				t.Fatalf("wrong-mode wording %q in output: %q", tc.absent, output)
			}
			if count := strings.Count(output, "WARNING: NexTerm is listening on non-loopback address"); count != 1 {
				t.Fatalf("stderr warning fired %d times: %q", count, output)
			}
			if count := strings.Count(output, "HTTP server is listening on a non-loopback address"); count != 1 {
				t.Fatalf("logger warning fired %d times: %q", count, output)
			}
		})
	}

	t.Run("loopback", func(t *testing.T) {
		address := loopbackAddress(t)
		process, _ := start(t, address, false)
		defer process.stop(t)
		waitForHealth(t, process, address)
		if output := process.output.String(); strings.Contains(output, "non-loopback") {
			t.Fatalf("loopback listen triggered exposure warning: %q", output)
		}
	})
}

func buildServerBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nexterm-server")
	cmd := exec.Command("go", "build", "-mod=readonly", "-o", path, ".")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, output)
	}
	return path
}

func serverProcessEnv(t *testing.T, extra ...string) []string {
	t.Helper()
	shared, err := os.MkdirTemp("", "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shared) })
	if err := os.Chmod(shared, 0o1777); err != nil {
		t.Fatal(err)
	}
	env := make([]string, 0, len(os.Environ())+len(extra)+1)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "TMPDIR=") {
			continue
		}
		env = append(env, entry)
	}
	return append(append(env, "TMPDIR="+shared), extra...)
}

func startServerProcess(t *testing.T, binary, dataDir string, syncOnly bool, extraArgs ...string) (*testServerProcess, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	args := []string{"--listen", address, "--data-dir", dataDir}
	if syncOnly {
		args = append(args, "--sync-only")
	}
	args = append(args, extraArgs...)
	cmd := exec.Command(binary, args...)
	cmd.Env = serverProcessEnv(t, "NEXTERM_WEB_ROOT=")
	buffer := &syncBuffer{}
	cmd.Stdout = buffer
	cmd.Stderr = buffer
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	process := &testServerProcess{cmd: cmd, wait: make(chan error, 1), output: buffer}
	go func() { process.wait <- cmd.Wait() }()
	return process, address
}

func (p *testServerProcess) stop(t *testing.T) {
	t.Helper()
	_ = p.cmd.Process.Kill()
	select {
	case <-p.wait:
	case <-time.After(5 * time.Second):
		t.Log("server process did not stop after kill")
	}
}

func (p *testServerProcess) err() error {
	select {
	case err := <-p.wait:
		return err
	default:
		return nil
	}
}

type accountSession struct {
	cookie *http.Cookie
	csrf   string
	userID string
}

// initSuperadminThroughProcess 经控制台初始化码完成超管初始化并登录, 返回会话 cookie 与 CSRF 令牌。
func initSuperadminThroughProcess(t *testing.T, process *testServerProcess, address, username, password string) *accountSession {
	t.Helper()
	marker := "一次性初始化码: "
	deadline := time.Now().Add(15 * time.Second)
	var code string
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(process.output.String(), "\n") {
			if strings.Contains(line, marker) {
				code = strings.TrimSpace(strings.SplitN(line, marker, 2)[1])
			}
		}
		if code != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if code == "" {
		t.Fatalf("init code not found in process output: %s", process.output.String())
	}
	_, envelopes, _, err := vault.GenerateUserDEKEnvelopes(password)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"code": code, "username": username, "password": password,
		"dek_envelope": envelopes.DEKEnvelope, "kdf_salt": envelopes.KDFSalt, "kdf_params": envelopes.KDFParams,
		"recovery_envelope": envelopes.RecoveryEnvelope, "recovery_hash": envelopes.RecoveryHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "http://"+address+"/auth/init", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("init status = %d", response.StatusCode)
	}
	var body struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	session := &accountSession{csrf: body.CSRFToken, userID: body.User.ID}
	for _, cookie := range response.Cookies() {
		if cookie.Name == "nexterm_session" {
			session.cookie = cookie
		}
	}
	if session.cookie == nil || session.csrf == "" || session.userID == "" {
		t.Fatalf("init session incomplete: %+v", session)
	}
	return session
}

func requestAccountJSON(t *testing.T, client *http.Client, address, path string, payload any, session *accountSession, expectedStatus int) rpcTestResponse {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+address+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if session != nil && session.cookie != nil {
		request.AddCookie(session.cookie)
	}
	if session != nil && session.csrf != "" {
		request.Header.Set("X-NexTerm-CSRF", session.csrf)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != expectedStatus {
		t.Fatalf("%s status = %d, want %d: %s", path, response.StatusCode, expectedStatus, body)
	}
	return rpcTestResponse{status: response.StatusCode, body: body}
}

func decodeAccountData(t *testing.T, response rpcTestResponse, target any) {
	t.Helper()
	if err := json.Unmarshal(response.body, target); err != nil {
		t.Fatalf("decode %s: %v", response.body, err)
	}
}

func genesisHeadForTest(userID string) string {
	digest := sha256.Sum256([]byte("nexterm/go/sync-head/v1\x00" + userID))
	return hex.EncodeToString(digest[:])
}

func waitForHealth(t *testing.T, process *testServerProcess, address string) core.Health {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	client := &http.Client{Timeout: time.Second}
	for {
		if err := process.err(); err != nil {
			t.Fatalf("server exited before health: %v\n%s", err, process.output.String())
		}
		response, err := client.Get("http://" + address + "/healthz")
		if err == nil {
			defer response.Body.Close()
			if response.StatusCode == http.StatusOK {
				var health core.Health
				if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
					t.Fatal(err)
				}
				return health
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("health not ready: %v\n%s", err, process.output.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func requestFullRPC(t *testing.T, client *http.Client, address, command string, args any) rpcTestResponse {
	t.Helper()
	return requestRPCPath(t, client, address, "/rpc", command, args)
}

func requestRPC(t *testing.T, client *http.Client, address, command string, args any, expectedStatus ...int) rpcTestResponse {
	t.Helper()
	return requestRPCPath(t, client, address, "/sync/rpc", command, args, expectedStatus...)
}

func requestRPCPath(t *testing.T, client *http.Client, address, path, command string, args any, expectedStatus ...int) rpcTestResponse {
	t.Helper()
	envelope := map[string]any{"cmd": command, "args": args}
	if path == "/rpc" {
		envelope["channel"] = "process-grid"
		envelope["clientId"] = "process-client"
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+address+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(expectedStatus) > 0 && response.StatusCode != expectedStatus[0] {
		t.Fatalf("%s status = %d, want %d: %s", command, response.StatusCode, expectedStatus[0], body)
	}
	if len(expectedStatus) == 0 && response.StatusCode != http.StatusOK {
		t.Fatalf("%s status = %d: %s", command, response.StatusCode, body)
	}
	if len(expectedStatus) == 0 {
		var envelope struct {
			OK    bool            `json:"ok"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("%s response = %s: %v", command, body, err)
		}
		if !envelope.OK {
			t.Fatalf("%s response = %s", command, body)
		}
	}
	return rpcTestResponse{status: response.StatusCode, body: body}
}

func decodeRPCData(t *testing.T, response rpcTestResponse, target any) {
	t.Helper()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(response.body, &envelope); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		t.Fatalf("decode %s: %v", envelope.Data, err)
	}
}

func requestURL(t *testing.T, client *http.Client, url, method string, body io.Reader, headers map[string]string, expectedStatus int) {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		t.Fatalf("%s %s status = %d, want %d", method, url, response.StatusCode, expectedStatus)
	}
}
