package fleetserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

type serviceFixture struct {
	service  *Service
	accounts *account.Accounts
	db       *sql.DB
	now      int64
}

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	accounts := account.New(database.DB())
	fixture := &serviceFixture{accounts: accounts, db: database.DB(), now: time.Now().UnixMilli()}
	service, err := New(Config{DB: database.DB(), Accounts: accounts}, WithNow(func() int64 { return fixture.now }))
	if err != nil {
		t.Fatal(err)
	}
	fixture.service = service
	return fixture
}

func (f *serviceFixture) createUser(t *testing.T, username string) *account.User {
	t.Helper()
	user, err := f.accounts.CreateUser(context.Background(), username, "", "password-"+username)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func (f *serviceFixture) createSuperadmin(t *testing.T, username string) *account.User {
	t.Helper()
	code, err := f.accounts.GenerateInitCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	user, err := f.accounts.InitSuperadmin(context.Background(), code, username, "password-"+username)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func (f *serviceFixture) identity(user *account.User) *account.Identity {
	return &account.Identity{UserID: user.ID, Role: user.Role, State: user.State}
}

func (f *serviceFixture) enrollAgent(t *testing.T, owner *account.User, name string) *EnrollResult {
	t.Helper()
	code, _, err := f.service.IssueEnrollCode(context.Background(), f.identity(owner), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.service.ConsumeEnrollCode(context.Background(), code, name, "linux", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (f *serviceFixture) auditRows(t *testing.T, kind string) []map[string]any {
	t.Helper()
	rows, err := f.db.QueryContext(context.Background(), "SELECT payload_json FROM audit_log WHERE source = ? AND kind = ? ORDER BY id", auditSourceFleet, kind)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var payloads []map[string]any
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

func requireIPCCode(t *testing.T, err error, code ipc.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %q, got nil", code)
	}
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("expected error code %q, got %v", code, err)
	}
}

func TestFleetEnrollCodeSingleUse(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")

	code, expiresAt, err := fixture.service.IssueEnrollCode(context.Background(), fixture.identity(owner), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if code == "" || expiresAt <= fixture.now {
		t.Fatalf("code=%q expiresAt=%d", code, expiresAt)
	}

	result, err := fixture.service.ConsumeEnrollCode(context.Background(), code, "build-host", "linux", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if result.DeviceID == "" || result.Secret == "" || result.MetricsIntervalMS != DefaultMetricsIntervalMS ||
		!result.DesiredAutostart || !result.TerminalEnabled || result.BaseURLs == nil {
		t.Fatalf("result=%+v", result)
	}

	var kind, purpose string
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT kind FROM user_device WHERE id = ?", result.DeviceID).Scan(&kind); err != nil || kind != agentDeviceKind {
		t.Fatalf("device kind=%q err=%v", kind, err)
	}
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT purpose FROM sync_credential WHERE device_id = ?", result.DeviceID).Scan(&purpose); err != nil || purpose != PurposeDeviceAgent {
		t.Fatalf("credential purpose=%q err=%v", purpose, err)
	}
	var agentPlatform string
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT platform FROM device_agent WHERE device_id = ?", result.DeviceID).Scan(&agentPlatform); err != nil || agentPlatform != "linux" {
		t.Fatalf("agent platform=%q err=%v", agentPlatform, err)
	}

	if _, err := fixture.service.ConsumeEnrollCode(context.Background(), code, "again", "linux", ""); err == nil {
		t.Fatal("second consume must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}

	audits := fixture.auditRows(t, auditKindDeviceEnroll)
	if len(audits) != 1 || audits[0]["device_id"] != result.DeviceID || audits[0]["outcome"] != "allow" || audits[0]["requester"] != owner.ID {
		t.Fatalf("enroll audits=%v", audits)
	}
}

func TestFleetEnrollCodeTTL(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")

	code, _, err := fixture.service.IssueEnrollCode(context.Background(), fixture.identity(owner), "", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	fixture.now += 6 * time.Minute.Milliseconds()
	if _, err := fixture.service.ConsumeEnrollCode(context.Background(), code, "late", "linux", ""); err == nil {
		t.Fatal("expired code must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}

	if _, _, err := fixture.service.IssueEnrollCode(context.Background(), fixture.identity(owner), "", 2*time.Hour); err == nil {
		t.Fatal("out-of-range ttl must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeBadParam)
	}
	if _, _, err := fixture.service.IssueEnrollCode(context.Background(), fixture.identity(owner), "", 0); err != nil {
		t.Fatalf("default ttl must pass: %v", err)
	}
}

func TestFleetEnrollCodeIssueTarget(t *testing.T) {
	fixture := newServiceFixture(t)
	root := fixture.createSuperadmin(t, "root")
	alice := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")

	if _, _, err := fixture.service.IssueEnrollCode(context.Background(), fixture.identity(alice), bob.ID, 0); err == nil {
		t.Fatal("non-superadmin issue for another user must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
	code, _, err := fixture.service.IssueEnrollCode(context.Background(), fixture.identity(root), bob.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.ConsumeEnrollCode(context.Background(), code, "bob-host", "linux", "")
	if err != nil {
		t.Fatal(err)
	}
	var ownerID string
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT user_id FROM user_device WHERE id = ?", result.DeviceID).Scan(&ownerID); err != nil || ownerID != bob.ID {
		t.Fatalf("device owner=%q err=%v", ownerID, err)
	}
}

func TestFleetEnrollCodeValidation(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	code, _, err := fixture.service.IssueEnrollCode(context.Background(), fixture.identity(owner), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ConsumeEnrollCode(context.Background(), "wrong-code", "host", "linux", ""); err == nil {
		t.Fatal("wrong code must fail")
	}
	if _, err := fixture.service.ConsumeEnrollCode(context.Background(), code, "", "linux", ""); err == nil {
		t.Fatal("empty name must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeBadParam)
	}
	if _, err := fixture.service.ConsumeEnrollCode(context.Background(), code, "host", strings.Repeat("p", maxPlatformLength+1), ""); err == nil {
		t.Fatal("overlong platform must fail")
	}
	if _, err := fixture.service.ConsumeEnrollCode(context.Background(), code, "host", "linux", strings.Repeat("v", maxAppVersionLength+1)); err == nil {
		t.Fatal("overlong app version must fail")
	}
}

func TestFleetAuthenticateAgent(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	result := fixture.enrollAgent(t, owner, "agent-host")

	userID, err := fixture.service.AuthenticateAgent(context.Background(), result.DeviceID, result.Secret)
	if err != nil || userID != owner.ID {
		t.Fatalf("authenticate user=%q err=%v", userID, err)
	}
	var lastUsedAt, deviceLastSeen, agentLastSeen sql.NullInt64
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT last_used_at FROM sync_credential WHERE device_id = ?", result.DeviceID).Scan(&lastUsedAt); err != nil || !lastUsedAt.Valid {
		t.Fatalf("credential last_used_at=%v err=%v", lastUsedAt, err)
	}
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT last_seen_at FROM user_device WHERE id = ?", result.DeviceID).Scan(&deviceLastSeen); err != nil || !deviceLastSeen.Valid {
		t.Fatalf("device last_seen_at=%v err=%v", deviceLastSeen, err)
	}
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT last_seen_at FROM device_agent WHERE device_id = ?", result.DeviceID).Scan(&agentLastSeen); err != nil || !agentLastSeen.Valid {
		t.Fatalf("agent last_seen_at=%v err=%v", agentLastSeen, err)
	}

	if _, err := fixture.service.AuthenticateAgent(context.Background(), "other-device", result.Secret); err == nil {
		t.Fatal("mismatched device id must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
	if _, err := fixture.service.AuthenticateAgent(context.Background(), result.DeviceID, "wrong-secret"); err == nil {
		t.Fatal("wrong secret must fail")
	}
}

func TestFleetAuthenticateAgentPurposeIsolation(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	device, err := fixture.accounts.RegisterDevice(context.Background(), owner.ID, "desktop", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := randomSecret(credentialSecretBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.ExecContext(context.Background(), `INSERT INTO sync_credential(id, user_id, device_id, purpose, secret_hash, created_at, expires_at)
VALUES(?,?,?,?,?,?,0)`, "cred-sync-purpose", owner.ID, device.ID, "sync", hashSecret(secret), fixture.now); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.AuthenticateAgent(context.Background(), device.ID, secret); err == nil {
		t.Fatal("sync-purpose credential must not authenticate as agent")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
}

func TestFleetAuthorizeRoleIsolation(t *testing.T) {
	fixture := newServiceFixture(t)
	root := fixture.createSuperadmin(t, "root")
	alice := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")
	result := fixture.enrollAgent(t, alice, "alice-host")

	if err := fixture.service.RevokeDevice(context.Background(), fixture.identity(bob), result.DeviceID); err == nil {
		t.Fatal("non-owner revoke must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
	if err := fixture.service.SetDesiredAutostart(context.Background(), fixture.identity(bob), result.DeviceID, false); err == nil {
		t.Fatal("non-owner autostart must fail")
	}
	if _, err := fixture.service.DeviceMetrics(context.Background(), fixture.identity(bob), result.DeviceID, 0); err == nil {
		t.Fatal("non-owner metrics read must fail")
	}

	denies := fixture.auditRows(t, auditKindDeviceRevoke)
	if len(denies) != 1 || denies[0]["outcome"] != "deny" || denies[0]["reason"] != "not_owner" || denies[0]["requester"] != bob.ID {
		t.Fatalf("revoke audits=%v", denies)
	}

	if err := fixture.service.SetDesiredAutostart(context.Background(), fixture.identity(alice), result.DeviceID, false); err != nil {
		t.Fatalf("owner autostart: %v", err)
	}
	if err := fixture.service.SetDesiredAutostart(context.Background(), fixture.identity(root), result.DeviceID, true); err != nil {
		t.Fatalf("superadmin autostart: %v", err)
	}
	allows := fixture.auditRows(t, auditKindDeviceAutostart)
	if len(allows) != 3 || allows[0]["outcome"] != "deny" || allows[1]["outcome"] != "allow" || allows[2]["outcome"] != "allow" {
		t.Fatalf("autostart audits=%v", allows)
	}

	if err := fixture.service.RevokeDevice(context.Background(), fixture.identity(alice), "missing-device"); err == nil {
		t.Fatal("missing device must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeNotFound)
	}
	notFound := fixture.auditRows(t, auditKindDeviceRevoke)
	if len(notFound) != 2 || notFound[1]["reason"] != "not_found" {
		t.Fatalf("revoke audits=%v", notFound)
	}
}

func TestFleetRevokeDevice(t *testing.T) {
	fixture := newServiceFixture(t)
	root := fixture.createSuperadmin(t, "root")
	alice := fixture.createUser(t, "alice")
	result := fixture.enrollAgent(t, alice, "alice-host")

	if err := fixture.service.RevokeDevice(context.Background(), fixture.identity(root), result.DeviceID); err != nil {
		t.Fatalf("superadmin revoke: %v", err)
	}
	var revokedAt sql.NullInt64
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT revoked_at FROM user_device WHERE id = ?", result.DeviceID).Scan(&revokedAt); err != nil || !revokedAt.Valid {
		t.Fatalf("device revoked_at=%v err=%v", revokedAt, err)
	}
	var credentialRevoked sql.NullInt64
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT revoked_at FROM sync_credential WHERE device_id = ?", result.DeviceID).Scan(&credentialRevoked); err != nil || !credentialRevoked.Valid {
		t.Fatalf("credential revoked_at=%v err=%v", credentialRevoked, err)
	}
	if _, err := fixture.service.AuthenticateAgent(context.Background(), result.DeviceID, result.Secret); err == nil {
		t.Fatal("revoked device must not authenticate")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
	if err := fixture.service.RevokeDevice(context.Background(), fixture.identity(alice), result.DeviceID); err == nil {
		t.Fatal("second revoke must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}

	audits := fixture.auditRows(t, auditKindDeviceRevoke)
	if len(audits) != 2 || audits[0]["outcome"] != "allow" || audits[0]["requester"] != root.ID ||
		audits[1]["outcome"] != "deny" || audits[1]["reason"] != "device_revoked" {
		t.Fatalf("revoke audits=%v", audits)
	}

	devices, err := fixture.service.ListDevices(context.Background(), fixture.identity(alice))
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].RevokedAt == 0 || devices[0].Agent == nil {
		t.Fatalf("devices=%+v", devices)
	}
}

func TestFleetListDevicesFiltering(t *testing.T) {
	fixture := newServiceFixture(t)
	root := fixture.createSuperadmin(t, "root")
	alice := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")
	aliceResult := fixture.enrollAgent(t, alice, "alice-host")
	bobResult := fixture.enrollAgent(t, bob, "bob-host")

	aliceDevices, err := fixture.service.ListDevices(context.Background(), fixture.identity(alice))
	if err != nil {
		t.Fatal(err)
	}
	if len(aliceDevices) != 1 || aliceDevices[0].ID != aliceResult.DeviceID || aliceDevices[0].Owner != nil {
		t.Fatalf("alice devices=%+v", aliceDevices)
	}
	if aliceDevices[0].Agent == nil || aliceDevices[0].Agent.Platform != "linux" || !aliceDevices[0].Agent.DesiredAutostart {
		t.Fatalf("alice agent=%+v", aliceDevices[0].Agent)
	}

	allDevices, err := fixture.service.ListDevices(context.Background(), fixture.identity(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(allDevices) != 2 {
		t.Fatalf("superadmin sees %d devices", len(allDevices))
	}
	owners := map[string]string{}
	for _, device := range allDevices {
		if device.Owner == nil {
			t.Fatalf("superadmin view missing owner: %+v", device)
		}
		owners[device.ID] = device.Owner.Username
	}
	if owners[aliceResult.DeviceID] != "alice" || owners[bobResult.DeviceID] != "bob" {
		t.Fatalf("owners=%v", owners)
	}
}

func TestFleetBaseURLs(t *testing.T) {
	fixture := newServiceFixture(t)

	valid := []BaseURLEntry{
		{URL: "https://fleet.example.com/"},
		{URL: "http://localhost:8080"},
		{URL: "http://192.168.1.10:9000"},
		{URL: "http://public.example.com", Insecure: true},
	}
	normalized, err := fixture.service.SetBaseURLs(context.Background(), valid)
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"https://fleet.example.com", "http://localhost:8080", "http://192.168.1.10:9000", "http://public.example.com"}
	for i, want := range wantOrder {
		if normalized[i].URL != want {
			t.Fatalf("normalized[%d]=%q want %q", i, normalized[i].URL, want)
		}
	}
	if !normalized[3].Insecure {
		t.Fatal("insecure flag must survive normalization")
	}

	loaded, err := fixture.service.BaseURLs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 4 || loaded[0].URL != wantOrder[0] {
		t.Fatalf("loaded=%+v", loaded)
	}

	deduped, err := fixture.service.SetBaseURLs(context.Background(), []BaseURLEntry{{URL: "https://dup.example.com"}, {URL: "https://dup.example.com/"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(deduped) != 1 {
		t.Fatalf("deduped=%+v", deduped)
	}

	invalid := [][]BaseURLEntry{
		{{URL: "fleet.example.com"}},
		{{URL: "ftp://fleet.example.com"}},
		{{URL: "https://user:pass@fleet.example.com"}},
		{{URL: "https://fleet.example.com?x=1"}},
		{{URL: "https://fleet.example.com#frag"}},
		{{URL: "http://public.example.com"}},
		{{URL: ""}},
		{{URL: "https://a.example.com"}, {URL: "https://b.example.com"}, {URL: "https://c.example.com"}, {URL: "https://d.example.com"}, {URL: "https://e.example.com"}, {URL: "https://f.example.com"}, {URL: "https://g.example.com"}, {URL: "https://h.example.com"}, {URL: "https://i.example.com"}},
	}
	for _, entries := range invalid {
		if _, err := fixture.service.SetBaseURLs(context.Background(), entries); err == nil {
			t.Fatalf("entries %+v must fail", entries)
		}
	}
	after, err := fixture.service.BaseURLs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].URL != "https://dup.example.com" {
		t.Fatalf("failed set must not mutate stored urls: %+v", after)
	}
}

func TestFleetBaseURLEnrollResponse(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	if _, err := fixture.service.SetBaseURLs(context.Background(), []BaseURLEntry{{URL: "https://fleet.example.com"}}); err != nil {
		t.Fatal(err)
	}
	result := fixture.enrollAgent(t, owner, "agent-host")
	if len(result.BaseURLs) != 1 || result.BaseURLs[0].URL != "https://fleet.example.com" {
		t.Fatalf("enroll base urls=%+v", result.BaseURLs)
	}
}

func TestFleetAutostartState(t *testing.T) {
	fixture := newServiceFixture(t)
	alice := fixture.createUser(t, "alice")
	result := fixture.enrollAgent(t, alice, "agent-host")

	if err := fixture.service.SetDesiredAutostart(context.Background(), fixture.identity(alice), result.DeviceID, false); err != nil {
		t.Fatal(err)
	}
	var desired int
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT desired_autostart FROM device_agent WHERE device_id = ?", result.DeviceID).Scan(&desired); err != nil || desired != 0 {
		t.Fatalf("desired_autostart=%d err=%v", desired, err)
	}

	state := ServiceState{Installed: true, Enabled: false, Active: false, LastReconcileAt: fixture.now, LastError: "unit missing"}
	if err := fixture.service.UpdateServiceState(context.Background(), result.DeviceID, state); err != nil {
		t.Fatal(err)
	}
	devices, err := fixture.service.ListDevices(context.Background(), fixture.identity(alice))
	if err != nil {
		t.Fatal(err)
	}
	agent := devices[0].Agent
	if agent.DesiredAutostart || agent.ServiceState != state {
		t.Fatalf("agent=%+v", agent)
	}

	desktop, err := fixture.accounts.RegisterDevice(context.Background(), alice.ID, "desktop", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.SetDesiredAutostart(context.Background(), fixture.identity(alice), desktop.ID, false); err == nil {
		t.Fatal("autostart on non-agent device must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeNotFound)
	}
}

func TestFleetMetricsRecordQuery(t *testing.T) {
	fixture := newServiceFixture(t)
	alice := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")
	result := fixture.enrollAgent(t, alice, "agent-host")

	sample := MetricsSample{CPUPct: 42.5, MemUsed: 100, MemTotal: 200, DiskUsed: 300, DiskTotal: 400, UptimeS: 5000}
	if err := fixture.service.RecordMetrics(context.Background(), result.DeviceID, sample); err != nil {
		t.Fatal(err)
	}
	samples, err := fixture.service.DeviceMetrics(context.Background(), fixture.identity(alice), result.DeviceID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || samples[0].CPUPct != 42.5 || samples[0].MemTotal != 200 || samples[0].TS == 0 {
		t.Fatalf("samples=%+v", samples)
	}
	if _, err := fixture.service.DeviceMetrics(context.Background(), fixture.identity(bob), result.DeviceID, 0); err == nil {
		t.Fatal("non-owner metrics read must fail")
	}
	if err := fixture.service.RecordMetrics(context.Background(), "missing-device", sample); err == nil {
		t.Fatal("record on missing device must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeNotFound)
	}
}

func TestFleetMetricsRollup(t *testing.T) {
	fixture := newServiceFixture(t)
	alice := fixture.createUser(t, "alice")
	result := fixture.enrollAgent(t, alice, "agent-host")

	old := fixture.now - 49*time.Hour.Milliseconds()
	for i := 0; i < 3; i++ {
		sample := MetricsSample{TS: old + int64(i)*time.Minute.Milliseconds(), CPUPct: float64(10 * (i + 1)), MemUsed: 100, MemTotal: 200, DiskUsed: 300, DiskTotal: 400, UptimeS: 5000}
		if err := fixture.service.RecordMetrics(context.Background(), result.DeviceID, sample); err != nil {
			t.Fatal(err)
		}
	}
	ancient := MetricsSample{TS: fixture.now - 31*24*time.Hour.Milliseconds(), CPUPct: 99, MemUsed: 1, MemTotal: 2, DiskUsed: 3, DiskTotal: 4, UptimeS: 5}
	if err := fixture.service.RecordMetrics(context.Background(), result.DeviceID, ancient); err != nil {
		t.Fatal(err)
	}
	recent := MetricsSample{TS: fixture.now - time.Hour.Milliseconds(), CPUPct: 55, MemUsed: 110, MemTotal: 200, DiskUsed: 310, DiskTotal: 400, UptimeS: 6000}
	if err := fixture.service.RecordMetrics(context.Background(), result.DeviceID, recent); err != nil {
		t.Fatal(err)
	}

	if err := fixture.service.RollupMetrics(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.RollupMetrics(context.Background()); err != nil {
		t.Fatalf("rollup must be idempotent: %v", err)
	}

	var rawCount int
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT count(*) FROM device_metrics WHERE device_id = ?", result.DeviceID).Scan(&rawCount); err != nil {
		t.Fatal(err)
	}
	if rawCount != 1 {
		t.Fatalf("raw rows after rollup=%d", rawCount)
	}
	var bucketTS int64
	var cpu float64
	var memUsed, uptime int64
	var sampleCount int
	if err := fixture.db.QueryRowContext(context.Background(), `SELECT bucket_ts, cpu_pct, mem_used, uptime_s, sample_count FROM device_metrics_hourly
WHERE device_id = ? AND bucket_ts = (SELECT max(bucket_ts) FROM device_metrics_hourly WHERE device_id = ?)`, result.DeviceID, result.DeviceID).
		Scan(&bucketTS, &cpu, &memUsed, &uptime, &sampleCount); err != nil {
		t.Fatal(err)
	}
	if sampleCount != 3 || cpu != 20 || memUsed != 100 || uptime != 5000 {
		t.Fatalf("hourly bucket ts=%d cpu=%v mem=%d uptime=%d count=%d", bucketTS, cpu, memUsed, uptime, sampleCount)
	}
	var hourlyCount int
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT count(*) FROM device_metrics_hourly WHERE device_id = ?", result.DeviceID).Scan(&hourlyCount); err != nil {
		t.Fatal(err)
	}
	if hourlyCount != 1 {
		t.Fatalf("hourly rows=%d (ancient bucket must be purged)", hourlyCount)
	}
}

func TestFleetMetricsQueryClamp(t *testing.T) {
	fixture := newServiceFixture(t)
	alice := fixture.createUser(t, "alice")
	result := fixture.enrollAgent(t, alice, "agent-host")
	if err := fixture.service.RecordMetrics(context.Background(), result.DeviceID, MetricsSample{TS: fixture.now - time.Hour.Milliseconds(), CPUPct: 1}); err != nil {
		t.Fatal(err)
	}
	samples, err := fixture.service.DeviceMetrics(context.Background(), fixture.identity(alice), result.DeviceID, fixture.now-7*24*time.Hour.Milliseconds())
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 {
		t.Fatalf("since must clamp to raw retention, got %d samples", len(samples))
	}
}

func TestFleetCascadeDeleteUser(t *testing.T) {
	fixture := newServiceFixture(t)
	alice := fixture.createUser(t, "alice")
	result := fixture.enrollAgent(t, alice, "agent-host")
	if err := fixture.service.RecordMetrics(context.Background(), result.DeviceID, MetricsSample{CPUPct: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.ExecContext(context.Background(), "DELETE FROM app_user WHERE id = ?", alice.ID); err != nil {
		t.Fatal(err)
	}
	for table, column := range map[string]string{"user_device": "id", "device_agent": "device_id", "device_metrics": "device_id", "sync_credential": "device_id"} {
		var count int
		if err := fixture.db.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table+" WHERE "+column+" = ?", result.DeviceID).Scan(&count); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows=%d after user delete", table, count)
		}
	}
}

func TestFleetSetCurrentURL(t *testing.T) {
	fixture := newServiceFixture(t)
	alice := fixture.createUser(t, "alice")
	result := fixture.enrollAgent(t, alice, "agent-host")

	if err := fixture.service.SetCurrentURL(context.Background(), result.DeviceID, "https://backup.example.com", "primary down"); err != nil {
		t.Fatal(err)
	}
	devices, err := fixture.service.ListDevices(context.Background(), fixture.identity(alice))
	if err != nil {
		t.Fatal(err)
	}
	if devices[0].Agent.CurrentURL != "https://backup.example.com" {
		t.Fatalf("current_url=%q", devices[0].Agent.CurrentURL)
	}
	audits := fixture.auditRows(t, auditKindDeviceFailover)
	if len(audits) != 1 || audits[0]["url"] != "https://backup.example.com" || audits[0]["reason"] != "primary down" {
		t.Fatalf("failover audits=%v", audits)
	}
	if err := fixture.service.SetCurrentURL(context.Background(), "missing-device", "https://x.example.com", ""); err == nil {
		t.Fatal("missing device must fail")
	}
}
