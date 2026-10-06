package account

import (
	"bytes"
	"context"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

func rewrapForTest(t *testing.T, oldPassword, newPassword string, current *vault.UserDEKEnvelopes) *vault.UserDEKEnvelopes {
	t.Helper()
	dek, err := vault.UnwrapUserDEK(oldPassword, current.DEKEnvelope, current.KDFSalt, current.KDFParams)
	if err != nil {
		t.Fatal(err)
	}
	envelope, salt, params, err := vault.WrapUserDEK(newPassword, dek)
	if err != nil {
		t.Fatal(err)
	}
	return &vault.UserDEKEnvelopes{
		DEKEnvelope:      envelope,
		KDFSalt:          salt,
		KDFParams:        params,
		RecoveryEnvelope: current.RecoveryEnvelope,
		RecoveryHash:     current.RecoveryHash,
	}
}

func TestSetAndGetUserDEKEnvelopes(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "lena", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	_, envelopes, _, err := vault.GenerateUserDEKEnvelopes("synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetUserDEKEnvelopes(ctx, user.ID, envelopes); err != nil {
		t.Fatal(err)
	}
	loaded, err := a.GetUserDEKEnvelopes(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded.DEKEnvelope, envelopes.DEKEnvelope) ||
		!bytes.Equal(loaded.KDFSalt, envelopes.KDFSalt) ||
		loaded.KDFParams != envelopes.KDFParams ||
		!bytes.Equal(loaded.RecoveryEnvelope, envelopes.RecoveryEnvelope) ||
		loaded.RecoveryHash != envelopes.RecoveryHash {
		t.Fatal("stored envelopes mismatch")
	}
	if err := a.SetUserDEKEnvelopes(ctx, user.ID, &vault.UserDEKEnvelopes{}); err == nil {
		t.Fatal("incomplete envelopes accepted")
	}
	other, err := a.CreateUser(ctx, "mike", "", "synthetic-password-2")
	if err != nil {
		t.Fatal(err)
	}
	requireCode(t, getEnvErr(a, ctx, other.ID), ipc.CodeNotFound)
}

func TestChangePasswordRewrapOnly(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "nina", "", "old-password-1")
	if err != nil {
		t.Fatal(err)
	}
	dek, envelopes, _, err := vault.GenerateUserDEKEnvelopes("old-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetUserDEKEnvelopes(ctx, user.ID, envelopes); err != nil {
		t.Fatal(err)
	}
	keepToken, keepSession, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	dropToken, _, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	updated := rewrapForTest(t, "old-password-1", "new-password-2", envelopes)
	if err := a.ChangePassword(ctx, user.ID, "old-password-1", "new-password-2", updated, keepSession.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, "nina", "new-password-2"); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
	requireCode(t, authErr(a, ctx, "nina", "old-password-1"), ipc.CodeForbidden)
	if _, err := a.ValidateSession(ctx, keepToken); err != nil {
		t.Fatalf("kept session revoked: %v", err)
	}
	requireCode(t, validateErr(a, ctx, dropToken), ipc.CodeForbidden)

	loaded, err := a.GetUserDEKEnvelopes(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded.DEKEnvelope, updated.DEKEnvelope) {
		t.Fatal("envelope not swapped")
	}
	unwrapped, err := vault.UnwrapUserDEK("new-password-2", loaded.DEKEnvelope, loaded.KDFSalt, loaded.KDFParams)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(unwrapped, dek) {
		t.Fatal("dek changed across password change")
	}
	if _, err := a.Authenticate(ctx, "nina", "new-password-2"); err != nil {
		t.Fatal(err)
	}
}

func TestChangePasswordWrongOldLeavesRowsUntouched(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "oscar", "", "old-password-1")
	if err != nil {
		t.Fatal(err)
	}
	_, envelopes, _, err := vault.GenerateUserDEKEnvelopes("old-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetUserDEKEnvelopes(ctx, user.ID, envelopes); err != nil {
		t.Fatal(err)
	}
	before, err := a.GetUserDEKEnvelopes(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated := rewrapForTest(t, "old-password-1", "new-password-2", envelopes)
	requireCode(t, a.ChangePassword(ctx, user.ID, "wrong-old", "new-password-2", updated, ""), ipc.CodeForbidden)

	after, err := a.GetUserDEKEnvelopes(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after.DEKEnvelope, before.DEKEnvelope) || !bytes.Equal(after.KDFSalt, before.KDFSalt) {
		t.Fatal("envelopes mutated by failed password change")
	}
	if _, err := a.Authenticate(ctx, "oscar", "old-password-1"); err != nil {
		t.Fatalf("old password broken by failed change: %v", err)
	}
}

func TestResetPasswordWithRecovery(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "paul", "", "old-password-1")
	if err != nil {
		t.Fatal(err)
	}
	dek, envelopes, recoveryKey, err := vault.GenerateUserDEKEnvelopes("old-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetUserDEKEnvelopes(ctx, user.ID, envelopes); err != nil {
		t.Fatal(err)
	}
	token, _, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	bogus := rewrapForTest(t, "old-password-1", "new-password-2", envelopes)
	requireCode(t, a.ResetPasswordWithRecovery(ctx, "paul", "AAAA-AAAA-AAAA-AAAA-AAAA-AAAA-AAAA-AAAA", "new-password-2", bogus), ipc.CodeForbidden)
	if _, err := a.Authenticate(ctx, "paul", "old-password-1"); err != nil {
		t.Fatalf("wrong recovery key mutated password: %v", err)
	}
	if _, err := a.ValidateSession(ctx, token); err != nil {
		t.Fatalf("wrong recovery key revoked sessions: %v", err)
	}
	untouched, err := a.GetUserDEKEnvelopes(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(untouched.DEKEnvelope, envelopes.DEKEnvelope) {
		t.Fatal("wrong recovery key mutated envelopes")
	}

	recovered, err := vault.UnwrapUserDEKWithRecovery(recoveryKey, envelopes.RecoveryEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(recovered, dek) {
		t.Fatal("recovery unwrap mismatch")
	}
	newEnvelope, newSalt, newParams, err := vault.WrapUserDEK("new-password-2", recovered)
	if err != nil {
		t.Fatal(err)
	}
	newRecoveryCanonical, err := vault.GenerateRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	newRecoveryEnvelope, err := vault.WrapUserDEKWithRecovery(newRecoveryCanonical, recovered)
	if err != nil {
		t.Fatal(err)
	}
	rotated := &vault.UserDEKEnvelopes{
		DEKEnvelope:      newEnvelope,
		KDFSalt:          newSalt,
		KDFParams:        newParams,
		RecoveryEnvelope: newRecoveryEnvelope,
		RecoveryHash:     vault.RecoveryKeyHash(newRecoveryCanonical),
	}
	if err := a.ResetPasswordWithRecovery(ctx, "paul", vault.FormatRecoveryKey(recoveryKey), "new-password-2", rotated); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, "paul", "new-password-2"); err != nil {
		t.Fatal(err)
	}
	requireCode(t, authErr(a, ctx, "paul", "old-password-1"), ipc.CodeForbidden)
	requireCode(t, validateErr(a, ctx, token), ipc.CodeForbidden)
	stored, err := a.GetUserDEKEnvelopes(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored.RecoveryEnvelope, rotated.RecoveryEnvelope) || stored.RecoveryHash != rotated.RecoveryHash {
		t.Fatal("rotated recovery envelope not stored")
	}
	if !vault.VerifyRecoveryKey(newRecoveryCanonical, stored.RecoveryHash) {
		t.Fatal("new recovery key does not verify")
	}
}

func TestAdminResetUser(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "quinn", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	_, envelopes, _, err := vault.GenerateUserDEKEnvelopes("synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetUserDEKEnvelopes(ctx, user.ID, envelopes); err != nil {
		t.Fatal(err)
	}
	token, _, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AdminResetUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err := a.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateResetRequired || !loaded.MustChangePassword {
		t.Fatalf("unexpected state: %+v", loaded)
	}
	requireCode(t, getEnvErr(a, ctx, user.ID), ipc.CodeNotFound)
	requireCode(t, validateErr(a, ctx, token), ipc.CodeForbidden)
	requireCode(t, a.AdminResetUser(ctx, "missing-user"), ipc.CodeNotFound)

	if _, err := a.Authenticate(ctx, "quinn", "synthetic-password-1"); err != nil {
		t.Fatalf("login after admin reset rejected: %v", err)
	}
	restored := rewrapForTest(t, "synthetic-password-1", "synthetic-password-1", envelopes)
	if err := a.ChangePassword(ctx, user.ID, "synthetic-password-1", "synthetic-password-1", restored, ""); err != nil {
		t.Fatal(err)
	}
	after, err := a.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.MustChangePassword {
		t.Fatal("must_change_password not cleared")
	}
	if after.State != StateActive {
		t.Fatalf("state not restored to active: %s", after.State)
	}
	restoredToken, _, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatalf("issue session after restore: %v", err)
	}
	if _, err := a.ValidateSession(ctx, restoredToken); err != nil {
		t.Fatalf("validate session after restore: %v", err)
	}
}

func TestResetPasswordWithRecoveryDisabledUser(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "ruth", "", "old-password-1")
	if err != nil {
		t.Fatal(err)
	}
	_, envelopes, recoveryKey, err := vault.GenerateUserDEKEnvelopes("old-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetUserDEKEnvelopes(ctx, user.ID, envelopes); err != nil {
		t.Fatal(err)
	}
	token, _, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, "UPDATE user SET state = ? WHERE id = ?", string(StateDisabled), user.ID); err != nil {
		t.Fatal(err)
	}
	before, err := a.GetUserByUsername(ctx, "ruth")
	if err != nil {
		t.Fatal(err)
	}
	updated := rewrapForTest(t, "old-password-1", "new-password-2", envelopes)
	requireCode(t, a.ResetPasswordWithRecovery(ctx, "ruth", recoveryKey, "new-password-2", updated), ipc.CodeForbidden)

	after, err := a.GetUserByUsername(ctx, "ruth")
	if err != nil {
		t.Fatal(err)
	}
	if after.PasswordHash() != before.PasswordHash() {
		t.Fatal("disabled user password changed via recovery reset")
	}
	if after.State != StateDisabled {
		t.Fatalf("disabled state lifted: %s", after.State)
	}
	stored, err := a.GetUserDEKEnvelopes(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored.DEKEnvelope, envelopes.DEKEnvelope) {
		t.Fatal("disabled user envelopes changed via recovery reset")
	}
	requireCode(t, validateErr(a, ctx, token), ipc.CodeForbidden)
	requireCode(t, issueErr(a, ctx, user.ID, ""), ipc.CodeForbidden)
}

func TestResetPasswordWithRecoveryFromResetRequired(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "seth", "", "old-password-1")
	if err != nil {
		t.Fatal(err)
	}
	_, envelopes, recoveryKey, err := vault.GenerateUserDEKEnvelopes("old-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetUserDEKEnvelopes(ctx, user.ID, envelopes); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, "UPDATE user SET state = ? WHERE id = ?", string(StateResetRequired), user.ID); err != nil {
		t.Fatal(err)
	}
	updated := rewrapForTest(t, "old-password-1", "new-password-2", envelopes)
	if err := a.ResetPasswordWithRecovery(ctx, "seth", recoveryKey, "new-password-2", updated); err != nil {
		t.Fatal(err)
	}
	after, err := a.GetUserByUsername(ctx, "seth")
	if err != nil {
		t.Fatal(err)
	}
	if after.State != StateActive {
		t.Fatalf("state not restored: %s", after.State)
	}
	if _, err := a.Authenticate(ctx, "seth", "new-password-2"); err != nil {
		t.Fatal(err)
	}
	token, _, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ValidateSession(ctx, token); err != nil {
		t.Fatal(err)
	}
}

func getEnvErr(a *Accounts, ctx context.Context, userID string) error {
	_, err := a.GetUserDEKEnvelopes(ctx, userID)
	return err
}
