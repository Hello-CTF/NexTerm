package fleetserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
)

// enrollAgent goes through the full issue+redeem flow and returns the device credentials.
func enrollAgent(t *testing.T, f *httpFixture, owner *account.User, name string) (deviceID, secret string) {
	t.Helper()
	session := f.session(t, owner)
	issue := f.call(t, "POST", "/device/enroll-codes", map[string]any{"ttl_ms": 600000}, session, session.csrf)
	if issue.status != http.StatusOK {
		t.Fatalf("issue enroll code: HTTP %d %v", issue.status, issue.body)
	}
	code, _ := issue.body["code"].(string)
	if code == "" {
		t.Fatalf("issue enroll code: empty code %v", issue.body)
	}
	enroll := f.call(t, "POST", "/device/enroll", map[string]any{
		"code": code, "name": name, "platform": "linux", "app_version": "e2e",
	}, nil, "")
	if enroll.status != http.StatusOK {
		t.Fatalf("enroll: HTTP %d %v", enroll.status, enroll.body)
	}
	deviceID, _ = enroll.body["device_id"].(string)
	secret, _ = enroll.body["secret"].(string)
	if deviceID == "" || secret == "" {
		t.Fatalf("enroll: missing credentials %v", enroll.body)
	}
	return deviceID, secret
}

func TestAgentSyncRequiresDeviceCredential(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "sync-owner")
	deviceID, secret := enrollAgent(t, f, owner, "sync-box")

	cases := []struct {
		name   string
		body   map[string]any
		status int
	}{
		{"valid credentials", map[string]any{"device_id": deviceID, "secret": secret}, http.StatusOK},
		{"invalid secret", map[string]any{"device_id": deviceID, "secret": "wrong"}, http.StatusForbidden},
		{"device mismatch", map[string]any{"device_id": "01J5OTHER0000000000000000", "secret": secret}, http.StatusForbidden},
		{"missing device ID", map[string]any{"secret": secret}, http.StatusForbidden},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := f.call(t, "POST", "/agent/sync", testCase.body, nil, "")
			if response.status != testCase.status {
				t.Fatalf("sync: HTTP %d %v", response.status, response.body)
			}
		})
	}
}

func TestAgentSyncSecondDeviceActiveImmediately(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "sync-multi")
	firstID, firstSecret := enrollAgent(t, f, owner, "multi-box-1")
	secondID, secondSecret := enrollAgent(t, f, owner, "multi-box-2")

	for _, device := range [][2]string{{firstID, firstSecret}, {secondID, secondSecret}} {
		response := f.call(t, "POST", "/agent/sync", map[string]any{"device_id": device[0], "secret": device[1]}, nil, "")
		if response.status != http.StatusOK {
			t.Fatalf("sync %s: HTTP %d %v", device[0], response.status, response.body)
		}
	}
}

func TestAgentSyncRecordsMetricsAndState(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "sync-metrics")
	deviceID, secret := enrollAgent(t, f, owner, "metrics-box")

	response := f.call(t, "POST", "/agent/sync", map[string]any{
		"device_id": deviceID, "secret": secret,
		"sample":        map[string]any{"ts": time.Now().UnixMilli(), "cpu_pct": 12.5, "mem_used": 100, "mem_total": 200, "disk_used": 300, "disk_total": 400, "uptime_s": 42},
		"service_state": map[string]any{"installed": true, "enabled": true, "active": true},
	}, nil, "")
	if response.status != http.StatusOK {
		t.Fatalf("sync: HTTP %d %v", response.status, response.body)
	}
	if response.body["desired_autostart"] != true || response.body["terminal_enabled"] != true {
		t.Fatalf("sync response missing desired config: %v", response.body)
	}
	if interval, ok := response.body["metrics_interval_ms"].(float64); !ok || interval != DefaultMetricsIntervalMS {
		t.Fatalf("metrics_interval_ms = %v, want %d", response.body["metrics_interval_ms"], DefaultMetricsIntervalMS)
	}

	samples, err := f.service.DeviceMetrics(context.Background(), &account.Identity{UserID: owner.ID, Role: account.RoleSuperadmin}, deviceID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || samples[0].CPUPct != 12.5 || samples[0].UptimeS != 42 {
		t.Fatalf("samples = %+v", samples)
	}

	devices, err := f.service.ListDevices(context.Background(), &account.Identity{UserID: owner.ID, Role: account.RoleSuperadmin})
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].Agent == nil || !devices[0].Agent.ServiceState.Installed {
		t.Fatalf("devices = %+v", devices)
	}
}

func TestAgentSyncRejectedAfterRevoke(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "sync-revoke")
	deviceID, secret := enrollAgent(t, f, owner, "revoked-box")
	session := f.session(t, owner)

	revoke := f.call(t, "POST", "/fleet/devices/"+deviceID+"/revoke", nil, session, session.csrf)
	if revoke.status != http.StatusOK {
		t.Fatalf("revoke: HTTP %d %v", revoke.status, revoke.body)
	}
	response := f.call(t, "POST", "/agent/sync", map[string]any{"device_id": deviceID, "secret": secret}, nil, "")
	if response.status != http.StatusForbidden {
		t.Fatalf("sync after revoke: HTTP %d %v", response.status, response.body)
	}
}

func TestAgentCurrentURLRecordsFailover(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "failover-owner")
	deviceID, secret := enrollAgent(t, f, owner, "failover-box")

	response := f.call(t, "POST", "/agent/current-url", map[string]any{
		"device_id": deviceID, "secret": secret, "url": "https://backup.example.com", "reason": "failover",
	}, nil, "")
	if response.status != http.StatusOK {
		t.Fatalf("current-url: HTTP %d %v", response.status, response.body)
	}
	devices, err := f.service.ListDevices(context.Background(), &account.Identity{UserID: owner.ID, Role: account.RoleSuperadmin})
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].Agent == nil || devices[0].Agent.CurrentURL != "https://backup.example.com" {
		t.Fatalf("devices = %+v", devices)
	}

	forbidden := f.call(t, "POST", "/agent/current-url", map[string]any{
		"device_id": deviceID, "secret": "wrong", "url": "https://backup.example.com", "reason": "failover",
	}, nil, "")
	if forbidden.status != http.StatusForbidden {
		t.Fatalf("current-url bad secret: HTTP %d %v", forbidden.status, forbidden.body)
	}
}

func TestAgentSyncUsesAgentWireShape(t *testing.T) {
	// Contract alignment: the handler must consume the wire format of agent.SyncRequest directly (including omitempty field names).
	payload := `{"device_id":"d","secret":"s","sample":{"ts":1,"cpu_pct":0.5,"mem_used":1,"mem_total":2,"disk_used":3,"disk_total":4,"uptime_s":5},"service_state":{"installed":true,"enabled":false,"active":true,"last_reconcile_at":7,"last_error":"x"}}`
	var request agent.SyncRequest
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		t.Fatalf("agent.SyncRequest must accept the wire shape: %v", err)
	}
	if request.Sample == nil || request.ServiceState == nil || request.ServiceState.LastReconcileAt != 7 {
		t.Fatalf("decoded = %+v", request)
	}
}
