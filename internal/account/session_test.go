package account

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
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

func TestSessionListLastSeenAndScopedRevoke(t *testing.T) {
	a, now := testAccounts(t)
	ctx := context.Background()
	owner, err := a.CreateUser(ctx, "session-owner", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	other, err := a.CreateUser(ctx, "other-owner", "", "synthetic-password-2")
	if err != nil {
		t.Fatal(err)
	}
	token, current, err := a.IssueSessionWithMetadata(ctx, owner.ID, "", SessionMetadata{ClientName: "NexTerm 桌面端", Origin: "192.0.2.10"})
	if err != nil {
		t.Fatal(err)
	}
	legacyToken, legacy, err := a.IssueSession(ctx, owner.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	otherToken, otherSession, err := a.IssueSession(ctx, other.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	listed, err := a.ListUserSessions(ctx, owner.ID)
	if err != nil || len(listed) != 2 {
		t.Fatalf("owner sessions = %+v, %v", listed, err)
	}
	for _, info := range listed {
		if info.ID == current.ID && (info.ClientName != "NexTerm 桌面端" || info.Origin != "192.0.2.10" || info.LastSeenAt != current.TouchedAt) {
			t.Fatalf("current session metadata = %+v", info)
		}
		if info.ID == legacy.ID && (info.ClientName != "" || info.Origin != "") {
			t.Fatalf("legacy session metadata = %+v", info)
		}
		if info.ID == otherSession.ID {
			t.Fatal("other user's session was listed")
		}
	}
	*now += sessionSlidePersist.Milliseconds() - 1
	if _, err := a.ValidateSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	*now += 1
	if _, err := a.ValidateSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	listed, err = a.ListUserSessions(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range listed {
		if info.ID == current.ID && info.LastSeenAt != *now {
			t.Fatalf("last seen did not advance after validated activity: %+v", info)
		}
	}
	requireCode(t, a.RevokeUserSession(ctx, owner.ID, otherSession.ID), ipc.CodeNotFound)
	if _, err := a.ValidateSession(ctx, otherToken); err != nil {
		t.Fatalf("another user's session was revoked: %v", err)
	}
	if err := a.RevokeUserSession(ctx, owner.ID, legacy.ID); err != nil {
		t.Fatal(err)
	}
	requireCode(t, validateErr(a, ctx, legacyToken), ipc.CodeForbidden)
	listed, err = a.ListUserSessions(ctx, owner.ID)
	if err != nil || len(listed) != 1 || listed[0].ID != current.ID {
		t.Fatalf("sessions after revoke = %+v, %v", listed, err)
	}
}

func TestRegisteredDeviceLastSeenFollowsValidatedSession(t *testing.T) {
	a, now := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "device-activity", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	active, err := a.RegisterDevice(ctx, user.ID, "active laptop", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	idle, err := a.RegisterDevice(ctx, user.ID, "idle laptop", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	token, session, err := a.IssueSession(ctx, user.ID, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkLastSeen := func(deviceID string, want int64) {
		t.Helper()
		device, err := a.GetDevice(ctx, deviceID)
		if err != nil {
			t.Fatal(err)
		}
		if device.LastSeenAt != want {
			t.Fatalf("device %s last seen = %d, want %d", deviceID, device.LastSeenAt, want)
		}
	}
	issuedAt := *now
	checkLastSeen(active.ID, issuedAt)
	checkLastSeen(idle.ID, 0)

	*now += sessionSlidePersist.Milliseconds() - 1
	if _, err := a.ValidateSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	checkLastSeen(active.ID, issuedAt)
	*now++
	if _, err := a.ValidateSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	checkLastSeen(active.ID, *now)
	checkLastSeen(idle.ID, 0)

	if err := a.RevokeSession(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	lastValidAt := *now
	*now += sessionSlidePersist.Milliseconds()
	requireCode(t, validateErr(a, ctx, token), ipc.CodeForbidden)
	checkLastSeen(active.ID, lastValidAt)
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
	if err := a.db.Raw("SELECT touched_at, expires_at FROM user_session WHERE id = ?", session.ID).Row().Scan(&touched, &expires); err != nil {
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
	if err := a.db.Raw("SELECT expires_at FROM user_session WHERE id = ?", session.ID).Row().Scan(&expires); err != nil {
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

	if err := a.db.Exec("UPDATE app_user SET state = ? WHERE id = ?", string(StateDisabled), user.ID).Error; err != nil {
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

func TestSessionRevokedDeviceBackstop(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "tess", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	device, err := a.RegisterDevice(ctx, user.ID, "laptop", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := a.IssueSession(ctx, user.ID, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.RevokeDevice(ctx, user.ID, device.ID); err != nil {
		t.Fatal(err)
	}
	requireCode(t, validateErr(a, ctx, token), ipc.CodeForbidden)

	now := a.now()
	if err := a.db.Exec(`INSERT INTO user_session(id, user_id, device_id, token_hash, created_at, touched_at, expires_at)
VALUES('sneaky-session', ?, ?, ?, ?, ?, ?)`, user.ID, device.ID, sessionTokenHash("sneaky-token"), now, now, now+SessionSlidingTTL.Milliseconds()).Error; err != nil {
		t.Fatal(err)
	}
	requireCode(t, validateErr(a, ctx, "sneaky-token"), ipc.CodeForbidden)
}

func TestIssueSessionConcurrentRevoke(t *testing.T) {
	a := testFileAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "uma", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	device, err := a.RegisterDevice(ctx, user.ID, "laptop", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	const issuers = 4
	var mu sync.Mutex
	var tokens []string
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < issuers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				token, _, err := a.IssueSession(ctx, user.ID, device.ID)
				if err != nil {
					return
				}
				mu.Lock()
				tokens = append(tokens, token)
				mu.Unlock()
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	if err := a.RevokeDevice(ctx, user.ID, device.ID); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()

	mu.Lock()
	issued := append([]string(nil), tokens...)
	mu.Unlock()
	if len(issued) == 0 {
		t.Fatal("no sessions issued during concurrency window")
	}
	for _, token := range issued {
		if _, err := a.ValidateSession(ctx, token); err == nil {
			t.Fatal("session outlived device revocation")
		}
	}
	if _, _, err := a.IssueSession(ctx, user.ID, device.ID); err == nil {
		t.Fatal("issue after revoke accepted")
	}
}

func TestRevokeSessionConcurrentSingleWinner(t *testing.T) {
	a := testFileAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "uma2", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	const revokers = 8
	var wg sync.WaitGroup
	errs := make([]error, revokers)
	for i := 0; i < revokers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = a.RevokeSession(ctx, session.ID)
		}(i)
	}
	wg.Wait()
	succeeded := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		requireCode(t, err, ipc.CodeNotFound)
	}
	if succeeded != 1 {
		t.Fatalf("concurrent revoke succeeded %d times, want exactly 1", succeeded)
	}
}

func validateErr(a *Accounts, ctx context.Context, token string) error {
	_, err := a.ValidateSession(ctx, token)
	return err
}

func issueErr(a *Accounts, ctx context.Context, userID, deviceID string) error {
	_, _, err := a.IssueSession(ctx, userID, deviceID)
	return err
}
