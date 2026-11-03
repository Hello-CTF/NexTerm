package fleetserver

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

func TestWithIdentityInjectsStoreScope(t *testing.T) {
	request := withIdentity(httptest.NewRequest("GET", "/fleet/devices", nil), &account.Identity{UserID: "user-a", Role: account.RoleSuperadmin})
	userID, ok := ipc.UserIDFromContext(request.Context())
	if !ok || userID != "user-a" {
		t.Fatalf("user ID = %q, ok=%v", userID, ok)
	}
	role, ok := ipc.RoleFromContext(request.Context())
	if !ok || role != string(account.RoleSuperadmin) {
		t.Fatalf("role = %q, ok=%v", role, ok)
	}
}

func TestEnrollAuditUsesAccountOwner(t *testing.T) {
	fixture := newHTTPFixture(t, false)
	owner := fixture.createUser(t, "owner")
	enrollAgent(t, fixture, owner, "box")
	audit := query.Use(fixture.service.db).AuditRow
	row, err := audit.WithContext(context.Background()).Where(audit.Kind.Eq(auditKindDeviceEnroll)).Order(audit.ID.Desc()).First()
	if err != nil {
		t.Fatal(err)
	}
	if row.OwnerID != owner.ID {
		t.Fatalf("audit owner = %q, want %q", row.OwnerID, owner.ID)
	}
}

func TestDevicePresenceAuditUsesDeviceOwner(t *testing.T) {
	fixture := newHTTPFixture(t, false)
	owner := fixture.createUser(t, "presence-owner")
	deviceID, _ := enrollAgent(t, fixture, owner, "presence-box")
	fixture.service.deviceOnline(deviceID)
	fixture.service.deviceOffline(deviceID)
	audit := query.Use(fixture.service.db).AuditRow
	for _, kind := range []string{auditKindDeviceOnline, auditKindDeviceOffline} {
		row, err := audit.WithContext(context.Background()).Where(audit.Kind.Eq(kind)).Order(audit.ID.Desc()).First()
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if row.OwnerID != owner.ID {
			t.Fatalf("%s audit owner = %q, want %q", kind, row.OwnerID, owner.ID)
		}
	}
}

func TestFailoverAuditUsesDeviceOwner(t *testing.T) {
	fixture := newHTTPFixture(t, false)
	owner := fixture.createUser(t, "failover-audit-owner")
	deviceID, secret := enrollAgent(t, fixture, owner, "failover-audit-box")
	response := fixture.call(t, "POST", "/agent/current-url", map[string]any{
		"device_id": deviceID, "secret": secret, "url": "https://backup.example.com", "reason": "failover",
	}, nil, "")
	if response.status != 200 {
		t.Fatalf("current-url: HTTP %d %v", response.status, response.body)
	}
	audit := query.Use(fixture.service.db).AuditRow
	row, err := audit.WithContext(context.Background()).Where(audit.Kind.Eq(auditKindDeviceFailover)).Order(audit.ID.Desc()).First()
	if err != nil {
		t.Fatal(err)
	}
	if row.OwnerID != owner.ID {
		t.Fatalf("failover audit owner = %q, want %q", row.OwnerID, owner.ID)
	}
}
