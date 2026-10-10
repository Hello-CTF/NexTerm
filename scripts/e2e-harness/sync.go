package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	_ "modernc.org/sqlite"
)

const genesisPrefix = "nexterm/go/sync-head/v1"

type syncReport struct {
	SchemaVersion int          `json:"schema_version"`
	Mode          string       `json:"mode"`
	Status        string       `json:"status"`
	Passed        int          `json:"passed"`
	Failed        int          `json:"failed"`
	Checks        []checkEntry `json:"checks"`
	HarnessErrors []string     `json:"harness_errors"`
	Binary        string       `json:"binary"`
	Helper        string       `json:"helper"`
	StartedAt     string       `json:"started_at"`
	FinishedAt    string       `json:"finished_at"`
	DurationSec   float64      `json:"duration_seconds"`
	Log           string       `json:"log"`
	SkipAsPass    bool         `json:"skip_as_pass"`
}

func genesisHead(userID string) string {
	sum := sha256.Sum256([]byte(genesisPrefix + "\x00" + userID))
	return hex.EncodeToString(sum[:])
}

func syntheticID() string {
	return "e2e" + randomHex(12)[:23]
}

func runSync(h *harness) int {
	started := time.Now()
	startedAt := started.UTC().Format("2006-01-02T15:04:05+00:00")
	harnessErrors := []string{}
	reportDir := filepath.Join(h.root, "target", "e2e-sync")
	var server *instance
	work := ""
	func() {
		defer func() {
			if r := recover(); r != nil {
				message := fmt.Sprintf("run: panic: %v", r)
				harnessErrors = append(harnessErrors, message)
				h.check("e2e 执行异常", false, message)
			}
		}()
		if err := h.prepare(); err != nil {
			harnessErrors = append(harnessErrors, "build: "+err.Error())
			h.check("e2e 执行异常", false, err.Error())
			return
		}
		var err error
		server, work, err = syncBody(h)
		if err != nil {
			harnessErrors = append(harnessErrors, "run: "+err.Error())
			h.check("e2e 执行异常", false, err.Error())
		}
	}()
	if server != nil {
		server.stop()
		if err := os.MkdirAll(reportDir, 0o755); err == nil {
			if raw, err := os.ReadFile(server.logPath); err == nil {
				os.WriteFile(filepath.Join(reportDir, "server.log"), raw, 0o644)
			}
		}
	}
	if work != "" {
		os.RemoveAll(work)
	}
	passed := len(h.checks) - h.failed
	status := "passed"
	if h.failed > 0 || len(harnessErrors) > 0 {
		status = "failed"
	}
	report := syncReport{
		SchemaVersion: 1,
		Mode:          "sync-v2-local",
		Status:        status,
		Passed:        passed,
		Failed:        h.failed,
		Checks:        h.checks,
		HarnessErrors: harnessErrors,
		Binary:        h.bin,
		Helper:        h.helper,
		StartedAt:     startedAt,
		FinishedAt:    time.Now().UTC().Format("2006-01-02T15:04:05+00:00"),
		DurationSec:   float64(time.Since(started).Milliseconds()) / 1000,
		Log:           "target/e2e-sync/server.log",
		SkipAsPass:    false,
	}
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		fatalf("%v", err)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fatalf("%v", err)
	}
	temporary := filepath.Join(reportDir, "report.json.tmp")
	if err := os.WriteFile(temporary, append(encoded, '\n'), 0o644); err != nil {
		fatalf("%v", err)
	}
	reportPath := filepath.Join(reportDir, "report.json")
	if err := os.Rename(temporary, reportPath); err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("\n通过 %d 项, 失败 %d 项\n", passed, h.failed)
	fmt.Printf("报告: %s\n", reportPath)
	if h.failed > 0 {
		for _, entry := range h.checks {
			if entry.Status != "passed" {
				fmt.Printf("  FAILED: %s\n", entry.Name)
			}
		}
		return 1
	}
	return 0
}

type syncObject struct {
	id   string
	kind string
	body map[string]interface{}
}

func syncBody(h *harness) (*instance, string, error) {
	work, err := os.MkdirTemp("", "nexterm-sync-v2-e2e-")
	if err != nil {
		return nil, "", err
	}
	masterKey := "e2e-master-" + randomHex(16)
	server, err := newInstance(h, work, "server", masterKey, "on", "")
	if err != nil {
		return nil, work, err
	}
	if err := server.start(); err != nil {
		return server, work, err
	}
	fmt.Println("== 账号初始化 ==")
	initCode, err := server.initCode()
	if err != nil {
		return server, work, err
	}
	alicePassword := "alice-e2e-pw-123"
	bobPassword := "bob-e2e-pw-1234"
	out, err := h.helperRun("gen-dek", alicePassword)
	if err != nil {
		return server, work, err
	}
	aliceBundle := map[string]interface{}{}
	if err := json.Unmarshal([]byte(out), &aliceBundle); err != nil {
		return server, work, err
	}
	status, body, err := newClient(server.port).request("POST", "/auth/init", map[string]interface{}{
		"code":              initCode,
		"username":          "alice",
		"password":          alicePassword,
		"dek_envelope":      aliceBundle["dek_envelope"],
		"kdf_salt":          aliceBundle["kdf_salt"],
		"kdf_params":        aliceBundle["kdf_params"],
		"recovery_envelope": aliceBundle["recovery_envelope"],
		"recovery_hash":     aliceBundle["recovery_hash"],
	}, false)
	if err != nil {
		return server, work, err
	}
	h.check("超管初始化", status == 200 && getStr(getMap(body, "user"), "username") == "alice", fmt.Sprintf("HTTP %d %v", status, body))
	alice := newClient(server.port)
	if err := alice.login("alice", alicePassword); err != nil {
		return server, work, err
	}
	if err := alice.me(); err != nil {
		return server, work, err
	}
	h.check("登录并获取用户 ID", alice.userID != "", fmt.Sprintf("user_id=%s", alice.userID))
	aliceDEK := getStr(aliceBundle, "dek")

	fmt.Println("== 设备一推送密文对象 ==")
	now := time.Now().UnixMilli()
	groupID := syntheticID()
	assetID := syntheticID()
	credentialID := syntheticID()
	snippetID := syntheticID()
	knownHostID := syntheticID()
	aiProfileID := syntheticID()
	payloads := []syncObject{
		{groupID, "group", map[string]interface{}{
			"id": groupID, "name": "E2E 生产-canary-组", "sort": 0, "createdAt": now, "updatedAt": now}},
		{credentialID, "credential", map[string]interface{}{
			"id": credentialID, "name": "e2e-canary-凭据", "kind": "password", "secret": "e2e-canary-secret-值", "updatedAt": now}},
		{snippetID, "snippet", map[string]interface{}{
			"id": snippetID, "name": "e2e-canary-片段", "body": "echo e2e-canary", "sort": 0, "createdAt": now, "updatedAt": now}},
		{assetID, "asset", map[string]interface{}{
			"id": assetID, "groupId": groupID, "kind": "ssh", "name": "e2e-canary-资产", "host": "192.0.2.10",
			"port": 2222, "username": "root", "credId": credentialID, "optionsJson": "{}", "tags": "", "note": "",
			"sort": 0, "createdAt": now, "updatedAt": now}},
		{knownHostID, "known_host", map[string]interface{}{
			"id": knownHostID, "host": "e2e-canary-host.internal", "port": 22, "keyType": "ssh-ed25519",
			"fingerprint": "SHA256:e2e-canary-fingerprint-值", "addedAt": now}},
		{aiProfileID, "ai_profile", map[string]interface{}{
			"id": aiProfileID, "name": "e2e-canary-档案", "baseUrl": "https://e2e-canary-ai.example.com",
			"apiKey": "e2e-canary-api-key-值", "model": "e2e-model", "fallbackModel": "e2e-fallback",
			"temperature": 0.5, "contextWindow": 64000, "maxTokens": 2048, "proxy": nil, "stream": true,
			"requestTimeoutSeconds": 60, "idleTimeoutSeconds": 15, "circuitFailureThreshold": 3,
			"circuitCooldownSeconds": 30, "updatedAt": now}},
	}
	objects := []map[string]interface{}{}
	for _, item := range payloads {
		raw, err := json.Marshal(item.body)
		if err != nil {
			return server, work, err
		}
		blob, err := h.helperRunStdin(string(raw), "seal", aliceDEK, item.id, item.kind)
		if err != nil {
			return server, work, err
		}
		objects = append(objects, map[string]interface{}{"id": item.id, "blob": blob})
	}
	status, body, err = alice.request("POST", "/sync/v2/push", map[string]interface{}{
		"protocol": 2, "known_head": genesisHead(alice.userID), "objects": objects,
	}, true)
	if err != nil {
		return server, work, err
	}
	h.check("设备一首批推送", status == 200 && getFloat(body, "applied") == 6, fmt.Sprintf("HTTP %d %v", status, body))
	head := getStr(body, "head")

	fmt.Println("== 服务端密文与盲存检查 ==")
	canaries := []string{
		"E2E 生产-canary-组", "e2e-canary-凭据", "e2e-canary-secret-值", "e2e-canary-片段", "e2e-canary-资产",
		"192.0.2.10", "e2e-canary-host.internal", "e2e-canary-fingerprint-值", "e2e-canary-档案", "e2e-canary-api-key-值",
	}
	db, err := sql.Open("sqlite", filepath.Join(server.dataDir, "data.db"))
	if err != nil {
		return server, work, err
	}
	rows, err := db.Query("SELECT id, seq, blob FROM user_sync_object ORDER BY seq")
	if err != nil {
		db.Close()
		return server, work, err
	}
	blobs := map[string][]byte{}
	var seqs []int64
	for rows.Next() {
		var id string
		var seq int64
		var blob []byte
		if err := rows.Scan(&id, &seq, &blob); err != nil {
			rows.Close()
			db.Close()
			return server, work, err
		}
		blobs[id] = blob
		seqs = append(seqs, seq)
	}
	rows.Close()
	h.check("服务端对象行数", len(blobs) == 6, fmt.Sprintf("rows=%d", len(blobs)))
	var leaks []string
	for _, canary := range canaries {
		for _, blob := range blobs {
			if bytes.Contains(blob, []byte(canary)) {
				leaks = append(leaks, canary)
			}
		}
	}
	h.check("服务端行不含明文", len(leaks) == 0, fmt.Sprintf("leaks=%v", leaks))
	seqSorted := slices.IsSorted(seqs)
	distinct := map[int64]bool{}
	for _, seq := range seqs {
		distinct[seq] = true
	}
	h.check("seq 单调连续", seqSorted && len(distinct) == 6, fmt.Sprintf("seqs=%v", seqs))
	var headRow string
	headErr := db.QueryRow("SELECT head_hash FROM user_sync_head WHERE user_id = ?", alice.userID).Scan(&headRow)
	h.check("head 行存在且为哈希", headErr == nil && headRow == head && len(headRow) == 64, fmt.Sprintf("head_row=%q", headRow))
	db.Close()

	fmt.Println("== 幂等重推 ==")
	status, body, err = alice.request("POST", "/sync/v2/push", map[string]interface{}{
		"protocol": 2, "known_head": head, "objects": objects,
	}, true)
	if err != nil {
		return server, work, err
	}
	h.check("重复推送幂等", status == 200 && getFloat(body, "applied") == 0 && getFloat(body, "skipped") == 6 &&
		getStr(body, "head") == head, fmt.Sprintf("HTTP %d %v", status, body))

	fmt.Println("== 设备二拉取与端到端解密 ==")
	device2 := newClient(server.port)
	if err := device2.login("alice", alicePassword); err != nil {
		return server, work, err
	}
	if err := device2.me(); err != nil {
		return server, work, err
	}
	status, body, err = device2.request("POST", "/sync/v2/pull", map[string]interface{}{"protocol": 2, "since_seq": 0}, false)
	if err != nil {
		return server, work, err
	}
	pulled := getList(body, "objects")
	h.check("设备二全量拉取", status == 200 && len(pulled) == 6, fmt.Sprintf("HTTP %d %v", status, body))
	roundtripOK := true
	for _, item := range payloads {
		var entry map[string]interface{}
		for _, candidate := range pulled {
			if row, ok := candidate.(map[string]interface{}); ok && getStr(row, "id") == item.id {
				entry = row
			}
		}
		if entry == nil {
			roundtripOK = false
			continue
		}
		opened, err := h.helperRunStdin(getStr(entry, "blob"), "open", aliceDEK, item.id, item.kind)
		if err != nil {
			roundtripOK = false
			continue
		}
		var openedPayload, originalPayload interface{}
		if err := json.Unmarshal([]byte(opened), &openedPayload); err != nil {
			roundtripOK = false
			continue
		}
		raw, _ := json.Marshal(item.body)
		json.Unmarshal(raw, &originalPayload)
		if !reflect.DeepEqual(openedPayload, originalPayload) {
			roundtripOK = false
		}
	}
	h.check("拉取对象可端到端解密", roundtripOK, "open mismatch")

	fmt.Println("== 分叉/回滚检测 ==")
	status, body, err = device2.request("POST", "/sync/v2/push", map[string]interface{}{
		"protocol": 2, "known_head": genesisHead(alice.userID), "objects": objects[:1],
	}, true)
	if err != nil {
		return server, work, err
	}
	h.check("过期 head 推送被拒绝(409)", status == 409, fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = device2.request("POST", "/sync/v2/pull", map[string]interface{}{"protocol": 2, "since_seq": 0}, false)
	if err != nil {
		return server, work, err
	}
	freshHead := getStr(body, "head")
	status, body, err = device2.request("POST", "/sync/v2/push", map[string]interface{}{
		"protocol": 2, "known_head": freshHead, "objects": objects[:1],
	}, true)
	if err != nil {
		return server, work, err
	}
	h.check("拉取合并后重推成功", status == 200 && getFloat(body, "skipped") == 1, fmt.Sprintf("HTTP %d %v", status, body))

	fmt.Println("== ids 清单 ==")
	status, body, err = device2.request("POST", "/sync/v2/ids", map[string]interface{}{"protocol": 2}, false)
	if err != nil {
		return server, work, err
	}
	entries := getList(body, "entries")
	idOK := status == 200 && len(entries) == 6
	for _, entry := range entries {
		row, ok := entry.(map[string]interface{})
		if !ok || len(getStr(row, "blob_hash")) != 64 {
			idOK = false
		}
	}
	h.check("ids 清单返回 blob 哈希", idOK, fmt.Sprintf("HTTP %d %v", status, body))

	fmt.Println("== 多用户隔离 ==")
	status, body, err = alice.request("POST", "/admin/users", map[string]interface{}{
		"username": "bob", "password": bobPassword, "display_name": "bob",
	}, true)
	if err != nil {
		return server, work, err
	}
	h.check("超管创建 bob", status == 200 && getStr(getMap(body, "user"), "username") == "bob", fmt.Sprintf("HTTP %d %v", status, body))
	out, err = h.helperRun("gen-dek", bobPassword)
	if err != nil {
		return server, work, err
	}
	bobBundle := map[string]interface{}{}
	if err := json.Unmarshal([]byte(out), &bobBundle); err != nil {
		return server, work, err
	}
	bob := newClient(server.port)
	if err := bob.login("bob", bobPassword); err != nil {
		return server, work, err
	}
	if err := bob.me(); err != nil {
		return server, work, err
	}
	status, body, err = bob.request("POST", "/auth/dek", map[string]interface{}{
		"dek_envelope":      bobBundle["dek_envelope"],
		"kdf_salt":          bobBundle["kdf_salt"],
		"kdf_params":        bobBundle["kdf_params"],
		"recovery_envelope": bobBundle["recovery_envelope"],
		"recovery_hash":     bobBundle["recovery_hash"],
	}, true)
	if err != nil {
		return server, work, err
	}
	h.check("bob 上传 DEK 信封", status == 200, fmt.Sprintf("HTTP %d %v", status, body))
	status, body, err = bob.request("POST", "/sync/v2/pull", map[string]interface{}{"protocol": 2, "since_seq": 0}, false)
	if err != nil {
		return server, work, err
	}
	h.check("bob 拉取为空(隔离)", status == 200 && len(getList(body, "objects")) == 0, fmt.Sprintf("HTTP %d %v", status, body))
	_, err = h.helperRunStdin(base64.StdEncoding.EncodeToString(blobs[assetID]), "open", getStr(bobBundle, "dek"), assetID, "asset")
	h.check("bob 的 DEK 无法解开 alice 对象", err != nil, "wrong key opened alice blob")

	fmt.Println("== 重启持久化 ==")
	server.stop()
	if err := server.start(); err != nil {
		return server, work, err
	}
	device3 := newClient(server.port)
	if err := device3.login("alice", alicePassword); err != nil {
		return server, work, err
	}
	if err := device3.me(); err != nil {
		return server, work, err
	}
	status, body, err = device3.request("POST", "/sync/v2/pull", map[string]interface{}{"protocol": 2, "since_seq": 0}, false)
	if err != nil {
		return server, work, err
	}
	h.check("重启后对象仍在", status == 200 && len(getList(body, "objects")) == 6, fmt.Sprintf("HTTP %d %v", status, body))
	return server, work, nil
}
