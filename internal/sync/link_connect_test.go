package sync

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func linkPatch(url, username, password string) LinkPatch {
	return LinkPatch{URL: testPtr(url), Username: testPtr(username), Password: testPtr(password)}
}

func TestLinkSetConnectedCachesSession(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)

	result, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "alice-pw-123"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Connect != LinkConnectConnected {
		t.Fatalf("connect = %q, want connected", result.Connect)
	}
	if result.VerifiedAt == 0 || result.LastError != "" {
		t.Fatalf("probe not recorded: %+v", result.Link)
	}

	// The session was already established during LinkSet: remote calls do not re-login.
	if _, err := instance.service.Remote().ListDevices(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fixture.logins.Load(); got != 1 {
		t.Fatalf("logins = %d, want 1 (LinkSet probe session reused)", got)
	}
}

func TestLinkSetBadPasswordRecordsError(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)

	// Save first, verify second: a failed probe does not roll back the save and does not error; the state is empty and LastError is persisted.
	result, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "wrong-pw"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Connect != "" || result.LastError == "" {
		t.Fatalf("result = %+v, want empty connect with recorded error", result)
	}

	link, err := instance.service.LinkGet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !link.IsConfigured() || link.LastError == "" {
		t.Fatalf("link = %+v, want configured with recorded error", link)
	}
}

func TestLinkMFAFlow(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	secret := fixture.bindTOTP(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	ctx := context.Background()

	result, err := instance.service.LinkSet(ctx, linkPatch(fixture.URL, "alice", "alice-pw-123"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Connect != LinkConnectMFARequired || result.MFATicket == "" || result.MFAExpiresAt == 0 {
		t.Fatalf("connect = %+v, want mfa_required with ticket", result)
	}

	// A wrong code is rejected without consuming the ticket: retrying the same ticket with the correct TOTP code must succeed (same semantics as the server).
	if _, err := instance.service.LinkMFA(ctx, "000000"); err == nil {
		t.Fatal("wrong TOTP code must fail")
	} else {
		requireCode(t, err, ipc.CodeForbidden)
	}
	result, err = instance.service.LinkMFA(ctx, testTOTPCode(t, secret, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if result.Connect != LinkConnectConnected {
		t.Fatalf("connect = %q, want connected", result.Connect)
	}

	// The session (cookie+CSRF) is held in memory: remote reads and CSRF writes work directly, with no re-login.
	if _, err := instance.service.Remote().ListDevices(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.service.Remote().PutBaseURLs(ctx, []RemoteBaseURLEntry{{URL: "https://a.example.com"}}); err != nil {
		t.Fatal(err)
	}
	if got := fixture.logins.Load(); got != 1 {
		t.Fatalf("logins = %d, want 1 (MFA completion reuses the ticket session without re-login)", got)
	}
}

func TestLinkResetRequiredFlow(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	userID := fixture.createUser(t, "alice", "temp-pw-123")
	if err := fixture.accounts.AdminResetUser(context.Background(), userID); err != nil {
		t.Fatal(err)
	}
	instance := newTestInstance(t, true)
	ctx := context.Background()

	result, err := instance.service.LinkSet(ctx, linkPatch(fixture.URL, "alice", "temp-pw-123"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Connect != LinkConnectResetRequired {
		t.Fatalf("connect = %q, want reset_required", result.Connect)
	}

	// Forced-password-change onboarding: a brand-new DEK envelope is issued with the new password, and the recovery key is delivered only once in the response.
	result, err = instance.service.LinkResetPassword(ctx, "brand-new-pw-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Connect != LinkConnectConnected || result.RecoveryKey == "" {
		t.Fatalf("connect = %+v, want connected with one-shot recovery key", result)
	}

	// The restricted session is kept by the server as a formal session: reads and CSRF writes work directly, with exactly one login in the whole flow.
	if _, err := instance.service.Remote().ListDevices(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.service.Remote().PutBaseURLs(ctx, []RemoteBaseURLEntry{{URL: "https://a.example.com"}}); err != nil {
		t.Fatal(err)
	}
	if got := fixture.logins.Load(); got != 1 {
		t.Fatalf("logins = %d, want 1 (reset completion reuses the restricted session without re-login)", got)
	}

	// The intermediate state is consumed: submitting a new password again must be rejected.
	if _, err := instance.service.LinkResetPassword(ctx, "another-pw-1"); err == nil {
		t.Fatal("consumed reset state must reject a second password")
	} else {
		requireCode(t, err, ipc.CodeBadParam)
	}

	// The saved password was updated to the new one: re-saving without a password (merging the existing password) must be connected directly.
	link, err := instance.service.LinkGet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if link.VerifiedAt == 0 {
		t.Fatalf("probe not recorded after reset: %+v", link)
	}
	result, err = instance.service.LinkSet(ctx, LinkPatch{URL: testPtr(fixture.URL), Username: testPtr("alice")})
	if err != nil {
		t.Fatal(err)
	}
	if result.Connect != LinkConnectConnected {
		t.Fatalf("re-connect = %q, want connected with updated password", result.Connect)
	}
}

func TestRemoteProxyRequiresDesktop(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true, WithMetadata("test", false))

	if _, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "alice-pw-123")); err != nil {
		t.Fatal(err)
	}
	_, err := instance.service.Remote().ListDevices(context.Background())
	requireCode(t, err, ipc.CodeUnsupported)
}
