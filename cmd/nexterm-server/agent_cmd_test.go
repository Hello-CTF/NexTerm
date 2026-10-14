//go:build darwin || linux

package main

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"testing"
	"time"
)

// TestAgentCLIDispatch verifies agent subcommand dispatch of the same binary:
// missing subcommand/unknown subcommand/missing required flags all exit with
// code 2 without touching any service manager.
func TestAgentCLIDispatch(t *testing.T) {
	binary := buildServerBinary(t)
	cases := []struct {
		name string
		args []string
	}{
		{"missing subcommand", []string{"agent"}},
		{"unknown subcommand", []string{"agent", "bogus"}},
		{"enroll missing required flags", []string{"agent", "enroll"}},
		{"enroll missing enrollment code", []string{"agent", "enroll", "--server", "http://127.0.0.1:1"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			command := exec.Command(binary, testCase.args...)
			output, err := command.CombinedOutput()
			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != 2 {
				t.Fatalf("exit = %v, want 2; output=%s", err, output)
			}
		})
	}
}

// TestServerProcessMountsFleetRoutes verifies fleet wiring with a real
// process: enrollment, user-level device management, and agent contract
// routes are available in the real binary with correct auth boundaries.
func TestServerProcessMountsFleetRoutes(t *testing.T) {
	binary := buildServerBinary(t)
	dataDir := t.TempDir()
	process, address := startServerProcess(t, binary, dataDir, false, "--auth=on")
	defer process.stop(t)
	waitForHealth(t, process, address)
	session := initSuperadminThroughProcess(t, process, address, "fleet-admin", "fleet-admin-pw-123")
	client := &http.Client{Timeout: 5 * time.Second}

	// Session + CSRF issues an enroll code.
	issue := requestAccountJSON(t, client, address, "/device/enroll-codes", map[string]any{"ttl_ms": 600000}, session, http.StatusOK)
	var issued struct {
		Code string `json:"code"`
	}
	decodeAccountData(t, issue, &issued)
	if issued.Code == "" {
		t.Fatalf("enroll code = %s", issue.body)
	}

	// Anonymous exchange of the enroll code for device credentials.
	enrollPayload, err := json.Marshal(map[string]any{"code": issued.Code, "name": "process-box", "platform": "linux", "app_version": "e2e"})
	if err != nil {
		t.Fatal(err)
	}
	enroll := requestAccountJSON(t, client, address, "/device/enroll", json.RawMessage(enrollPayload), nil, http.StatusOK)
	var enrolled struct {
		DeviceID string `json:"device_id"`
		Secret   string `json:"secret"`
	}
	decodeAccountData(t, enroll, &enrolled)
	if enrolled.DeviceID == "" || enrolled.Secret == "" {
		t.Fatalf("enroll = %s", enroll.body)
	}

	// Device credentials work via /agent/sync; wrong credentials get 403.
	syncPayload, err := json.Marshal(map[string]any{
		"device_id": enrolled.DeviceID, "secret": enrolled.Secret,
		"sample": map[string]any{"ts": time.Now().UnixMilli(), "cpu_pct": 1.5, "mem_used": 1, "mem_total": 2, "disk_used": 3, "disk_total": 4, "uptime_s": 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestAccountJSON(t, client, address, "/agent/sync", json.RawMessage(syncPayload), nil, http.StatusOK)
	badSync, err := json.Marshal(map[string]any{"device_id": enrolled.DeviceID, "secret": "wrong"})
	if err != nil {
		t.Fatal(err)
	}
	requestAccountJSON(t, client, address, "/agent/sync", json.RawMessage(badSync), nil, http.StatusForbidden)

	// The user-level device list requires a session (no cookie -> 401).
	response, err := client.Get("http://" + address + "/fleet/devices")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous /fleet/devices status = %d, want 401", response.StatusCode)
	}

	// Device metrics read back the just-synced sample per session (GET route
	// with session cookie).
	metricsRequest, err := http.NewRequest(http.MethodGet, "http://"+address+"/fleet/devices/"+enrolled.DeviceID+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	metricsRequest.AddCookie(session.cookie)
	metricsResponse, err := client.Do(metricsRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer metricsResponse.Body.Close()
	if metricsResponse.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d", metricsResponse.StatusCode)
	}
	var samples struct {
		Samples []struct {
			CPUPct float64 `json:"cpu_pct"`
		} `json:"samples"`
	}
	if err := json.NewDecoder(metricsResponse.Body).Decode(&samples); err != nil {
		t.Fatal(err)
	}
	if len(samples.Samples) != 1 || samples.Samples[0].CPUPct != 1.5 {
		t.Fatalf("metrics = %+v", samples)
	}
}
