package account

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
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

// The second envelope upload conflicts deterministically regardless of driver
// RowsAffected semantics (clientFoundRows reports 1 for a no-op INSERT), and
// the stored row keeps the first upload's content.
func TestInsertUserDEKEnvelopesConflictDeterministic(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "oliver", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	_, first, _, err := vault.GenerateUserDEKEnvelopes("synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.InsertUserDEKEnvelopes(ctx, user.ID, first); err != nil {
		t.Fatalf("first envelope upload = %v", err)
	}
	second := testEnvelopes(t)
	if err := a.InsertUserDEKEnvelopes(ctx, user.ID, second); err == nil || !errors.Is(err, ErrDEKEnvelopesExist) {
		t.Fatalf("second envelope upload = %v, want ErrDEKEnvelopesExist", err)
	}
	stored, err := a.GetUserDEKEnvelopes(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored.DEKEnvelope, first.DEKEnvelope) ||
		!bytes.Equal(stored.KDFSalt, first.KDFSalt) ||
		stored.KDFParams != first.KDFParams ||
		!bytes.Equal(stored.RecoveryEnvelope, first.RecoveryEnvelope) ||
		stored.RecoveryHash != first.RecoveryHash {
		t.Fatal("stored envelopes mismatch: the second upload overwrote the first")
	}
	// Re-uploading the identical envelope set also conflicts (no idempotent
	// success path): the row already exists.
	if err := a.InsertUserDEKEnvelopes(ctx, user.ID, first); err == nil || !errors.Is(err, ErrDEKEnvelopesExist) {
		t.Fatalf("identical re-upload = %v, want ErrDEKEnvelopesExist", err)
	}
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

func seedSyncRowsForTest(t *testing.T, a *Accounts, userID string) {
	t.Helper()
	rows := []any{
		&store.UserSyncObject{UserID: userID, ID: "obj-1", Seq: 1, Blob: []byte("blob"), BlobHash: []byte("obj-hash")},
		&store.UserSyncHead{UserID: userID, HeadHash: "head-1"},
		&store.SyncState{UserID: userID, ObjectID: "obj-1", PayloadHash: []byte("payload-hash"), BlobHash: []byte("obj-hash")},
		&store.CredentialTombstone{ID: "cred-1", DeletedAt: 1, OwnerID: userID},
		&store.SyncTombstone{ID: "obj-2", Kind: "asset", DeletedAt: 1, OwnerID: userID},
		&store.AuditRow{TS: 1, Source: "test", Kind: "admin.reset", PayloadJSON: "{}", OwnerID: userID},
	}
	for _, row := range rows {
		if err := a.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func countUserRows(t *testing.T, a *Accounts, model any, column, userID string) int64 {
	t.Helper()
	var n int64
	if err := a.db.Model(model).Where(column+" = ?", userID).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAdminResetUserPurgesSyncState(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "tara", "", "synthetic-password-1")
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
	seedSyncRowsForTest(t, a, user.ID)
	if err := a.AdminResetUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []struct {
		name   string
		model  any
		column string
	}{
		{"user_sync_object", &store.UserSyncObject{}, "user_id"},
		{"user_sync_head", &store.UserSyncHead{}, "user_id"},
		{"sync_state", &store.SyncState{}, "user_id"},
		{"credential_tombstone", &store.CredentialTombstone{}, "owner_id"},
		{"sync_tombstone", &store.SyncTombstone{}, "owner_id"},
	} {
		if n := countUserRows(t, a, table.model, table.column, user.ID); n != 0 {
			t.Fatalf("%s rows remain after admin reset: %d", table.name, n)
		}
	}
	requireCode(t, getEnvErr(a, ctx, user.ID), ipc.CodeNotFound)
	if n := countUserRows(t, a, &store.AuditRow{}, "owner_id", user.ID); n != 1 {
		t.Fatalf("audit rows after admin reset: %d", n)
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
	if err := a.db.Exec("UPDATE app_user SET state = ? WHERE id = ?", string(StateDisabled), user.ID).Error; err != nil {
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
	if err := a.db.Exec("UPDATE app_user SET state = ? WHERE id = ?", string(StateResetRequired), user.ID).Error; err != nil {
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

func rotatedEnvelopesForTest(t *testing.T, recoveryKey, password string, current *vault.UserDEKEnvelopes) *vault.UserDEKEnvelopes {
	t.Helper()
	dek, err := vault.UnwrapUserDEKWithRecovery(recoveryKey, current.RecoveryEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	envelope, salt, params, err := vault.WrapUserDEK(password, dek)
	if err != nil {
		t.Fatal(err)
	}
	newRecoveryKey, err := vault.GenerateRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	recoveryEnvelope, err := vault.WrapUserDEKWithRecovery(newRecoveryKey, dek)
	if err != nil {
		t.Fatal(err)
	}
	return &vault.UserDEKEnvelopes{
		DEKEnvelope:      envelope,
		KDFSalt:          salt,
		KDFParams:        params,
		RecoveryEnvelope: recoveryEnvelope,
		RecoveryHash:     vault.RecoveryKeyHash(newRecoveryKey),
	}
}

func TestResetPasswordWithRecoveryConcurrent(t *testing.T) {
	a := testFileAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "xavier", "", "old-password-1")
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
	passwords := []string{"new-password-a", "new-password-b"}
	rotated := []*vault.UserDEKEnvelopes{
		rotatedEnvelopesForTest(t, recoveryKey, passwords[0], envelopes),
		rotatedEnvelopesForTest(t, recoveryKey, passwords[1], envelopes),
	}
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range passwords {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = a.ResetPasswordWithRecovery(ctx, "xavier", vault.FormatRecoveryKey(recoveryKey), passwords[i], rotated[i])
		}(i)
	}
	close(start)
	wg.Wait()
	winner := -1
	for i, err := range errs {
		if err == nil {
			if winner != -1 {
				t.Fatalf("both recovery resets succeeded: %v", errs)
			}
			winner = i
			continue
		}
		requireCode(t, err, ipc.CodeForbidden)
	}
	if winner == -1 {
		t.Fatalf("no recovery reset succeeded: %v", errs)
	}
	requireCode(t, authErr(a, ctx, "xavier", "old-password-1"), ipc.CodeForbidden)
	if _, err := a.Authenticate(ctx, "xavier", passwords[winner]); err != nil {
		t.Fatalf("winner password rejected: %v", err)
	}
	requireCode(t, validateErr(a, ctx, token), ipc.CodeForbidden)
	stored, err := a.GetUserDEKEnvelopes(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored.RecoveryEnvelope, rotated[winner].RecoveryEnvelope) || stored.RecoveryHash != rotated[winner].RecoveryHash {
		t.Fatal("stored recovery envelope does not match the winning reset")
	}
	if vault.VerifyRecoveryKey(vault.FormatRecoveryKey(recoveryKey), stored.RecoveryHash) {
		t.Fatal("old recovery key still verifies after rotation")
	}
}
