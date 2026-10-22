package account

import (
	"context"
	"testing"
)

func requireRevokedSessionIDs(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("revoked session IDs = %v, want %v", got, want)
	}
	set := make(map[string]bool, len(got))
	for _, id := range got {
		set[id] = true
	}
	for _, id := range want {
		if !set[id] {
			t.Fatalf("revoked session IDs = %v, missing %s", got, id)
		}
	}
}

func TestRevokeUserSessionsWithIDsReturnsActualSet(t *testing.T) {
	a, now := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "revoke-actual-set", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	expiredToken, expired, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	*now += SessionSlidingTTL.Milliseconds() + 1
	activeToken, active, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	listed, err := a.ListUserSessions(ctx, user.ID)
	if err != nil || len(listed) != 1 || listed[0].ID != active.ID {
		t.Fatalf("active-only sessions = %+v, %v", listed, err)
	}
	afterListToken, afterList, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	ids, err := a.RevokeUserSessionsWithIDs(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	requireRevokedSessionIDs(t, ids, expired.ID, active.ID, afterList.ID)
	for name, token := range map[string]string{"expired": expiredToken, "active": activeToken, "after list": afterListToken} {
		if _, err := a.ValidateSession(ctx, token); err == nil {
			t.Fatalf("%s session remains valid after revocation", name)
		}
	}
	freshToken, _, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ValidateSession(ctx, freshToken); err != nil {
		t.Fatalf("session created after revocation was revoked: %v", err)
	}
}

func TestRevokeUserSessionsWithIDsRollback(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "revoke-rollback", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	firstToken, _, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	secondToken, _, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.Exec(`CREATE TRIGGER fail_revoke_user_sessions BEFORE UPDATE ON user_session
	BEGIN
		SELECT RAISE(ABORT, 'injected revoke failure');
	END`).Error; err != nil {
		t.Fatal(err)
	}
	ids, err := a.RevokeUserSessionsWithIDs(ctx, user.ID, "")
	if err == nil || ids != nil {
		t.Fatalf("failed revocation IDs = %v, err = %v", ids, err)
	}
	for _, token := range []string{firstToken, secondToken} {
		if _, err := a.ValidateSession(ctx, token); err != nil {
			t.Fatalf("failed revocation changed session validity: %v", err)
		}
	}
	if err := a.db.Exec("DROP TRIGGER fail_revoke_user_sessions").Error; err != nil {
		t.Fatal(err)
	}
	ids, err = a.RevokeUserSessionsWithIDs(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("revoked IDs after rollback retry = %v", ids)
	}
}

func TestDatabaseRevokeUserSessionsWithIDsReturnsActualSet(t *testing.T) {
	a, now := testDatabaseAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "database-revoke-actual-set", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	_, expired, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	*now += SessionSlidingTTL.Milliseconds() + 1
	_, active, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	listed, err := a.ListUserSessions(ctx, user.ID)
	if err != nil || len(listed) != 1 || listed[0].ID != active.ID {
		t.Fatalf("active-only sessions = %+v, %v", listed, err)
	}
	_, afterList, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	ids, err := a.RevokeUserSessionsWithIDs(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	requireRevokedSessionIDs(t, ids, expired.ID, active.ID, afterList.ID)
}
