//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

	"github.com/Hello-CTF/NexTerm/internal/account"
	core "github.com/Hello-CTF/NexTerm/internal/app"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
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

func TestServerProcessBootstrapsVaultFromMasterKeyFile(t *testing.T) {
	binary := buildServerBinary(t)
	dataDir := t.TempDir()
	keyFile := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyFile, []byte("regression-master-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "--listen", address, "--data-dir", dataDir, "--auth=loopback", "--master-key-file", keyFile)
	cmd.Env = serverProcessEnv(t, "NEXTERM_WEB_ROOT=")
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
	encoded, err := json.Marshal(health.Vault)
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		Initialized     bool   `json:"initialized"`
		Unlocked        bool   `json:"unlocked"`
		AutoLockMinutes uint64 `json:"autoLockMinutes"`
	}
	if err := json.Unmarshal(encoded, &status); err != nil {
		t.Fatal(err)
	}
	if !status.Initialized || !status.Unlocked || status.AutoLockMinutes != 0 {
		t.Fatalf("vault status = %+v, want initialized, unlocked, and no idle auto-lock from --master-key-file", status)
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

func TestServerProcessRetriesDeferredUpgradeAfterFirstAdmin(t *testing.T) {
	binary := buildServerBinary(t)
	dataDir := t.TempDir()
	database, err := store.Open(context.Background(), filepath.Join(dataDir, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{
		&store.AssetRow{ID: "legacy-asset", Kind: "ssh", Name: "legacy", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 1},
		&store.ConversationRow{ID: "legacy-conversation", ScopeJSON: "{}", CreatedAt: 1, UpdatedAt: 1},
		&store.CronJob{ID: "legacy-cron", SessionID: "legacy-conversation", Prompt: "synthetic-prompt", Schedule: "* * * * *", Timezone: "UTC", Enabled: false, TimeoutMS: 1000, CreatedAt: 1, UpdatedAt: 1, Revision: 1, NextRunAt: time.Now().Add(time.Hour).UnixMilli()},
	} {
		if err := database.GORM().Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	process, address := startServerProcess(t, binary, dataDir, false)
	defer process.stop(t)
	waitForHealth(t, process, address)
	session := initSuperadminThroughProcess(t, process, address, "alice", "alice-pw-123")

	upgraded, err := store.Open(context.Background(), filepath.Join(dataDir, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	for _, check := range []struct {
		model any
		id    string
	}{
		{&store.AssetRow{}, "legacy-asset"},
		{&store.ConversationRow{}, "legacy-conversation"},
		{&store.CronJob{}, "legacy-cron"},
	} {
		var ownerID string
		if err := upgraded.GORM().Model(check.model).Select("owner_id").Where("id = ?", check.id).Scan(&ownerID).Error; err != nil {
			t.Fatal(err)
		}
		if ownerID != session.userID {
			t.Fatalf("%T owner after real server init = %q, want %q", check.model, ownerID, session.userID)
		}
	}
	if output := process.output.String(); !strings.Contains(output, "data upgrade deferred") || !strings.Contains(output, "completed data upgrade") {
		t.Fatalf("real server deferred/retry logs = %s", output)
	}
}

func TestSyncOnlyProcessServesV2ObjectProtocol(t *testing.T) {
	binary := buildServerBinary(t)
	dataDir := t.TempDir()
	process, address := startServerProcess(t, binary, dataDir, true)
	defer process.stop(t)
	health := waitForHealth(t, process, address)
	if !health.OK || !health.SyncOnly || health.Commands != 3 || health.WebRoot != nil || health.Vault == nil || health.Retention == nil {
		t.Fatalf("sync-only health = %+v", health)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	requestURL(t, client, "http://"+address+"/rpc", http.MethodPost, strings.NewReader(`{"cmd":"app_platform","args":null}`), map[string]string{"Content-Type": "application/json"}, http.StatusNotFound)
	requestURL(t, client, "http://"+address+"/", http.MethodGet, nil, nil, http.StatusNotFound)
	requestURL(t, client, "http://"+address+"/sync/v2/push", http.MethodPost, strings.NewReader(`{"protocol":3,"known_head":"","objects":[]}`), map[string]string{"Content-Type": "application/json"}, http.StatusUnauthorized)

	session := initSuperadminThroughProcess(t, process, address, "alice", "alice-pw-123")
	push := requestAccountJSON(t, client, address, "/sync/v2/push", map[string]any{
		"protocol": 3, "known_head": genesisHeadForTest(session.userID), "objects": []map[string]any{},
	}, session, http.StatusOK)
	var pushed struct {
		Head    string `json:"head"`
		Applied int    `json:"applied"`
	}
	decodeAccountData(t, push, &pushed)
	if pushed.Head == "" {
		t.Fatalf("push response = %s", push.body)
	}
	pull := requestAccountJSON(t, client, address, "/sync/v2/pull", map[string]any{"protocol": 3, "since_seq": 0}, session, http.StatusOK)
	if !strings.Contains(string(pull.body), `"objects"`) {
		t.Fatalf("pull response = %s", pull.body)
	}
	ids := requestAccountJSON(t, client, address, "/sync/v2/ids", map[string]any{"protocol": 3}, session, http.StatusOK)
	if !strings.Contains(string(ids.body), `"entries"`) {
		t.Fatalf("ids response = %s", ids.body)
	}
	// Push requests must carry the CSRF header; rejected when missing.
	requestAccountJSON(t, client, address, "/sync/v2/push", map[string]any{
		"protocol": 3, "known_head": pushed.Head, "objects": []map[string]any{},
	}, &accountSession{cookie: session.cookie}, http.StatusForbidden)
}

// TestSyncOnlyProcessServesPeerBundleRPC guards the sync-only peer command
// surface contract: /sync/rpc mounts only sync_digest/sync_export/sync_import
// (health reports 3 accordingly), shares the account RPC session and CSRF
// checks - anonymous requests and CSRF-less session requests are rejected,
// and a fourth command gets not_found.
func TestSyncOnlyProcessServesPeerBundleRPC(t *testing.T) {
	binary := buildServerBinary(t)
	dataDir := t.TempDir()
	process, address := startServerProcess(t, binary, dataDir, true)
	defer process.stop(t)
	health := waitForHealth(t, process, address)
	if !health.OK || !health.SyncOnly || health.Commands != 3 {
		t.Fatalf("sync-only health = %+v", health)
	}
	client := &http.Client{Timeout: 3 * time.Second}

	requestRPC(t, client, address, "sync_digest", nil, http.StatusUnauthorized)

	session := initSuperadminThroughProcess(t, process, address, "alice", "alice-pw-123")
	requestAccountJSON(t, client, address, "/sync/rpc", map[string]any{
		"cmd": "sync_digest", "args": map[string]any{},
	}, &accountSession{cookie: session.cookie}, http.StatusForbidden)

	digest := requestAccountJSON(t, client, address, "/sync/rpc", map[string]any{
		"cmd": "sync_digest", "args": map[string]any{},
	}, session, http.StatusOK)
	var digestEnvelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Origin string `json:"origin"`
			Assets []any  `json:"assets"`
		} `json:"data"`
	}
	decodeAccountData(t, digest, &digestEnvelope)
	if !digestEnvelope.OK || digestEnvelope.Data.Origin != "server" {
		t.Fatalf("peer digest = %s", digest.body)
	}

	missing := requestAccountJSON(t, client, address, "/sync/rpc", map[string]any{
		"cmd": "app_info", "args": map[string]any{},
	}, session, http.StatusOK)
	var failure struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decodeAccountData(t, missing, &failure)
	if failure.OK || failure.Error.Code != "not_found" {
		t.Fatalf("app_info on peer surface = %s", missing.body)
	}
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
	var attached struct{ TabID string }
	decodeRPCData(t, response, &attached)
	if attached.TabID == "" {
		t.Fatal("terminal_attach returned no tab")
	}
	requestFullRPC(t, client, address, "terminal_resize", map[string]any{"tabId": attached.TabID, "cols": 100, "rows": 30})
	response = requestFullRPC(t, client, address, "terminal_resize_flush", map[string]any{"tabId": attached.TabID})
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
	if len(tabs) != 1 || tabs[0].TabID != attached.TabID || tabs[0].Cols != 100 || tabs[0].Rows != 30 {
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

// TestServerProcessLoopbackUninitializedLoginFree guards the M196 contract
// (M202): a fresh data dir with --auth=loopback is login-free before
// initialization - /auth/status reports loopback with initialized=false and
// /rpc works anonymously; the explicit init path remains, and after init the
// account routes still require a session (anonymous /auth/me is 401) while
// the mode still reports loopback.
func TestServerProcessLoopbackUninitializedLoginFree(t *testing.T) {
	binary := buildServerBinary(t)
	dataDir := t.TempDir()
	process, address := startServerProcess(t, binary, dataDir, false, "--auth=loopback")
	defer process.stop(t)
	waitForHealth(t, process, address)

	client := &http.Client{Timeout: 5 * time.Second}
	getStatus := func() (int, map[string]any) {
		t.Helper()
		request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+address+"/auth/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, body
	}

	code, body := getStatus()
	if code != http.StatusOK || body["initialized"] != false || body["auth"] != "loopback" {
		t.Fatalf("fresh loopback status=%d body=%v", code, body)
	}
	requestRPCPath(t, client, address, "/rpc", "app_platform", nil)
	requestURL(t, client, "http://"+address+"/auth/me", http.MethodGet, nil, nil, http.StatusUnauthorized)

	initSuperadminThroughProcess(t, process, address, "alice", "alice-pw-123")
	code, body = getStatus()
	if code != http.StatusOK || body["initialized"] != true || body["auth"] != "loopback" {
		t.Fatalf("initialized loopback status=%d body=%v", code, body)
	}
	requestURL(t, client, "http://"+address+"/auth/me", http.MethodGet, nil, nil, http.StatusUnauthorized)
}

func TestServerProcessRequireVault(t *testing.T) {
	binary := buildServerBinary(t)

	// Without a configured key, --require-vault reuses the auto-generated
	// master key from the data dir instead of refusing to start.
	generatedDir := t.TempDir()
	process, address := startServerProcess(t, binary, generatedDir, false, "--require-vault", "--auth=loopback")
	status := vaultStatusFromHealth(t, waitForHealth(t, process, address))
	if !status.Initialized || !status.Unlocked {
		t.Fatalf("require-vault without explicit key = %+v, want generated key bootstrap", status)
	}
	if _, err := os.Stat(filepath.Join(generatedDir, "master.key")); err != nil {
		t.Fatalf("require-vault did not generate master.key: %v", err)
	}
	process.stop(t)

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

	keyFile := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyFile, []byte("first-master-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	process, address = startServerProcess(t, binary, dataDir, false, "--require-vault", "--master-key-file", keyFile)
	status = vaultStatusFromHealth(t, waitForHealth(t, process, address))
	if !status.Initialized || !status.Unlocked {
		t.Fatalf("vault status = %+v", status)
	}
	process.stop(t)

	if err := os.WriteFile(keyFile, []byte("second-master-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, output := exitCode("--listen", "127.0.0.1:0", "--data-dir", dataDir, "--require-vault", "--master-key-file", keyFile)
	if code != 1 || !strings.Contains(output, "vault") {
		t.Fatalf("require-vault with wrong key = code %d, output %q", code, output)
	}

	process, address = startServerProcess(t, binary, dataDir, false, "--master-key-file", keyFile)
	defer process.stop(t)
	status = vaultStatusFromHealth(t, waitForHealth(t, process, address))
	if !status.Initialized || status.Unlocked {
		t.Fatalf("wrong key without require-vault = %+v, want locked vault still serving", status)
	}
}

// TestServerProcessAutoGeneratesMasterKey guards the out-of-box contract
// without an explicit key: first start generates a 0600 64-char hex
// master.key in the data dir and initializes/unlocks the credential store;
// restart reuses the same key (no regeneration) and old ciphertext stays
// decryptable.
func TestServerProcessAutoGeneratesMasterKey(t *testing.T) {
	binary := buildServerBinary(t)
	dataDir := t.TempDir()
	keyPath := filepath.Join(dataDir, "master.key")
	readKey := func() string {
		t.Helper()
		data, err := os.ReadFile(keyPath)
		if err != nil {
			t.Fatal(err)
		}
		key := strings.TrimSpace(string(data))
		if len(key) != 64 {
			t.Fatalf("generated master key length = %d, want 64 hex chars", len(key))
		}
		if _, err := hex.DecodeString(key); err != nil {
			t.Fatalf("generated master key is not hex: %v", err)
		}
		return key
	}

	process, address := startServerProcess(t, binary, dataDir, false, "--auth=loopback")
	status := vaultStatusFromHealth(t, waitForHealth(t, process, address))
	if !status.Initialized || !status.Unlocked {
		t.Fatalf("vault status with generated key = %+v", status)
	}
	firstKey := readKey()
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("master.key mode = %04o, want 0600", info.Mode().Perm())
	}
	client := &http.Client{Timeout: 5 * time.Second}
	created := requestFullRPC(t, client, address, "vault_set_credential", map[string]any{"args": map[string]any{"name": "generated", "kind": "password", "secret": "s3cret"}})
	var createdData map[string]string
	decodeRPCData(t, created, &createdData)
	if createdData["id"] == "" {
		t.Fatalf("vault_set_credential data = %+v", createdData)
	}
	process.stop(t)

	process, address = startServerProcess(t, binary, dataDir, false, "--auth=loopback")
	defer process.stop(t)
	status = vaultStatusFromHealth(t, waitForHealth(t, process, address))
	if !status.Initialized || !status.Unlocked {
		t.Fatalf("vault status after restart = %+v, want reused key unlock", status)
	}
	if key := readKey(); key != firstKey {
		t.Fatal("master.key was regenerated on restart")
	}
	revealed := requestFullRPC(t, client, address, "vault_reveal_credential", map[string]any{"id": createdData["id"]})
	var revealedData struct {
		Value string `json:"value"`
	}
	decodeRPCData(t, revealed, &revealedData)
	if revealedData.Value != "s3cret" {
		t.Fatalf("credential after restart = %q", revealedData.Value)
	}
}

func vaultStatusFromHealth(t *testing.T, health core.Health) vault.Status {
	t.Helper()
	if health.Vault == nil {
		t.Fatal("health has no vault status")
	}
	encoded, err := json.Marshal(health.Vault)
	if err != nil {
		t.Fatal(err)
	}
	var status vault.Status
	if err := json.Unmarshal(encoded, &status); err != nil {
		t.Fatal(err)
	}
	return status
}

func parseSystemdUnit(t *testing.T, path string) map[string]map[string][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	unit := make(map[string]map[string][]string)
	section := ""
	for index := 0; index < len(lines); index++ {
		line := strings.TrimSpace(lines[index])
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		for strings.HasSuffix(line, `\`) {
			line = strings.TrimSpace(strings.TrimSuffix(line, `\`))
			index++
			if index >= len(lines) {
				t.Fatalf("%s: unterminated continuation", path)
			}
			line += " " + strings.TrimSpace(lines[index])
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			if _, ok := unit[section]; !ok {
				unit[section] = make(map[string][]string)
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section == "" {
			t.Fatalf("%s: invalid unit entry %q", path, line)
		}
		key = strings.TrimSpace(key)
		unit[section][key] = append(unit[section][key], strings.TrimSpace(value))
	}
	return unit
}

func systemdValue(t *testing.T, unit map[string]map[string][]string, section, key string) string {
	t.Helper()
	values := unit[section][key]
	if len(values) != 1 {
		t.Fatalf("[%s] %s has %d values, want 1", section, key, len(values))
	}
	return values[0]
}

func applyEnvironment(t *testing.T, environment map[string]string, assignments string) {
	t.Helper()
	for _, assignment := range strings.Fields(assignments) {
		key, value, ok := strings.Cut(assignment, "=")
		if !ok || key == "" {
			t.Fatalf("invalid environment assignment %q", assignment)
		}
		environment[key] = value
	}
}

func environmentExample(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	environment := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		applyEnvironment(t, environment, line)
	}
	return environment
}

func TestSystemdUnitsDefaultToAuthOn(t *testing.T) {
	for _, test := range []struct {
		unit            string
		environmentFile string
		example         string
		syncOnly        bool
	}{
		{unit: "nexterm-server.service", environmentFile: "/etc/nexterm/nexterm.env", example: "nexterm.env.example"},
		{unit: "nexterm-onlyserver.service", environmentFile: "/etc/nexterm/onlyserver.env", example: "onlyserver.env.example", syncOnly: true},
	} {
		t.Run(test.unit, func(t *testing.T) {
			root := filepath.Join("..", "..", "deploy", "systemd")
			unit := parseSystemdUnit(t, filepath.Join(root, test.unit))
			environment := make(map[string]string)
			for _, assignments := range unit["Service"]["Environment"] {
				applyEnvironment(t, environment, assignments)
			}
			environmentFile := strings.TrimPrefix(systemdValue(t, unit, "Service", "EnvironmentFile"), "-")
			if environmentFile != test.environmentFile {
				t.Fatalf("EnvironmentFile = %q, want %q", environmentFile, test.environmentFile)
			}
			for key, value := range environmentExample(t, filepath.Join(root, test.example)) {
				environment[key] = value
			}
			execStart := strings.Fields(systemdValue(t, unit, "Service", "ExecStart"))
			if len(execStart) == 0 {
				t.Fatal("ExecStart is empty")
			}
			invocation, err := core.ParseCLI(execStart[1:], core.CommandServe, func(key string) string {
				return environment[key]
			})
			if err != nil {
				t.Fatal(err)
			}
			if invocation.Auth != core.AuthOn {
				t.Fatalf("effective auth = %q, want %q", invocation.Auth, core.AuthOn)
			}
			if invocation.SyncOnly != test.syncOnly {
				t.Fatalf("sync-only = %v, want %v", invocation.SyncOnly, test.syncOnly)
			}
		})
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
	start := func(t *testing.T, listen string, syncOnly bool, extraArgs ...string) (*testServerProcess, string) {
		t.Helper()
		dataDir := t.TempDir()
		args := []string{"--listen", listen, "--data-dir", dataDir}
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

	// The exposure warning must reflect the auth mode honestly: auth=off must
	// not claim access control is enabled; on/loopback (non-loopback listen
	// counts as on) keep the account-session wording.
	for _, tc := range []struct {
		name     string
		syncOnly bool
		auth     string
		detail   string
		absent   string
	}{
		{"full-auth-off", false, "--auth=off", "访问控制已关闭（--auth=off）", "已启用访问控制"},
		{"sync-only-auth-off", true, "--auth=off", "onlyServer 模式（--auth=off）", "持有账号会话的人可以同步"},
		{"full-auth-on", false, "--auth=on", "完整版已启用访问控制", "访问控制已关闭"},
		{"full-auth-loopback", false, "--auth=loopback", "完整版已启用访问控制", "访问控制已关闭"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			healthAddress := loopbackAddress(t)
			listen := strings.Replace(healthAddress, "127.0.0.1", "0.0.0.0", 1)
			process, _ := start(t, listen, tc.syncOnly, tc.auth)
			defer process.stop(t)
			waitForHealth(t, process, healthAddress)
			output := process.output.String()
			if !strings.Contains(output, "WARNING: NexTerm is listening on non-loopback address "+listen) || !strings.Contains(output, tc.detail) {
				t.Fatalf("exposure warning missing %q with %s: %q", tc.detail, tc.auth, output)
			}
			if strings.Contains(output, tc.absent) {
				t.Fatalf("wording %q must not appear with %s: %q", tc.absent, tc.auth, output)
			}
		})
	}
}

var serverBinaryBuild sync.Once
var serverBinaryPath string
var serverBinaryBuildErr error

func TestMain(m *testing.M) {
	code := m.Run()
	if serverBinaryPath != "" {
		_ = os.RemoveAll(filepath.Dir(serverBinaryPath))
	}
	os.Exit(code)
}

func buildServerBinary(t *testing.T) string {
	t.Helper()
	serverBinaryBuild.Do(func() {
		directory, err := os.MkdirTemp("", "nexterm-server-test-")
		if err != nil {
			serverBinaryBuildErr = err
			return
		}
		serverBinaryPath = filepath.Join(directory, "nexterm-server")
		cmd := exec.Command("go", "build", "-mod=readonly", "-o", serverBinaryPath, ".")
		if output, err := cmd.CombinedOutput(); err != nil {
			serverBinaryBuildErr = fmt.Errorf("build server: %w\n%s", err, output)
		}
	})
	if serverBinaryBuildErr != nil {
		t.Fatal(serverBinaryBuildErr)
	}
	return serverBinaryPath
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

// initSuperadminThroughProcess completes superadmin init and login via the
// console init code, returning the session cookie and CSRF token.
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

// --- init-admin: installer super-admin bootstrap (DEP01) ---

func initAdminTestGetenv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func openInitAdminTestStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(dir, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func parseInitAdminPassword(t *testing.T, output string) string {
	t.Helper()
	index := strings.Index(output, initAdminPasswordMarker)
	if index < 0 {
		t.Fatalf("initial password was not printed to the console: %q", output)
	}
	password := output[index+len(initAdminPasswordMarker):]
	if end := strings.Index(password, "（"); end >= 0 {
		password = password[:end]
	}
	password = strings.TrimSpace(password)
	if password == "" || strings.ContainsAny(password, " \n\t") {
		t.Fatalf("invalid initial password format: %q", password)
	}
	return password
}

func TestInitAdminRandomPasswordFirstInstall(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := runInitAdmin([]string{"--data-dir", dir}, initAdminTestGetenv(nil), &stdout, &stderr); code != 0 {
		t.Fatalf("init-admin exit = %d, stderr: %s", code, stderr.String())
	}
	password := parseInitAdminPassword(t, stdout.String())
	if strings.Contains(stderr.String(), password) {
		t.Fatal("initial password leaked to stderr")
	}
	accounts := account.New(openInitAdminTestStore(t, dir).GORM())
	user, err := accounts.Authenticate(context.Background(), "admin", password)
	if err != nil {
		t.Fatalf("printed password rejected: %v", err)
	}
	if user.Role != account.RoleSuperadmin || user.State != account.StateResetRequired || !user.MustChangePassword {
		t.Fatalf("random first-install policy must force a first-login change: %+v", user)
	}
	// The forced first-login change works with freshly generated envelopes,
	// the same flow the browser uses after an admin reset.
	_, envelopes, _, err := vault.GenerateUserDEKEnvelopes("synthetic-password-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.ChangePassword(context.Background(), user.ID, password, "synthetic-password-2", envelopes, ""); err != nil {
		t.Fatalf("forced first-login change rejected: %v", err)
	}
	after, err := accounts.Authenticate(context.Background(), "admin", "synthetic-password-2")
	if err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
	if after.State != account.StateActive || after.MustChangePassword {
		t.Fatalf("first-login change did not clear the forced-change state: %+v", after)
	}
}

func TestInitAdminSuppliedPasswordNoForcedRotation(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	getenv := initAdminTestGetenv(map[string]string{"NEXTERM_ADMIN_PASSWORD": "synthetic-password-1"})
	if code := runInitAdmin([]string{"--data-dir", dir}, getenv, &stdout, &stderr); code != 0 {
		t.Fatalf("init-admin exit = %d, stderr: %s", code, stderr.String())
	}
	if password := parseInitAdminPassword(t, stdout.String()); password != "synthetic-password-1" {
		t.Fatalf("printed password = %q, want the supplied one", password)
	}
	if strings.Contains(stderr.String(), "synthetic-password-1") {
		t.Fatal("supplied password leaked to stderr")
	}
	accounts := account.New(openInitAdminTestStore(t, dir).GORM())
	user, err := accounts.Authenticate(context.Background(), "admin", "synthetic-password-1")
	if err != nil {
		t.Fatalf("supplied password rejected: %v", err)
	}
	if user.State != account.StateActive || user.MustChangePassword {
		t.Fatalf("supplied first-install policy must not force rotation: %+v", user)
	}
}

func TestInitAdminExistingAccountPrintsNothing(t *testing.T) {
	dir := t.TempDir()
	seed := account.New(openInitAdminTestStore(t, dir).GORM())
	if _, err := seed.CreateUser(context.Background(), "existing", "", "synthetic-password-1"); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	getenv := initAdminTestGetenv(map[string]string{"NEXTERM_ADMIN_PASSWORD": "synthetic-password-2"})
	if code := runInitAdmin([]string{"--data-dir", dir}, getenv, &stdout, &stderr); code != initAdminExitUnavailable {
		t.Fatalf("init-admin exit = %d, want %d (stderr: %s)", code, initAdminExitUnavailable, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("existing-account run must not print to stdout: %q", stdout.String())
	}
	if strings.Contains(stderr.String(), "synthetic-password-2") {
		t.Fatal("supplied password leaked on the existing-account path")
	}
	accounts := account.New(openInitAdminTestStore(t, dir).GORM())
	if _, err := accounts.Authenticate(context.Background(), "admin", "synthetic-password-2"); err == nil {
		t.Fatal("a second superadmin was created over existing accounts")
	}
}

func TestInitAdminFailedCreateRetrySucceeds(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	short := initAdminTestGetenv(map[string]string{"NEXTERM_ADMIN_PASSWORD": "short"})
	if code := runInitAdmin([]string{"--data-dir", dir}, short, &stdout, &stderr); code != 1 {
		t.Fatalf("short supplied password must fail creation, exit = %d", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("failed creation must not print a password: %q", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	valid := initAdminTestGetenv(map[string]string{"NEXTERM_ADMIN_PASSWORD": "synthetic-password-1"})
	if code := runInitAdmin([]string{"--data-dir", dir}, valid, &stdout, &stderr); code != 0 {
		t.Fatalf("retry after a failed create must succeed, exit = %d (stderr: %s)", code, stderr.String())
	}
	if password := parseInitAdminPassword(t, stdout.String()); password != "synthetic-password-1" {
		t.Fatalf("retry printed password = %q", password)
	}
	accounts := account.New(openInitAdminTestStore(t, dir).GORM())
	if _, err := accounts.Authenticate(context.Background(), "admin", "synthetic-password-1"); err != nil {
		t.Fatalf("retry password rejected: %v", err)
	}
}

func TestInitAdminFailureOutputRedactsPassword(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	getenv := initAdminTestGetenv(map[string]string{"NEXTERM_ADMIN_PASSWORD": "synthetic-password-1"})
	if code := runInitAdmin([]string{"--data-dir", dir, "--username", "bad name"}, getenv, &stdout, &stderr); code != 1 {
		t.Fatalf("invalid username must fail creation, exit = %d", code)
	}
	combined := stdout.String() + stderr.String()
	if strings.Contains(combined, "synthetic-password-1") {
		t.Fatalf("supplied password reached the failure output: %q", combined)
	}
	if stdout.Len() != 0 {
		t.Fatalf("failed creation must not print to stdout: %q", stdout.String())
	}
}

func TestInitAdminKeepsAlreadyIssuedInitCode(t *testing.T) {
	dir := t.TempDir()
	seed := account.New(openInitAdminTestStore(t, dir).GORM())
	code, err := seed.GenerateInitCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if got := runInitAdmin([]string{"--data-dir", dir}, initAdminTestGetenv(nil), &stdout, &stderr); got != initAdminExitCodeIssued {
		t.Fatalf("init-admin exit = %d, want %d (stderr: %s)", got, initAdminExitCodeIssued, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("code-issued run must not print to stdout: %q", stdout.String())
	}
	accounts := account.New(openInitAdminTestStore(t, dir).GORM())
	if _, err := accounts.InitSuperadmin(context.Background(), code, "root", "synthetic-password-1"); err != nil {
		t.Fatalf("the already-issued init code must stay usable after a bootstrap attempt: %v", err)
	}
}

func TestInitAdminSkipsPlatformManagedAuth(t *testing.T) {
	for _, auth := range []string{"off", "platform"} {
		dir := t.TempDir()
		var stdout, stderr bytes.Buffer
		getenv := initAdminTestGetenv(map[string]string{"NEXTERM_AUTH": auth})
		if code := runInitAdmin([]string{"--data-dir", dir}, getenv, &stdout, &stderr); code != initAdminExitUnavailable {
			t.Fatalf("auth=%s: exit = %d, want %d", auth, code, initAdminExitUnavailable)
		}
		if stdout.Len() != 0 {
			t.Fatalf("auth=%s: must not print to stdout: %q", auth, stdout.String())
		}
		if _, err := os.Stat(filepath.Join(dir, "data.db")); !os.IsNotExist(err) {
			t.Fatalf("auth=%s: the store must not be opened, data.db stat: %v", auth, err)
		}
	}
}

func TestInitAdminHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runInitAdmin([]string{"--help"}, initAdminTestGetenv(nil), &stdout, &stderr); code != 0 {
		t.Fatalf("--help exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "init-admin") || !strings.Contains(stdout.String(), "NEXTERM_ADMIN_PASSWORD") {
		t.Fatalf("usage must document the command and the injection variable: %q", stdout.String())
	}
}

func initAdminProcessEnv(extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra))
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "NEXTERM_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, extra...)
}

func TestInitAdminBinaryEndToEnd(t *testing.T) {
	binary := buildServerBinary(t)
	ctx := context.Background()

	randomDir := t.TempDir()
	run := func(dir string, extraEnv []string, extraArgs ...string) (string, string, int) {
		t.Helper()
		args := append([]string{"init-admin", "--data-dir", dir}, extraArgs...)
		command := exec.Command(binary, args...)
		command.Env = initAdminProcessEnv(extraEnv...)
		var stdout, stderr bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = &stderr
		err := command.Run()
		code := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else if err != nil {
			t.Fatalf("run init-admin: %v", err)
		}
		return stdout.String(), stderr.String(), code
	}

	stdout, stderr, code := run(randomDir, nil)
	if code != 0 {
		t.Fatalf("first install exit = %d, stderr: %s", code, stderr)
	}
	password := parseInitAdminPassword(t, stdout)
	if strings.Contains(stderr, password) {
		t.Fatal("initial password leaked to stderr")
	}
	accounts := account.New(openInitAdminTestStore(t, randomDir).GORM())
	if _, err := accounts.Authenticate(ctx, "admin", password); err != nil {
		t.Fatalf("printed random password rejected: %v", err)
	}

	stdout, stderr, code = run(randomDir, nil)
	if code != initAdminExitUnavailable {
		t.Fatalf("re-run exit = %d, want %d", code, initAdminExitUnavailable)
	}
	if stdout != "" {
		t.Fatalf("re-run must stay silent on stdout: %q", stdout)
	}

	suppliedDir := t.TempDir()
	stdout, _, code = run(suppliedDir, []string{"NEXTERM_ADMIN_PASSWORD=short"})
	if code != 1 || stdout != "" {
		t.Fatalf("failed create: exit = %d stdout = %q", code, stdout)
	}
	stdout, stderr, code = run(suppliedDir, []string{"NEXTERM_ADMIN_PASSWORD=synthetic-password-1"})
	if code != 0 {
		t.Fatalf("retry exit = %d, stderr: %s", code, stderr)
	}
	if printed := parseInitAdminPassword(t, stdout); printed != "synthetic-password-1" {
		t.Fatalf("retry printed password = %q", printed)
	}
	supplied := account.New(openInitAdminTestStore(t, suppliedDir).GORM())
	if _, err := supplied.Authenticate(ctx, "admin", "synthetic-password-1"); err != nil {
		t.Fatalf("supplied password rejected: %v", err)
	}

	redactDir := t.TempDir()
	stdout, stderr, code = run(redactDir, []string{"NEXTERM_ADMIN_PASSWORD=synthetic-password-1"}, "--username", "bad name")
	if code != 1 {
		t.Fatalf("invalid username exit = %d, want 1", code)
	}
	if combined := stdout + stderr; strings.Contains(combined, "synthetic-password-1") {
		t.Fatalf("supplied password reached the failure output: %q", combined)
	}
}

type initAdminLoginView struct {
	State              string `json:"state"`
	MustChangePassword bool   `json:"must_change_password"`
}

func loginInitAdminSession(t *testing.T, client *http.Client, address, username, password string) (*accountSession, initAdminLoginView) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"username": username, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "http://"+address+"/auth/login", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("login status = %d: %s", response.StatusCode, body)
	}
	var body struct {
		User struct {
			ID string `json:"id"`
			initAdminLoginView
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
		t.Fatalf("login session incomplete: %+v", session)
	}
	return session, body.User.initAdminLoginView
}

func getInitAdminPath(t *testing.T, client *http.Client, address, path string, session *accountSession, expectedStatus int) map[string]any {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+address+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(session.cookie)
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
	var decoded map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
	return decoded
}

// TestInitAdminHTTPEndToEnd drives the installer-created admin through the
// real HTTP surface: the random policy lands in reset_required with the API
// locked down until the forced password change (fresh envelopes, the browser
// flow), while the supplied policy logs in active with no forced rotation and
// no stored DEK.
func TestInitAdminHTTPEndToEnd(t *testing.T) {
	binary := buildServerBinary(t)
	client := &http.Client{Timeout: 5 * time.Second}

	randomDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := runInitAdmin([]string{"--data-dir", randomDir}, initAdminTestGetenv(nil), &stdout, &stderr); code != 0 {
		t.Fatalf("init-admin exit = %d, stderr: %s", code, stderr.String())
	}
	password := parseInitAdminPassword(t, stdout.String())
	process, address := startServerProcess(t, binary, randomDir, false, "--auth=on")
	defer process.stop(t)
	waitForHealth(t, process, address)

	session, view := loginInitAdminSession(t, client, address, "admin", password)
	if view.State != string(account.StateResetRequired) || !view.MustChangePassword {
		t.Fatalf("random-policy login view: %+v", view)
	}
	getInitAdminPath(t, client, address, "/auth/devices", session, http.StatusForbidden)

	_, envelopes, _, err := vault.GenerateUserDEKEnvelopes("synthetic-password-2")
	if err != nil {
		t.Fatal(err)
	}
	requestAccountJSON(t, client, address, "/auth/password", map[string]any{
		"old_password": password, "new_password": "synthetic-password-2",
		"dek_envelope": envelopes.DEKEnvelope, "kdf_salt": envelopes.KDFSalt, "kdf_params": envelopes.KDFParams,
		"recovery_envelope": envelopes.RecoveryEnvelope, "recovery_hash": envelopes.RecoveryHash,
	}, session, http.StatusOK)
	me := getInitAdminPath(t, client, address, "/auth/me", session, http.StatusOK)
	user := me["user"].(map[string]any)
	if user["state"] != string(account.StateActive) || user["must_change_password"] != false {
		t.Fatalf("post-change user: %v", user)
	}
	getInitAdminPath(t, client, address, "/auth/dek", session, http.StatusOK)
	getInitAdminPath(t, client, address, "/auth/devices", session, http.StatusOK)

	suppliedDir := t.TempDir()
	stdout.Reset()
	stderr.Reset()
	getenv := initAdminTestGetenv(map[string]string{"NEXTERM_ADMIN_PASSWORD": "synthetic-password-1"})
	if code := runInitAdmin([]string{"--data-dir", suppliedDir}, getenv, &stdout, &stderr); code != 0 {
		t.Fatalf("init-admin exit = %d, stderr: %s", code, stderr.String())
	}
	suppliedProcess, suppliedAddress := startServerProcess(t, binary, suppliedDir, false, "--auth=on")
	defer suppliedProcess.stop(t)
	waitForHealth(t, suppliedProcess, suppliedAddress)
	suppliedSession, suppliedView := loginInitAdminSession(t, client, suppliedAddress, "admin", "synthetic-password-1")
	if suppliedView.State != string(account.StateActive) || suppliedView.MustChangePassword {
		t.Fatalf("supplied-policy login view: %+v", suppliedView)
	}
	getInitAdminPath(t, client, suppliedAddress, "/auth/dek", suppliedSession, http.StatusNotFound)
}
