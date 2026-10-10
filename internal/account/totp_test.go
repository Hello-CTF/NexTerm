package account

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

func totpCodeForTest(t *testing.T, secretBase32 string, nowSeconds int64) string {
	t.Helper()
	secret, err := totpSecretEncoding.DecodeString(secretBase32)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%06d", hotp(secret, uint64(nowSeconds)/totpPeriodSeconds))
}

func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	vectors := []struct {
		unix int64
		code string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	}
	for _, vector := range vectors {
		if !verifyTOTPCode(secret, vector.code, vector.unix) {
			t.Fatalf("RFC6238 vector failed at %d: %s", vector.unix, vector.code)
		}
	}
	if verifyTOTPCode(secret, "287082", 59+2*totpPeriodSeconds) {
		t.Fatal("code outside ±1 window accepted")
	}
	if verifyTOTPCode(secret, "12345", 59) || verifyTOTPCode(secret, "abcdef", 59) {
		t.Fatal("malformed code accepted")
	}
}

func TestTOTPSetupConfirmLifecycle(t *testing.T) {
	a, now := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "alice", "", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	enabled, pending, left, err := a.TOTPStatus(ctx, user.ID)
	if err != nil || enabled || pending || left != 0 {
		t.Fatalf("unexpected initial status: %v %v %d %v", enabled, pending, left, err)
	}

	secret, uri, err := a.BeginTOTPSetup(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 32 || !strings.HasPrefix(uri, "otpauth://totp/NexTerm:alice?secret="+secret+"&issuer=NexTerm") {
		t.Fatalf("unexpected setup payload: %q %q", secret, uri)
	}
	enabled, pending, _, err = a.TOTPStatus(ctx, user.ID)
	if err != nil || enabled || !pending {
		t.Fatalf("status after setup: %v %v %v", enabled, pending, err)
	}
	if bound, err := a.TOTPEnabled(ctx, user.ID); err != nil || bound {
		t.Fatalf("unconfirmed binding must not count as enabled: %v %v", bound, err)
	}

	if _, err := a.ConfirmTOTPSetup(ctx, user.ID, "000000"); err == nil {
		t.Fatal("wrong confirm code accepted")
	}
	codes, err := a.ConfirmTOTPSetup(ctx, user.ID, totpCodeForTest(t, secret, *now/1000))
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != recoveryCodeCount {
		t.Fatalf("unexpected recovery code count: %d", len(codes))
	}
	for _, code := range codes {
		if len(code) != 19 {
			t.Fatalf("unexpected recovery code format: %q", code)
		}
	}
	enabled, pending, left, err = a.TOTPStatus(ctx, user.ID)
	if err != nil || !enabled || pending || left != recoveryCodeCount {
		t.Fatalf("status after confirm: %v %v %d %v", enabled, pending, left, err)
	}

	if _, err := a.ConfirmTOTPSetup(ctx, user.ID, totpCodeForTest(t, secret, *now/1000)); err == nil {
		t.Fatal("second confirm accepted without new setup")
	}

	secret2, _, err := a.BeginTOTPSetup(ctx, user.ID, totpCodeForTest(t, secret, *now/1000))
	if err != nil {
		t.Fatal(err)
	}
	if secret2 == secret {
		t.Fatal("re-setup reused the same secret")
	}
	codes2, err := a.ConfirmTOTPSetup(ctx, user.ID, totpCodeForTest(t, secret2, *now/1000))
	if err != nil {
		t.Fatal(err)
	}
	if valid, err := a.VerifyTOTPLoginCode(ctx, user.ID, codes[0]); err != nil || valid {
		t.Fatalf("stale recovery code accepted after re-bind: %v %v", valid, err)
	}
	valid, err := a.VerifyTOTPLoginCode(ctx, user.ID, codes2[0])
	if err != nil || !valid {
		t.Fatalf("fresh recovery codes must work: %v %v", valid, err)
	}
}

func TestTOTPLoginVerification(t *testing.T) {
	a, now := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "bob", "", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := a.BeginTOTPSetup(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	codes, err := a.ConfirmTOTPSetup(ctx, user.ID, totpCodeForTest(t, secret, *now/1000))
	if err != nil {
		t.Fatal(err)
	}

	valid, err := a.VerifyTOTPLoginCode(ctx, user.ID, totpCodeForTest(t, secret, *now/1000))
	if err != nil || !valid {
		t.Fatalf("valid TOTP code rejected: %v %v", valid, err)
	}
	if valid, err := a.VerifyTOTPLoginCode(ctx, user.ID, "123456"); err != nil || valid {
		t.Fatalf("wrong code accepted: %v %v", valid, err)
	}

	recovery := codes[0]
	valid, err = a.VerifyTOTPLoginCode(ctx, user.ID, recovery)
	if err != nil || !valid {
		t.Fatalf("valid recovery code rejected: %v %v", valid, err)
	}
	valid, err = a.VerifyTOTPLoginCode(ctx, user.ID, recovery)
	if err != nil || valid {
		t.Fatalf("recovery code must be single-use: %v %v", valid, err)
	}
	_, _, left, err := a.TOTPStatus(ctx, user.ID)
	if err != nil || left != recoveryCodeCount-1 {
		t.Fatalf("recovery code left count: %d %v", left, err)
	}

	if err := a.DisableTOTP(ctx, user.ID, "000000"); err == nil {
		t.Fatal("disable with wrong code accepted")
	}
	if err := a.DisableTOTP(ctx, user.ID, totpCodeForTest(t, secret, *now/1000)); err != nil {
		t.Fatal(err)
	}
	if bound, err := a.TOTPEnabled(ctx, user.ID); err != nil || bound {
		t.Fatalf("still enabled after disable: %v %v", bound, err)
	}
	if valid, err := a.VerifyTOTPLoginCode(ctx, user.ID, totpCodeForTest(t, secret, *now/1000)); err != nil || valid {
		t.Fatalf("disabled secret still verifies: %v %v", valid, err)
	}
	if valid, err := a.VerifyTOTPLoginCode(ctx, user.ID, codes[1]); err != nil || valid {
		t.Fatalf("recovery code survived disable: %v %v", valid, err)
	}
}

func TestTOTPSecretStoredEncrypted(t *testing.T) {
	a, now := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "carol", "", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := a.BeginTOTPSetup(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := totpSecretEncoding.DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	var envelope []byte
	if err := a.db.QueryRowContext(ctx, "SELECT pending_secret_envelope FROM user_totp WHERE user_id = ?", user.ID).Scan(&envelope); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(envelope), secret) || strings.Contains(string(envelope), string(raw)) {
		t.Fatal("pending secret stored as plaintext")
	}
	if _, err := a.openTOTPSecret("u-other", envelope); err == nil {
		t.Fatal("envelope opened under wrong user AAD")
	}

	codes, err := a.ConfirmTOTPSetup(ctx, user.ID, totpCodeForTest(t, secret, *now/1000))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRowContext(ctx, "SELECT secret_envelope FROM user_totp WHERE user_id = ?", user.ID).Scan(&envelope); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(envelope), secret) {
		t.Fatal("active secret stored as plaintext")
	}
	var hashCount int
	if err := a.db.QueryRowContext(ctx, "SELECT count(*) FROM user_totp_recovery_code WHERE code_hash = ?", codes[0]).Scan(&hashCount); err != nil {
		t.Fatal(err)
	}
	if hashCount != 0 {
		t.Fatal("recovery code stored as plaintext")
	}
}

func TestMFARequiredSetting(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	if required, err := a.MFARequired(ctx); err != nil || required {
		t.Fatalf("default must be off: %v %v", required, err)
	}
	if err := a.SetMFARequired(ctx, true); err != nil {
		t.Fatal(err)
	}
	if required, err := a.MFARequired(ctx); err != nil || !required {
		t.Fatalf("setting not persisted: %v %v", required, err)
	}
	if err := a.SetMFARequired(ctx, false); err != nil {
		t.Fatal(err)
	}
	if required, err := a.MFARequired(ctx); err != nil || required {
		t.Fatalf("setting not cleared: %v %v", required, err)
	}
}

func TestAdminResetClearsTOTP(t *testing.T) {
	a, now := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "dave", "", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := a.BeginTOTPSetup(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfirmTOTPSetup(ctx, user.ID, totpCodeForTest(t, secret, *now/1000)); err != nil {
		t.Fatal(err)
	}
	if err := a.AdminResetUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if bound, err := a.TOTPEnabled(ctx, user.ID); err != nil || bound {
		t.Fatalf("admin reset must clear TOTP binding: %v %v", bound, err)
	}
}

func TestTOTPEnabledMap(t *testing.T) {
	a, now := testAccounts(t)
	ctx := context.Background()
	bound, err := a.CreateUser(ctx, "erin", "", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := a.CreateUser(ctx, "frank", "", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := a.BeginTOTPSetup(ctx, bound.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfirmTOTPSetup(ctx, bound.ID, totpCodeForTest(t, secret, *now/1000)); err != nil {
		t.Fatal(err)
	}
	enabled, err := a.TOTPEnabledMap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled[bound.ID] || enabled[plain.ID] {
		t.Fatalf("unexpected enabled map: %v", enabled)
	}
}

func TestConfirmTOTPSetupRejectsReplacedPending(t *testing.T) {
	a, now := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "race", "", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	originalSecret, _, err := a.BeginTOTPSetup(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	baseNow := a.now
	defer func() { a.now = baseNow }()
	var nowCalls atomic.Int32
	confirming := make(chan struct{})
	resume := make(chan struct{})
	a.now = func() int64 {
		if nowCalls.Add(1) == 2 {
			close(confirming)
			<-resume
		}
		return baseNow()
	}
	confirmed := make(chan error, 1)
	go func() {
		_, err := a.ConfirmTOTPSetup(ctx, user.ID, totpCodeForTest(t, originalSecret, *now/1000))
		confirmed <- err
	}()
	<-confirming
	replacementSecret, _, replacementErr := a.BeginTOTPSetup(ctx, user.ID, "")
	close(resume)
	if replacementErr != nil {
		t.Fatal(replacementErr)
	}
	if err := <-confirmed; err == nil {
		t.Fatal("stale pending confirmation succeeded after replacement")
	}

	enabled, pending, _, err := a.TOTPStatus(ctx, user.ID)
	if err != nil || enabled || !pending {
		t.Fatalf("replacement status = enabled:%v pending:%v err:%v", enabled, pending, err)
	}
	a.now = baseNow
	if _, err := a.ConfirmTOTPSetup(ctx, user.ID, totpCodeForTest(t, replacementSecret, *now/1000)); err != nil {
		t.Fatalf("replacement setup could not be confirmed: %v", err)
	}
}
