package account

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"
)

func TestAccountEpochBumpEvents(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "alice", "", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	epoch := a.Epoch(user.ID)
	if !a.CheckEpoch(user.ID, epoch) {
		t.Fatal("fresh account epoch mismatch")
	}

	// Session issuance alone does not advance the generation.
	_, first, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !a.CheckEpoch(user.ID, epoch) {
		t.Fatal("session issuance bumped the epoch")
	}

	// A single-session revocation bumps it.
	if err := a.RevokeSession(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if a.CheckEpoch(user.ID, epoch) {
		t.Fatal("session revoke did not bump the epoch")
	}
	epoch = a.Epoch(user.ID)

	// A user-scoped single-session revocation bumps it.
	_, second, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.RevokeUserSession(ctx, user.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	if a.CheckEpoch(user.ID, epoch) {
		t.Fatal("user session revoke did not bump the epoch")
	}
	epoch = a.Epoch(user.ID)

	// Revoking all sessions bumps it.
	if _, _, err := a.IssueSession(ctx, user.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RevokeUserSessionsWithIDs(ctx, user.ID, ""); err != nil {
		t.Fatal(err)
	}
	if a.CheckEpoch(user.ID, epoch) {
		t.Fatal("revoke-all did not bump the epoch")
	}
	epoch = a.Epoch(user.ID)

	// A device revocation bumps it.
	device, err := a.RegisterDevice(ctx, user.ID, "alice-phone", "mobile")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.RevokeDevice(ctx, user.ID, device.ID); err != nil {
		t.Fatal(err)
	}
	if a.CheckEpoch(user.ID, epoch) {
		t.Fatal("device revoke did not bump the epoch")
	}
	epoch = a.Epoch(user.ID)

	// Disabling bumps it; re-enabling does not.
	if err := a.SetUserDisabled(ctx, user.ID, true); err != nil {
		t.Fatal(err)
	}
	if a.CheckEpoch(user.ID, epoch) {
		t.Fatal("disable did not bump the epoch")
	}
	epoch = a.Epoch(user.ID)
	if err := a.SetUserDisabled(ctx, user.ID, false); err != nil {
		t.Fatal(err)
	}
	if !a.CheckEpoch(user.ID, epoch) {
		t.Fatal("re-enable bumped the epoch")
	}

	// A password change bumps it.
	if err := a.ChangePassword(ctx, user.ID, "password-123", "password-456", testEnvelopes(t), ""); err != nil {
		t.Fatal(err)
	}
	if a.CheckEpoch(user.ID, epoch) {
		t.Fatal("password change did not bump the epoch")
	}
	epoch = a.Epoch(user.ID)

	// An admin reset bumps it.
	if err := a.AdminResetUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if a.CheckEpoch(user.ID, epoch) {
		t.Fatal("admin reset did not bump the epoch")
	}
}

func TestTOTPSetupConfirmEpochBinding(t *testing.T) {
	a, now := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "alice", "", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := a.BeginTOTPSetup(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	// A revocation between setup and confirm invalidates the pending binding.
	a.epochs.bump(user.ID)
	if _, err := a.ConfirmTOTPSetup(ctx, user.ID, totpCodeForTest(t, secret, *now/1000)); err == nil {
		t.Fatal("stale-generation TOTP confirm succeeded")
	}
	if enabled, _, _, err := a.TOTPStatus(ctx, user.ID); err != nil || enabled {
		t.Fatalf("stale-generation confirm enabled TOTP: %v %v", enabled, err)
	}

	// A fresh setup binds to the current generation and confirms.
	secret2, _, err := a.BeginTOTPSetup(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfirmTOTPSetup(ctx, user.ID, totpCodeForTest(t, secret2, *now/1000)); err != nil {
		t.Fatalf("fresh-generation TOTP confirm failed: %v", err)
	}
}

// A failed owner read aborts the revocation instead of silently skipping the
// epoch bump after the revoke already committed: the session stays active, the
// epoch is untouched, and the retry after recovery revokes and bumps (so a
// login, MFA, or DEK boundary captured before the bump fails closed).
func TestRevokeSessionOwnerReadFailureAbortsRevocation(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "alice", "", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	token, session, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	epoch := a.Epoch(user.ID)

	original := accountSessionOwnerRead
	accountSessionOwnerRead = func(context.Context, *gorm.DB, string) (string, error) {
		return "", errors.New("injected owner read failure")
	}
	if err := a.RevokeSession(ctx, session.ID); err == nil {
		t.Fatal("RevokeSession swallowed the owner-read failure")
	}
	if !a.CheckEpoch(user.ID, epoch) {
		t.Fatal("failed revocation bumped the epoch")
	}
	if _, err := a.ValidateSession(ctx, token); err != nil {
		t.Fatalf("failed revocation revoked the session: %v", err)
	}

	accountSessionOwnerRead = original
	if err := a.RevokeSession(ctx, session.ID); err != nil {
		t.Fatalf("RevokeSession retry = %v", err)
	}
	if a.CheckEpoch(user.ID, epoch) {
		t.Fatal("successful revocation did not bump the epoch")
	}
	if _, err := a.ValidateSession(ctx, token); err == nil {
		t.Fatal("successful revocation left the session active")
	}
}
