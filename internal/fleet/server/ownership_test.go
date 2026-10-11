package fleetserver

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
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
	var ownerID string
	if err := fixture.service.db.QueryRowContext(context.Background(), "SELECT owner_id FROM audit_log WHERE kind = ? ORDER BY id DESC LIMIT 1", auditKindDeviceEnroll).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if ownerID != owner.ID {
		t.Fatalf("audit owner = %q, want %q", ownerID, owner.ID)
	}
}
