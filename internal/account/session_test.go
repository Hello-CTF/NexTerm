package account

import (
	"context"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestSessionIssueValidateRevoke(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "dave", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	token, session, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := a.ValidateSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if identity.UserID != user.ID || identity.SessionID != session.ID || identity.Role != RoleUser {
		t.Fatalf("unexpected identity: %+v", identity)
	}
	requireCode(t, validateErr(a, ctx, "not-a-token"), ipc.CodeForbidden)
	if err := a.RevokeSession(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	requireCode(t, validateErr(a, ctx, token), ipc.CodeForbidden)
	requireCode(t, a.RevokeSession(ctx, session.ID), ipc.CodeNotFound)
}

func TestSessionSlidingAndAbsoluteExpiry(t *testing.T) {
	a, now := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "erin", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	token, session, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	*now += 11 * time.Hour.Milliseconds()
	if _, err := a.ValidateSession(ctx, token); err != nil {
		t.Fatalf("validate at +11h: %v", err)
	}
	var touched, expires int64
	if err := a.db.QueryRowContext(ctx, "SELECT touched_at, expires_at FROM user_session WHERE id = ?", session.ID).Scan(&touched, &expires); err != nil {
		t.Fatal(err)
	}
	if touched != *now || expires != *now+SessionSlidingTTL.Milliseconds() {
		t.Fatalf("slide did not persist: touched=%d expires=%d", touched, expires)
	}

	for round := 0; round < 14; round++ {
		*now += 11 * time.Hour.Milliseconds()
		if _, err := a.ValidateSession(ctx, token); err != nil {
			t.Fatalf("keep-alive round %d at +%dh: %v", round, 11*(round+2), err)
		}
	}
	absolute := session.CreatedAt + SessionAbsoluteTTL.Milliseconds()
	if err := a.db.QueryRowContext(ctx, "SELECT expires_at FROM user_session WHERE id = ?", session.ID).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if expires != absolute {
		t.Fatalf("sliding expiry not capped by absolute TTL: expires=%d absolute=%d", expires, absolute)
	}
	*now = absolute
	requireCode(t, validateErr(a, ctx, token), ipc.CodeForbidden)

	lapsed, _, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	*now += SessionSlidingTTL.Milliseconds() + 1
	requireCode(t, validateErr(a, ctx, lapsed), ipc.CodeForbidden)
}

func TestSessionRevokeOthersAndDisabledUser(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "fred", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	token1, session1, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	token2, _, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	affected, err := a.RevokeUserSessions(ctx, user.ID, session1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if affected != 1 {
		t.Fatalf("affected=%d", affected)
	}
	if _, err := a.ValidateSession(ctx, token1); err != nil {
		t.Fatalf("kept session revoked: %v", err)
	}
	requireCode(t, validateErr(a, ctx, token2), ipc.CodeForbidden)

	if _, err := a.db.ExecContext(ctx, "UPDATE user SET state = ? WHERE id = ?", string(StateDisabled), user.ID); err != nil {
		t.Fatal(err)
	}
	requireCode(t, validateErr(a, ctx, token1), ipc.CodeForbidden)
}

func TestSessionDeviceOwnership(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	owner, err := a.CreateUser(ctx, "gina", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	other, err := a.CreateUser(ctx, "harry", "", "synthetic-password-2")
	if err != nil {
		t.Fatal(err)
	}
	device, err := a.RegisterDevice(ctx, owner.ID, "laptop", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	token, identity, err := a.issueWithIdentity(ctx, owner.ID, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if identity.DeviceID != device.ID {
		t.Fatalf("identity device=%q", identity.DeviceID)
	}
	requireCode(t, issueErr(a, ctx, other.ID, device.ID), ipc.CodeForbidden)
	if err := a.RevokeDevice(ctx, owner.ID, device.ID); err != nil {
		t.Fatal(err)
	}
	requireCode(t, issueErr(a, ctx, owner.ID, device.ID), ipc.CodeForbidden)
	requireCode(t, validateErr(a, ctx, token), ipc.CodeForbidden)
}

func (a *Accounts) issueWithIdentity(ctx context.Context, userID, deviceID string) (string, *Identity, error) {
	token, _, err := a.IssueSession(ctx, userID, deviceID)
	if err != nil {
		return "", nil, err
	}
	identity, err := a.ValidateSession(ctx, token)
	return token, identity, err
}

func validateErr(a *Accounts, ctx context.Context, token string) error {
	_, err := a.ValidateSession(ctx, token)
	return err
}

func issueErr(a *Accounts, ctx context.Context, userID, deviceID string) error {
	_, _, err := a.IssueSession(ctx, userID, deviceID)
	return err
}
