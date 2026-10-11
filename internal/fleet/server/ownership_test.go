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
