package fleetserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
)

type serviceFixture struct {
	service  *Service
	accounts *account.Accounts
	db       *gorm.DB
	now      int64
}

func newServiceFixture(t testing.TB) *serviceFixture {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	accounts := account.New(database.GORM())
	fixture := &serviceFixture{accounts: accounts, db: database.GORM(), now: time.Now().UnixMilli()}
	service, err := New(Config{DB: database.GORM(), Accounts: accounts}, WithNow(func() int64 { return fixture.now }))
	if err != nil {
		t.Fatal(err)
	}
	fixture.service = service
	return fixture
}

func (f *serviceFixture) createUser(t testing.TB, username string) *account.User {
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

func (f *serviceFixture) enrollAgent(t testing.TB, owner *account.User, name string) *EnrollResult {
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
	return auditPayloadRows(t, f.db, auditSourceFleet, kind)
}

func auditPayloadRows(t *testing.T, db *gorm.DB, source, kind string) []map[string]any {
	t.Helper()
	audit := query.Use(db).AuditRow
	rows, err := audit.WithContext(context.Background()).
		Where(audit.Source.Eq(source), audit.Kind.Eq(kind)).
		Order(audit.ID).
		Find()
	if err != nil {
		t.Fatal(err)
	}
	var payloads []map[string]any
	for _, row := range rows {
		var payload map[string]any
		if err := json.Unmarshal([]byte(row.PayloadJSON), &payload); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

// auditAssetIDs returns the asset_id column of fleet audit rows of the given kind (the filter anchor of the device timeline).
func auditAssetIDs(t *testing.T, db *gorm.DB, kind string) []string {
	t.Helper()
	audit := query.Use(db).AuditRow
	rows, err := audit.WithContext(context.Background()).
		Where(audit.Source.Eq(auditSourceFleet), audit.Kind.Eq(kind)).
		Order(audit.ID).
		Find()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, row := range rows {
		if row.AssetID != nil {
			ids = append(ids, *row.AssetID)
		} else {
			ids = append(ids, "")
		}
	}
	return ids
}

// Online/offline audits are only written on real state transitions: offline-before-online is ignored, and repeated online/offline only records once.
func TestDevicePresenceTransitionDedup(t *testing.T) {
	fixture := newServiceFixture(t)

	fixture.service.deviceOffline("dev-dedup")
	if rows := fixture.auditRows(t, auditKindDeviceOffline); len(rows) != 0 {
		t.Fatalf("offline-before-online wrote an offline audit: %v", rows)
	}

	fixture.service.deviceOnline("dev-dedup")
	fixture.service.deviceOnline("dev-dedup")
	fixture.service.deviceOffline("dev-dedup")
	fixture.service.deviceOffline("dev-dedup")

	onlines := fixture.auditRows(t, auditKindDeviceOnline)
	if len(onlines) != 1 || onlines[0]["action"] != "online" || onlines[0]["device_id"] != "dev-dedup" {
		t.Fatalf("online audits=%v", onlines)
	}
	offlines := fixture.auditRows(t, auditKindDeviceOffline)
	if len(offlines) != 1 || offlines[0]["action"] != "offline" || offlines[0]["device_id"] != "dev-dedup" {
		t.Fatalf("offline audits=%v", offlines)
	}
	if ids := auditAssetIDs(t, fixture.db, auditKindDeviceOnline); len(ids) != 1 || ids[0] != "dev-dedup" {
		t.Fatalf("online audit asset_ids=%v", ids)
	}
	if ids := auditAssetIDs(t, fixture.db, auditKindDeviceOffline); len(ids) != 1 || ids[0] != "dev-dedup" {
		t.Fatalf("offline audit asset_ids=%v", ids)
	}
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

	ctx := context.Background()
	userDevice := query.Use(fixture.db).UserDevice
	device, err := userDevice.WithContext(ctx).Where(userDevice.ID.Eq(result.DeviceID)).First()
	if err != nil || device.Kind != agentDeviceKind {
		t.Fatalf("device kind=%q err=%v", device.Kind, err)
	}
	syncCredential := query.Use(fixture.db).SyncCredential
	credential, err := syncCredential.WithContext(ctx).Where(syncCredential.DeviceID.Eq(result.DeviceID)).First()
	if err != nil || credential.Purpose != PurposeDeviceAgent {
		t.Fatalf("credential purpose=%q err=%v", credential.Purpose, err)
	}
	deviceAgent := query.Use(fixture.db).DeviceAgent
	agent, err := deviceAgent.WithContext(ctx).Where(deviceAgent.DeviceID.Eq(result.DeviceID)).First()
	if err != nil || agent.Platform != "linux" {
		t.Fatalf("agent platform=%q err=%v", agent.Platform, err)
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
	if ids := auditAssetIDs(t, fixture.db, auditKindDeviceEnroll); len(ids) != 1 || ids[0] != result.DeviceID {
		t.Fatalf("enroll audit asset_ids=%v", ids)
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
	userDevice := query.Use(fixture.db).UserDevice
	device, err := userDevice.WithContext(context.Background()).Where(userDevice.ID.Eq(result.DeviceID)).First()
	if err != nil || device.UserID != bob.ID {
		t.Fatalf("device owner=%q err=%v", device.UserID, err)
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
	ctx := context.Background()
	syncCredential := query.Use(fixture.db).SyncCredential
	credential, err := syncCredential.WithContext(ctx).Where(syncCredential.DeviceID.Eq(result.DeviceID)).First()
	if err != nil || credential.LastUsedAt == nil {
		t.Fatalf("credential last_used_at=%v err=%v", credential.LastUsedAt, err)
	}
	userDevice := query.Use(fixture.db).UserDevice
	device, err := userDevice.WithContext(ctx).Where(userDevice.ID.Eq(result.DeviceID)).First()
	if err != nil || device.LastSeenAt == nil {
		t.Fatalf("device last_seen_at=%v err=%v", device.LastSeenAt, err)
	}
	deviceAgent := query.Use(fixture.db).DeviceAgent
	agent, err := deviceAgent.WithContext(ctx).Where(deviceAgent.DeviceID.Eq(result.DeviceID)).First()
	if err != nil || agent.LastSeenAt == nil {
		t.Fatalf("agent last_seen_at=%v err=%v", agent.LastSeenAt, err)
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
	if err := query.Use(fixture.db).SyncCredential.WithContext(context.Background()).Create(&store.SyncCredential{
		ID: "cred-sync-purpose", UserID: owner.ID, DeviceID: device.ID, Purpose: "sync",
		SecretHash: hashSecret(secret), CreatedAt: fixture.now,
	}); err != nil {
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
	if err := fixture.service.SetDesiredAutostart(context.Background(), fixture.identity(root), result.DeviceID, true); err == nil {
		t.Fatal("superadmin autostart on another user's device must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
	allows := fixture.auditRows(t, auditKindDeviceAutostart)
	if len(allows) != 3 || allows[0]["outcome"] != "deny" || allows[1]["outcome"] != "allow" || allows[2]["outcome"] != "deny" || allows[2]["reason"] != "not_owner" {
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
	alice := fixture.createUser(t, "alice")
	result := fixture.enrollAgent(t, alice, "alice-host")

	if err := fixture.service.RevokeDevice(context.Background(), fixture.identity(alice), result.DeviceID); err != nil {
		t.Fatalf("owner revoke: %v", err)
	}
	ctx := context.Background()
	userDevice := query.Use(fixture.db).UserDevice
	device, err := userDevice.WithContext(ctx).Where(userDevice.ID.Eq(result.DeviceID)).First()
	if err != nil || device.RevokedAt == nil {
		t.Fatalf("device revoked_at=%v err=%v", device.RevokedAt, err)
	}
	syncCredential := query.Use(fixture.db).SyncCredential
	credential, err := syncCredential.WithContext(ctx).Where(syncCredential.DeviceID.Eq(result.DeviceID)).First()
	if err != nil || credential.RevokedAt == nil {
		t.Fatalf("credential revoked_at=%v err=%v", credential.RevokedAt, err)
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
	if len(audits) != 2 || audits[0]["outcome"] != "allow" || audits[0]["requester"] != alice.ID ||
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
	_ = fixture.enrollAgent(t, bob, "bob-host")

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
	if len(allDevices) != 0 {
		t.Fatalf("superadmin sees %d devices", len(allDevices))
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
	deviceAgent := query.Use(fixture.db).DeviceAgent
	agentRow, err := deviceAgent.WithContext(context.Background()).Where(deviceAgent.DeviceID.Eq(result.DeviceID)).First()
	if err != nil || agentRow.DesiredAutostart {
		t.Fatalf("desired_autostart=%v err=%v", agentRow.DesiredAutostart, err)
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

	metrics, err := fixture.service.DeviceMetrics(context.Background(), fixture.identity(alice), result.DeviceID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Samples == nil || len(metrics.Samples) != 0 || metrics.Latest != nil || metrics.SampleCount != 0 {
		t.Fatalf("empty metrics=%+v", metrics)
	}

	sample := MetricsSample{CPUPct: 42.5, MemUsed: 100, MemTotal: 200, DiskUsed: 300, DiskTotal: 400, UptimeS: 5000}
	if err := fixture.service.RecordMetrics(context.Background(), result.DeviceID, sample); err != nil {
		t.Fatal(err)
	}
	metrics, err = fixture.service.DeviceMetrics(context.Background(), fixture.identity(alice), result.DeviceID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics.Samples) != 1 || metrics.Samples[0].CPUPct != 42.5 || metrics.Samples[0].MemTotal != 200 || metrics.Samples[0].TS == 0 {
		t.Fatalf("samples=%+v", metrics.Samples)
	}
	if metrics.Latest == nil || *metrics.Latest != *metrics.Samples[0] || metrics.SampleCount != 1 || metrics.Downsampled {
		t.Fatalf("single metrics=%+v", metrics)
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

func TestFleetMetricsQueryKeepsLatestAndStableOrder(t *testing.T) {
	fixture := newServiceFixture(t)
	alice := fixture.createUser(t, "alice")
	result := fixture.enrollAgent(t, alice, "agent-host")
	base := fixture.now - time.Hour.Milliseconds()
	rowCount := maxMetricsSamples + 2
	rows := make([]*store.DeviceMetrics, 0, rowCount)
	for i := rowCount - 1; i >= 0; i-- {
		cpu := float64(i)
		rows = append(rows, &store.DeviceMetrics{
			ID: fmt.Sprintf("metric-%06d", i), DeviceID: result.DeviceID,
			TS: base + int64(i/2), CPUPct: &cpu,
		})
	}
	deviceMetrics := query.Use(fixture.db).DeviceMetrics
	if err := deviceMetrics.WithContext(context.Background()).CreateInBatches(rows, 500); err != nil {
		t.Fatal(err)
	}

	metrics, err := fixture.service.DeviceMetrics(context.Background(), fixture.identity(alice), result.DeviceID, base)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.SampleCount != int64(rowCount) || !metrics.Downsampled || len(metrics.Samples) == 0 || len(metrics.Samples) > maxMetricsSamples {
		t.Fatalf("metrics count=%d returned=%d downsampled=%v", metrics.SampleCount, len(metrics.Samples), metrics.Downsampled)
	}
	if metrics.Latest == nil || metrics.Latest.CPUPct != float64(rowCount-1) {
		t.Fatalf("latest=%+v", metrics.Latest)
	}
	for i, sample := range metrics.Samples {
		ordinal := int(sample.CPUPct)
		wantTS := base + int64(ordinal/2)
		if ordinal < 0 || ordinal >= rowCount || sample.TS != wantTS {
			t.Fatalf("sample[%d]=(ts=%d,cpu=%v) is not an original row", i, sample.TS, sample.CPUPct)
		}
		if i > 0 {
			previous := metrics.Samples[i-1]
			if previous.TS > sample.TS || (previous.TS == sample.TS && previous.CPUPct > sample.CPUPct) {
				t.Fatalf("samples are not in stable (ts,id) order at %d", i)
			}
		}
	}
	if metrics.Samples[0].CPUPct != 0 || metrics.Samples[len(metrics.Samples)-1].CPUPct != float64(rowCount-1) {
		t.Fatalf("window endpoints are missing: first=%+v last=%+v", metrics.Samples[0], metrics.Samples[len(metrics.Samples)-1])
	}
}

func deviceMetricRowForTest(deviceID, id string, sample MetricsSample) *store.DeviceMetrics {
	return &store.DeviceMetrics{
		ID: id, DeviceID: deviceID, TS: sample.TS,
		CPUPct: &sample.CPUPct, MemUsed: &sample.MemUsed, MemTotal: &sample.MemTotal,
		DiskUsed: &sample.DiskUsed, DiskTotal: &sample.DiskTotal, UptimeS: &sample.UptimeS,
	}
}

func TestFleetMetricsQueryKeepsAllSamplesAtLimit(t *testing.T) {
	fixture := newServiceFixture(t)
	alice := fixture.createUser(t, "alice")
	result := fixture.enrollAgent(t, alice, "agent-host")
	base := fixture.now - time.Hour.Milliseconds()
	rows := make([]*store.DeviceMetrics, 0, maxMetricsSamples)
	for i := maxMetricsSamples - 1; i >= 0; i-- {
		sample := MetricsSample{TS: base + int64(i/2), CPUPct: float64(i), UptimeS: int64(i)}
		rows = append(rows, deviceMetricRowForTest(result.DeviceID, fmt.Sprintf("metric-%06d", i), sample))
	}
	deviceMetrics := query.Use(fixture.db).DeviceMetrics
	if err := deviceMetrics.WithContext(context.Background()).CreateInBatches(rows, 500); err != nil {
		t.Fatal(err)
	}

	metrics, err := fixture.service.DeviceMetrics(context.Background(), fixture.identity(alice), result.DeviceID, base)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.SampleCount != maxMetricsSamples || metrics.Downsampled || len(metrics.Samples) != maxMetricsSamples {
		t.Fatalf("metrics count=%d returned=%d downsampled=%v", metrics.SampleCount, len(metrics.Samples), metrics.Downsampled)
	}
	if metrics.Latest == nil || metrics.Latest.CPUPct != maxMetricsSamples-1 || metrics.Samples[0].CPUPct != 0 || metrics.Samples[len(metrics.Samples)-1].CPUPct != maxMetricsSamples-1 {
		t.Fatalf("latest/endpoints=%+v %+v %+v", metrics.Latest, metrics.Samples[0], metrics.Samples[len(metrics.Samples)-1])
	}
}

func TestFleetMetricsQueryKeepsCompleteWindow(t *testing.T) {
	fixture := newServiceFixture(t)
	alice := fixture.createUser(t, "alice")
	result := fixture.enrollAgent(t, alice, "agent-host")
	const rowCount = 17280
	base := fixture.now - 24*time.Hour.Milliseconds()
	raw := make(map[int64]MetricsSample, rowCount)
	rows := make([]*store.DeviceMetrics, 0, rowCount)
	for i := rowCount - 1; i >= 0; i-- {
		ts := base + int64(i)*5000
		if i > 0 && i%1000 == 0 {
			ts = base + int64(i-1)*5000
		}
		sample := MetricsSample{TS: ts, CPUPct: 10, MemUsed: 50, MemTotal: 100, DiskUsed: 25, DiskTotal: 100, UptimeS: int64(i)}
		switch i {
		case 100:
			sample.CPUPct = -5
		case 200:
			sample.CPUPct = 99.5
		case 300:
			sample.MemUsed = 90
		case 400:
			sample.MemUsed = 5
		case 500:
			sample.DiskUsed = 95
		case 600:
			sample.DiskUsed = 5
		case 17000:
			sample.CPUPct = 98.5
		}
		raw[sample.UptimeS] = sample
		rows = append(rows, deviceMetricRowForTest(result.DeviceID, fmt.Sprintf("metric-%06d", i), sample))
	}
	deviceMetrics := query.Use(fixture.db).DeviceMetrics
	if err := deviceMetrics.WithContext(context.Background()).CreateInBatches(rows, 500); err != nil {
		t.Fatal(err)
	}

	metrics, err := fixture.service.DeviceMetrics(context.Background(), fixture.identity(alice), result.DeviceID, base)
	if err != nil {
		t.Fatal(err)
	}
	samples := metrics.Samples
	if metrics.SampleCount != rowCount || !metrics.Downsampled || len(samples) == 0 || len(samples) >= rowCount || len(samples) > maxMetricsSamples {
		t.Fatalf("sample count=%d returned=%d downsampled=%v", metrics.SampleCount, len(samples), metrics.Downsampled)
	}
	if metrics.WindowStart != base || metrics.WindowEnd != fixture.now || metrics.SampleIntervalMS != 5000 {
		t.Fatalf("window=%d-%d interval=%d", metrics.WindowStart, metrics.WindowEnd, metrics.SampleIntervalMS)
	}
	if metrics.Latest == nil || metrics.Latest.UptimeS != rowCount-1 {
		t.Fatalf("latest=%+v", metrics.Latest)
	}
	if samples[0].TS != base {
		t.Fatalf("first ts=%d, want window start %d", samples[0].TS, base)
	}
	lastTS := base + (rowCount-1)*5000
	if samples[len(samples)-1].TS != lastTS {
		t.Fatalf("last ts=%d, want window end sample %d", samples[len(samples)-1].TS, lastTS)
	}
	for i, sample := range samples {
		expected, ok := raw[sample.UptimeS]
		if !ok || *sample != expected {
			t.Fatalf("sample[%d]=%+v is not an original row", i, sample)
		}
		if i > 0 {
			previous := samples[i-1]
			if previous.TS > sample.TS || (previous.TS == sample.TS && previous.UptimeS > sample.UptimeS) {
				t.Fatalf("samples are not in stable (ts,id) order at %d", i)
			}
		}
	}
	for _, spike := range []int64{100, 200, 300, 400, 500, 600, 17000} {
		found := false
		for _, sample := range samples {
			if sample.UptimeS == spike {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("original spike %d is missing", spike)
		}
	}
}

func TestFleetMetricsQueryIgnoresInvalidRatios(t *testing.T) {
	fixture := newServiceFixture(t)
	alice := fixture.createUser(t, "alice")
	result := fixture.enrollAgent(t, alice, "agent-host")
	const rowCount = maxMetricsSamples + 1
	ts := fixture.now - time.Minute.Milliseconds()
	rows := make([]*store.DeviceMetrics, 0, rowCount)
	for i := rowCount - 1; i >= 0; i-- {
		sample := MetricsSample{TS: ts, CPUPct: 10, MemUsed: 50, MemTotal: 100, DiskUsed: 50, DiskTotal: 100, UptimeS: int64(i)}
		if i == 2500 {
			sample.MemUsed = 1000
			sample.MemTotal = 0
			sample.DiskUsed = 1000
			sample.DiskTotal = -1
		}
		rows = append(rows, deviceMetricRowForTest(result.DeviceID, fmt.Sprintf("metric-%06d", i), sample))
	}
	deviceMetrics := query.Use(fixture.db).DeviceMetrics
	if err := deviceMetrics.WithContext(context.Background()).CreateInBatches(rows, 500); err != nil {
		t.Fatal(err)
	}

	metrics, err := fixture.service.DeviceMetrics(context.Background(), fixture.identity(alice), result.DeviceID, ts)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.SampleCount != rowCount || !metrics.Downsampled {
		t.Fatalf("metrics=%+v", metrics)
	}
	for _, sample := range metrics.Samples {
		if sample.UptimeS == 2500 {
			t.Fatalf("invalid zero/negative denominators became ratio extrema: %+v", sample)
		}
	}
}

func TestFleetMetricsQueryExcludesFutureSamples(t *testing.T) {
	fixture := newServiceFixture(t)
	alice := fixture.createUser(t, "alice")
	result := fixture.enrollAgent(t, alice, "agent-host")
	for _, sample := range []MetricsSample{
		{TS: fixture.now - time.Hour.Milliseconds(), CPUPct: 1},
		{TS: fixture.now - 5*time.Minute.Milliseconds(), CPUPct: 2},
		{TS: fixture.now + time.Hour.Milliseconds(), CPUPct: 99},
	} {
		if err := fixture.service.RecordMetrics(context.Background(), result.DeviceID, sample); err != nil {
			t.Fatal(err)
		}
	}

	metrics, err := fixture.service.DeviceMetrics(context.Background(), fixture.identity(alice), result.DeviceID, fixture.now-2*time.Hour.Milliseconds())
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics.Samples) != 2 || metrics.Samples[0].CPUPct != 1 || metrics.Samples[1].CPUPct != 2 || metrics.Latest == nil || metrics.Latest.CPUPct != 2 {
		t.Fatalf("future sample entered the frozen window: %+v", metrics)
	}

	metrics, err = fixture.service.DeviceMetrics(context.Background(), fixture.identity(alice), result.DeviceID, fixture.now-time.Minute.Milliseconds())
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics.Samples) != 0 || metrics.SampleCount != 0 || metrics.Latest == nil || metrics.Latest.CPUPct != 2 || metrics.SeriesError != "" {
		t.Fatalf("latest outside the trend window was not preserved independently: %+v", metrics)
	}
}

func TestFleetMetricsQuerySeriesErrorKeepsLatest(t *testing.T) {
	fixture := newServiceFixture(t)
	alice := fixture.createUser(t, "alice")
	result := fixture.enrollAgent(t, alice, "agent-host")
	const rowCount = 1500
	base := fixture.now - time.Hour.Milliseconds()
	rows := make([]*store.DeviceMetrics, 0, rowCount)
	for i := rowCount - 1; i >= 0; i-- {
		sample := MetricsSample{TS: base + int64(i), CPUPct: 42.5, UptimeS: int64(i)}
		rows = append(rows, deviceMetricRowForTest(result.DeviceID, fmt.Sprintf("metric-%06d", i), sample))
	}
	deviceMetrics := query.Use(fixture.db).DeviceMetrics
	if err := deviceMetrics.WithContext(context.Background()).CreateInBatches(rows, 500); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	queries := 0
	if err := fixture.db.Callback().Query().After("gorm:query").Register("test:cancel_metrics_window", func(db *gorm.DB) {
		if strings.Contains(db.Statement.SQL.String(), "device_metrics") {
			queries++
			if queries == 1 {
				sqlDB, err := db.DB()
				if err == nil {
					_ = sqlDB.Close()
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}

	metrics, err := fixture.service.DeviceMetrics(ctx, fixture.identity(alice), result.DeviceID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 {
		t.Fatalf("device metrics queries=%d, want latest, one trend batch, then failure", queries)
	}
	if metrics.Latest == nil || metrics.Latest.CPUPct != 42.5 || metrics.SeriesError == "" {
		t.Fatalf("partial metrics=%+v", metrics)
	}
	if metrics.Samples == nil || len(metrics.Samples) != 0 || metrics.SampleCount != 0 || metrics.Downsampled {
		t.Fatalf("failed trend must not claim complete samples: %+v", metrics)
	}
	if metrics.WindowStart != fixture.now-24*time.Hour.Milliseconds() || metrics.WindowEnd != fixture.now {
		t.Fatalf("window=%d-%d", metrics.WindowStart, metrics.WindowEnd)
	}
}

func TestFleetMetricsQuerySeriesErrorWithoutLatest(t *testing.T) {
	fixture := newServiceFixture(t)
	alice := fixture.createUser(t, "alice")
	result := fixture.enrollAgent(t, alice, "agent-host")
	queries := 0
	if err := fixture.db.Callback().Query().After("gorm:query").Register("test:fail_empty_metrics_window", func(db *gorm.DB) {
		if strings.Contains(db.Statement.SQL.String(), "device_metrics") {
			queries++
			if queries == 1 {
				sqlDB, err := db.DB()
				if err == nil {
					_ = sqlDB.Close()
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}

	metrics, err := fixture.service.DeviceMetrics(context.Background(), fixture.identity(alice), result.DeviceID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 || metrics.Latest != nil || metrics.SeriesError == "" {
		t.Fatalf("empty failed metrics=%+v queries=%d", metrics, queries)
	}
	if metrics.Samples == nil || len(metrics.Samples) != 0 || metrics.SampleCount != 0 || metrics.Downsampled {
		t.Fatalf("empty failure must not claim complete samples: %+v", metrics)
	}
}

func countDeviceMetrics(t *testing.T, db *gorm.DB, deviceID string) int64 {
	t.Helper()
	deviceMetrics := query.Use(db).DeviceMetrics
	count, err := deviceMetrics.WithContext(context.Background()).Where(deviceMetrics.DeviceID.Eq(deviceID)).Count()
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func countDeviceMetricsHourly(t *testing.T, db *gorm.DB, deviceID string) int64 {
	t.Helper()
	hourly := query.Use(db).DeviceMetricsHourly
	count, err := hourly.WithContext(context.Background()).Where(hourly.DeviceID.Eq(deviceID)).Count()
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func latestHourlyBucket(t *testing.T, db *gorm.DB, deviceID string) *store.DeviceMetricsHourly {
	t.Helper()
	hourly := query.Use(db).DeviceMetricsHourly
	row, err := hourly.WithContext(context.Background()).
		Where(hourly.DeviceID.Eq(deviceID)).
		Order(hourly.BucketTS.Desc()).
		First()
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func hourlyBucketAt(t *testing.T, db *gorm.DB, deviceID string, bucketTS int64) *store.DeviceMetricsHourly {
	t.Helper()
	hourly := query.Use(db).DeviceMetricsHourly
	row, err := hourly.WithContext(context.Background()).
		Where(hourly.DeviceID.Eq(deviceID), hourly.BucketTS.Eq(bucketTS)).
		First()
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestFleetMetricsRollup(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.now = (fixture.now / metricsHourMS) * metricsHourMS
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

	if rawCount := countDeviceMetrics(t, fixture.db, result.DeviceID); rawCount != 1 {
		t.Fatalf("raw rows after rollup=%d", rawCount)
	}
	bucket := latestHourlyBucket(t, fixture.db, result.DeviceID)
	if bucket.SampleCount != 3 || *bucket.CPUPct != 20 || *bucket.MemUsed != 100 || *bucket.UptimeS != 5000 {
		t.Fatalf("hourly bucket ts=%d cpu=%v mem=%d uptime=%d count=%d", bucket.BucketTS, *bucket.CPUPct, *bucket.MemUsed, *bucket.UptimeS, bucket.SampleCount)
	}
	if hourlyCount := countDeviceMetricsHourly(t, fixture.db, result.DeviceID); hourlyCount != 1 {
		t.Fatalf("hourly rows=%d (ancient bucket must be purged)", hourlyCount)
	}
}

func TestFleetMetricsRollupBoundaryBucket(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.now = (fixture.now / metricsHourMS) * metricsHourMS
	alice := fixture.createUser(t, "alice")
	result := fixture.enrollAgent(t, alice, "agent-host")

	bucketStart := fixture.now - 50*metricsHourMS
	for i, cpu := range []float64{10, 20, 40} {
		sample := MetricsSample{TS: bucketStart + int64(10+i*20)*time.Minute.Milliseconds(), CPUPct: cpu, MemUsed: 100, MemTotal: 200, DiskUsed: 300, DiskTotal: 400, UptimeS: 5000}
		if err := fixture.service.RecordMetrics(context.Background(), result.DeviceID, sample); err != nil {
			t.Fatal(err)
		}
	}
	recent := MetricsSample{TS: fixture.now - 30*time.Minute.Milliseconds(), CPUPct: 55}
	if err := fixture.service.RecordMetrics(context.Background(), result.DeviceID, recent); err != nil {
		t.Fatal(err)
	}

	fixture.now = bucketStart + 48*time.Hour.Milliseconds() + 40*time.Minute.Milliseconds()
	if err := fixture.service.RollupMetrics(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hourly := countDeviceMetricsHourly(t, fixture.db, result.DeviceID); hourly != 0 {
		t.Fatalf("boundary bucket must wait for a complete hour, hourly=%d", hourly)
	}
	if raw := countDeviceMetrics(t, fixture.db, result.DeviceID); raw != 4 {
		t.Fatalf("boundary rows must stay raw, raw=%d", raw)
	}

	fixture.now += 10 * time.Minute.Milliseconds()
	if err := fixture.service.RollupMetrics(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hourly := countDeviceMetricsHourly(t, fixture.db, result.DeviceID); hourly != 0 {
		t.Fatalf("cutoff advanced inside the same bucket, hourly=%d", hourly)
	}
	if raw := countDeviceMetrics(t, fixture.db, result.DeviceID); raw != 4 {
		t.Fatalf("boundary rows must stay raw, raw=%d", raw)
	}

	fixture.now = bucketStart + 49*metricsHourMS + 10*time.Minute.Milliseconds()
	if err := fixture.service.RollupMetrics(context.Background()); err != nil {
		t.Fatal(err)
	}
	if raw := countDeviceMetrics(t, fixture.db, result.DeviceID); raw != 1 {
		t.Fatalf("completed bucket must be deleted from raw, raw=%d", raw)
	}
	bucket := hourlyBucketAt(t, fixture.db, result.DeviceID, bucketStart)
	if bucket.SampleCount != 3 || *bucket.CPUPct < 23.32 || *bucket.CPUPct > 23.34 || *bucket.MemUsed != 100 || *bucket.UptimeS != 5000 {
		t.Fatalf("hourly bucket cpu=%v mem=%d uptime=%d count=%d (samples lost across batches)", *bucket.CPUPct, *bucket.MemUsed, *bucket.UptimeS, bucket.SampleCount)
	}

	if err := fixture.service.RollupMetrics(context.Background()); err != nil {
		t.Fatalf("rollup must be idempotent: %v", err)
	}
	if raw := countDeviceMetrics(t, fixture.db, result.DeviceID); raw != 1 {
		t.Fatalf("idempotent rerun changed raw rows: %d", raw)
	}
	recheck := hourlyBucketAt(t, fixture.db, result.DeviceID, bucketStart)
	if recheck.SampleCount != bucket.SampleCount || *recheck.CPUPct != *bucket.CPUPct {
		t.Fatalf("idempotent rerun overwrote bucket: count=%d cpu=%v", recheck.SampleCount, *recheck.CPUPct)
	}
}

func TestFleetMetricsQueryClamp(t *testing.T) {
	fixture := newServiceFixture(t)
	alice := fixture.createUser(t, "alice")
	result := fixture.enrollAgent(t, alice, "agent-host")
	since := fixture.now - time.Hour.Milliseconds()
	for _, sample := range []MetricsSample{
		{TS: fixture.now - 49*time.Hour.Milliseconds(), CPUPct: 1},
		{TS: fixture.now - 47*time.Hour.Milliseconds(), CPUPct: 2},
		{TS: since, CPUPct: 3},
		{TS: fixture.now - 30*time.Minute.Milliseconds(), CPUPct: 4},
	} {
		if err := fixture.service.RecordMetrics(context.Background(), result.DeviceID, sample); err != nil {
			t.Fatal(err)
		}
	}

	metrics, err := fixture.service.DeviceMetrics(context.Background(), fixture.identity(alice), result.DeviceID, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics.Samples) != 2 || metrics.Samples[0].CPUPct != 3 || metrics.Samples[1].CPUPct != 4 || metrics.SampleCount != 2 {
		t.Fatalf("since must be inclusive and exclude older samples: %+v", metrics)
	}
	if metrics.WindowStart != since || metrics.WindowEnd != fixture.now {
		t.Fatalf("window=%d-%d", metrics.WindowStart, metrics.WindowEnd)
	}

	metrics, err = fixture.service.DeviceMetrics(context.Background(), fixture.identity(alice), result.DeviceID, fixture.now-7*24*time.Hour.Milliseconds())
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics.Samples) != 3 || metrics.Samples[0].CPUPct != 2 || metrics.Samples[1].CPUPct != 3 || metrics.Samples[2].CPUPct != 4 || metrics.SampleCount != 3 {
		t.Fatalf("since must clamp to raw retention: %+v", metrics)
	}
	if metrics.WindowStart != fixture.now-rawMetricsRetention.Milliseconds() {
		t.Fatalf("retention window start=%d", metrics.WindowStart)
	}
}

func TestFleetCascadeDeleteUser(t *testing.T) {
	fixture := newServiceFixture(t)
	alice := fixture.createUser(t, "alice")
	result := fixture.enrollAgent(t, alice, "agent-host")
	if err := fixture.service.RecordMetrics(context.Background(), result.DeviceID, MetricsSample{CPUPct: 1}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	appUser := query.Use(fixture.db).AppUser
	if _, err := appUser.WithContext(ctx).Where(appUser.ID.Eq(alice.ID)).Delete(); err != nil {
		t.Fatal(err)
	}
	userDevice := query.Use(fixture.db).UserDevice
	if count, err := userDevice.WithContext(ctx).Where(userDevice.ID.Eq(result.DeviceID)).Count(); err != nil || count != 0 {
		t.Fatalf("user_device rows=%d err=%v after user delete", count, err)
	}
	deviceAgent := query.Use(fixture.db).DeviceAgent
	if count, err := deviceAgent.WithContext(ctx).Where(deviceAgent.DeviceID.Eq(result.DeviceID)).Count(); err != nil || count != 0 {
		t.Fatalf("device_agent rows=%d err=%v after user delete", count, err)
	}
	deviceMetrics := query.Use(fixture.db).DeviceMetrics
	if count, err := deviceMetrics.WithContext(ctx).Where(deviceMetrics.DeviceID.Eq(result.DeviceID)).Count(); err != nil || count != 0 {
		t.Fatalf("device_metrics rows=%d err=%v after user delete", count, err)
	}
	syncCredential := query.Use(fixture.db).SyncCredential
	if count, err := syncCredential.WithContext(ctx).Where(syncCredential.DeviceID.Eq(result.DeviceID)).Count(); err != nil || count != 0 {
		t.Fatalf("sync_credential rows=%d err=%v after user delete", count, err)
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

func metricsBenchmarkPercentile(values []float64, percentile int) float64 {
	index := (percentile*len(values) + 99) / 100
	if index < 1 {
		index = 1
	}
	return values[index-1]
}

func BenchmarkDeviceMetricsWindow(b *testing.B) {
	type Scenario struct {
		name            string
		window          time.Duration
		rows            int
		irregular       bool
		returnedSamples int
		responseBytes   int
	}
	type Result struct {
		Scenario        string    `json:"scenario"`
		Dialect         string    `json:"dialect"`
		GoVersion       string    `json:"goVersion"`
		GOOS            string    `json:"goos"`
		GOARCH          string    `json:"goarch"`
		WindowHours     int64     `json:"windowHours"`
		Rows            int       `json:"rows"`
		Warmups         int       `json:"warmups"`
		Samples         int       `json:"samples"`
		DurationsMS     []float64 `json:"durationsMs"`
		AllocationsMiB  []float64 `json:"allocationsMiB"`
		DurationP50MS   float64   `json:"durationP50Ms"`
		DurationP95MS   float64   `json:"durationP95Ms"`
		AllocP50MiB     float64   `json:"allocP50MiB"`
		AllocP95MiB     float64   `json:"allocP95MiB"`
		ResponseBytes   int       `json:"responseBytes"`
		ReturnedSamples int       `json:"returnedSamples"`
		MaxIntervalMS   int64     `json:"maxIntervalMs"`
		Measurement     string    `json:"measurement"`
		AllocationNote  string    `json:"allocationNote"`
		Command         string    `json:"command"`
		GeneratedAt     string    `json:"generatedAt"`
	}
	scenarios := []Scenario{
		{name: "1hz-24h", window: 24 * time.Hour, rows: 86400, returnedSamples: 1991, responseBytes: 224382},
		{name: "1hz-48h", window: 48 * time.Hour, rows: 172800, returnedSamples: 1991, responseBytes: 225349},
		{name: "irregular-24h", window: 24 * time.Hour, rows: 86400, irregular: true, returnedSamples: 1991, responseBytes: 224381},
	}
	output := os.Getenv("NEXTERM_METRICS_BENCHMARK_OUTPUT")
	if output == "" {
		output = ".tmp/fleet-metrics-window-benchmark.json"
	}
	command := fmt.Sprintf("NEXTERM_METRICS_BENCHMARK_OUTPUT=%s go test -mod=readonly ./internal/fleet/server -run '^$' -bench '^BenchmarkDeviceMetricsWindow$' -benchtime=1x -count=1", output)
	results := make([]Result, 0, len(scenarios))
	for _, scenario := range scenarios {
		scenario := scenario
		b.Run(scenario.name, func(b *testing.B) {
			now := time.Now().UnixMilli()
			windowStart := now - scenario.window.Milliseconds()
			fixture := newServiceFixture(b)
			fixture.now = now
			owner := fixture.createUser(b, "benchmark-owner")
			device := fixture.enrollAgent(b, owner, "benchmark-device")
			rows := make([]*store.DeviceMetrics, 0, scenario.rows)
			previousTS := windowStart
			totalOffset := int64(scenario.rows-1) * 1000
			if scenario.irregular {
				totalOffset = 0
				for i := 1; i < scenario.rows; i++ {
					step := int64(1000)
					if i%5000 == 0 {
						step += 10000
					} else if i%1000 == 0 {
						step -= 500
					}
					totalOffset += step
				}
			}
			offset := int64(0)
			maxInterval := int64(1000)
			duplicates := 0
			for i := 0; i < scenario.rows; i++ {
				ts := windowStart + int64(i)*1000
				if scenario.irregular {
					ts = windowStart + offset*scenario.window.Milliseconds()/totalOffset
					if i > 0 {
						step := int64(1000)
						if i%5000 == 0 {
							step += 10000
						} else if i%1000 == 0 {
							step -= 500
						}
						offset += step
						if i%1000 == 0 {
							ts = previousTS
							duplicates++
						}
					}
				}
				sample := MetricsSample{TS: ts, CPUPct: float64(i % 100), MemUsed: 50, MemTotal: 100, DiskUsed: 25, DiskTotal: 100, UptimeS: int64(i)}
				rows = append(rows, deviceMetricRowForTest(device.DeviceID, fmt.Sprintf("metric-%06d", i), sample))
				if i > 0 && ts-previousTS > maxInterval {
					maxInterval = ts - previousTS
				}
				previousTS = ts
			}
			if last := rows[len(rows)-1]; last.TS != now {
				if now-previousTS > maxInterval {
					maxInterval = now - previousTS
				}
				last.TS = now
				previousTS = now
			}
			if previousTS != now {
				b.Fatalf("last fixture ts=%d, want %d", previousTS, now)
			}
			if scenario.irregular && (duplicates == 0 || maxInterval < 10000) {
				b.Fatalf("invalid irregular fixture: duplicates=%d maxInterval=%d", duplicates, maxInterval)
			}
			deviceMetrics := query.Use(fixture.db).DeviceMetrics
			if err := deviceMetrics.WithContext(context.Background()).CreateInBatches(rows, 500); err != nil {
				b.Fatal(err)
			}

			ctx := context.Background()
			var result *DeviceMetricsResult
			var err error
			for i := 0; i < 5; i++ {
				result, err = fixture.service.DeviceMetrics(ctx, fixture.identity(owner), device.DeviceID, windowStart)
				if err != nil {
					b.Fatal(err)
				}
			}
			durations := make([]float64, 0, 30)
			allocations := make([]float64, 0, 30)
			responseBytes := 0
			for i := 0; i < 30; i++ {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				started := time.Now()
				result, err = fixture.service.DeviceMetrics(ctx, fixture.identity(owner), device.DeviceID, windowStart)
				elapsed := time.Since(started)
				runtime.ReadMemStats(&after)
				if err != nil {
					b.Fatal(err)
				}
				if result.SampleCount != int64(scenario.rows) || result.SeriesError != "" || len(result.Samples) != scenario.returnedSamples {
					b.Fatalf("incomplete benchmark result: count=%d returned=%d want=%d seriesError=%q", result.SampleCount, len(result.Samples), scenario.returnedSamples, result.SeriesError)
				}
				durations = append(durations, float64(elapsed)/float64(time.Millisecond))
				allocations = append(allocations, float64(after.TotalAlloc-before.TotalAlloc)/(1024*1024))
				encoded, err := json.Marshal(result)
				if err != nil {
					b.Fatal(err)
				}
				responseBytes = len(encoded)
				if responseBytes != scenario.responseBytes {
					b.Fatalf("response bytes=%d, want unchanged contract size %d", responseBytes, scenario.responseBytes)
				}
			}
			sortedDurations := append([]float64(nil), durations...)
			sort.Float64s(sortedDurations)
			sortedAllocations := append([]float64(nil), allocations...)
			sort.Float64s(sortedAllocations)
			resultRecord := Result{
				Scenario: scenario.name, Dialect: "sqlite", GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
				WindowHours: scenario.window.Milliseconds() / time.Hour.Milliseconds(), Rows: scenario.rows, Warmups: 5, Samples: 30,
				DurationsMS: durations, AllocationsMiB: allocations,
				DurationP50MS: metricsBenchmarkPercentile(sortedDurations, 50), DurationP95MS: metricsBenchmarkPercentile(sortedDurations, 95),
				AllocP50MiB: metricsBenchmarkPercentile(sortedAllocations, 50), AllocP95MiB: metricsBenchmarkPercentile(sortedAllocations, 95),
				ResponseBytes: responseBytes, ReturnedSamples: len(result.Samples), MaxIntervalMS: maxInterval,
				Measurement:    "DeviceMetrics wall time excluding JSON serialization",
				AllocationNote: "TotalAlloc delta per call, excluding JSON serialization", Command: command, GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			}
			results = append(results, resultRecord)
			b.ReportMetric(resultRecord.DurationP50MS, "duration_p50_ms")
			b.ReportMetric(resultRecord.DurationP95MS, "duration_p95_ms")
			b.ReportMetric(resultRecord.AllocP95MiB, "alloc_p95_MiB")
			b.ReportMetric(float64(resultRecord.ResponseBytes), "response_bytes")
			if resultRecord.ResponseBytes > 1024*1024 {
				b.Errorf("response P95 contract: bytes=%d, want <= %d", resultRecord.ResponseBytes, 1024*1024)
			}
			if scenario.window == 24*time.Hour {
				if resultRecord.DurationP95MS > 500 {
					b.Errorf("24h duration P95=%.3fms, want <= 500ms", resultRecord.DurationP95MS)
				}
				if resultRecord.AllocP95MiB > 16 {
					b.Errorf("24h allocation P95=%.3fMiB, want <= 16MiB", resultRecord.AllocP95MiB)
				}
			}
		})
	}
	encoded, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		b.Fatal(err)
	}
	outputPath := output
	if !filepath.IsAbs(outputPath) {
		outputPath = filepath.Join("..", "..", "..", outputPath)
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(outputPath, append(encoded, '\n'), 0o600); err != nil {
		b.Fatal(err)
	}
	b.Logf("benchmark results written to %s", outputPath)
}
