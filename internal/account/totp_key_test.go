package account

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestTOTPKeyStoredOutsideDatabase(t *testing.T) {
	a, now := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "keycase", "", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := a.BeginTOTPSetup(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	var settingCount int
	if err := a.db.QueryRowContext(ctx, "SELECT count(*) FROM setting WHERE key = ?", "auth.totp_key").Scan(&settingCount); err != nil {
		t.Fatal(err)
	}
	if settingCount != 0 {
		t.Fatal("TOTP key material must not live in the database")
	}
	info, err := os.Stat(a.totpKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %o, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(a.totpKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(key) != totpKeyBytes {
		t.Fatalf("key file content invalid: %v", err)
	}

	codes, err := a.ConfirmTOTPSetup(ctx, user.ID, totpCodeForTest(t, secret, *now/1000))
	if err != nil {
		t.Fatal(err)
	}
	valid, err := a.VerifyTOTPLoginCode(ctx, user.ID, codes[0])
	if err != nil || !valid {
		t.Fatalf("recovery code rejected: %v %v", valid, err)
	}
}

func TestTOTPKeyFailClosedWithoutKeyFile(t *testing.T) {
	db, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a := New(db.DB())
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "nokey", "", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.BeginTOTPSetup(ctx, user.ID, ""); err == nil || !strings.Contains(err.Error(), "未配置") {
		t.Fatalf("setup without key file must fail closed: %v", err)
	}
}

func TestTOTPKeyLegacySettingIgnored(t *testing.T) {
	db, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	legacy := make([]byte, totpKeyBytes)
	for i := range legacy {
		legacy[i] = byte(i + 1)
	}
	if _, err := db.DB().ExecContext(ctx, `INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)`,
		"auth.totp_key", base64.StdEncoding.EncodeToString(legacy), 1); err != nil {
		t.Fatal(err)
	}

	keyPath := filepath.Join(t.TempDir(), "totp.key")
	a := New(db.DB(), WithTOTPKeyFile(keyPath))
	now := int64(1_700_000_000_000)
	a.now = func() int64 { return now }
	user, err := a.CreateUser(ctx, "legacy", "", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := a.BeginTOTPSetup(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(generated) != totpKeyBytes {
		t.Fatalf("key file content invalid: %v", err)
	}
	if bytes.Equal(generated, legacy) {
		t.Fatal("legacy in-database key must not be migrated into the key file")
	}
	var settingCount int
	if err := a.db.QueryRowContext(ctx, "SELECT count(*) FROM setting WHERE key = ?", "auth.totp_key").Scan(&settingCount); err != nil {
		t.Fatal(err)
	}
	if settingCount != 1 {
		t.Fatal("legacy in-database key row must be left untouched")
	}

	block, err := aes.NewCipher(legacy)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, totpNonceLength)
	legacyEnvelope := aead.Seal(nonce, nonce, []byte("legacy-secret"), totpAAD(user.ID))
	if opened, err := a.openTOTPSecret(user.ID, legacyEnvelope); err == nil {
		t.Fatalf("legacy envelope must no longer open: %q", opened)
	}

	if _, err := a.ConfirmTOTPSetup(ctx, user.ID, totpCodeForTest(t, secret, now/1000)); err != nil {
		t.Fatal(err)
	}
}

func TestTOTPVerifyGuidesRebindWhenLegacyKeyPresent(t *testing.T) {
	a, now := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "legacylogin", "", "password-123")
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

	legacy := bytes.Repeat([]byte("L"), totpKeyBytes)
	block, err := aes.NewCipher(legacy)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	rawSecret, err := totpSecretEncoding.DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, totpNonceLength)
	legacyEnvelope := aead.Seal(nonce, nonce, rawSecret, totpAAD(user.ID))
	if _, err := a.db.ExecContext(ctx, "UPDATE user_totp SET secret_envelope = ? WHERE user_id = ?", legacyEnvelope, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)`,
		"auth.totp_key", base64.StdEncoding.EncodeToString(legacy), 1); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(a.totpKeyPath); err != nil {
		t.Fatal(err)
	}

	if _, err := a.VerifyTOTPLoginCode(ctx, user.ID, totpCodeForTest(t, secret, *now/1000)); err == nil || !strings.Contains(err.Error(), "重新绑定") {
		t.Fatalf("legacy-sealed TOTP must fail with rebind guidance: %v", err)
	}
	valid, err := a.VerifyTOTPLoginCode(ctx, user.ID, codes[0])
	if err != nil || !valid {
		t.Fatalf("recovery code must stay usable after key drop: %v %v", valid, err)
	}
}

func TestTOTPRebindReverify(t *testing.T) {
	a, now := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "rebind", "", "password-123")
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

	if _, _, err := a.BeginTOTPSetup(ctx, user.ID, ""); err == nil || ipc.NormalizeError(err).Code != ipc.CodeForbidden {
		t.Fatalf("empty reverify accepted: %v", err)
	}
	if _, _, err := a.BeginTOTPSetup(ctx, user.ID, "wrong-credential"); err == nil || ipc.NormalizeError(err).Code != ipc.CodeForbidden {
		t.Fatalf("wrong reverify accepted: %v", err)
	}
	if _, _, err := a.BeginTOTPSetup(ctx, user.ID, "wrong-password-1"); err == nil || ipc.NormalizeError(err).Code != ipc.CodeForbidden {
		t.Fatalf("wrong password accepted: %v", err)
	}

	// 当前动态码可换绑; 确认后旧恢复码全部作废, 换发新码。
	secret2, _, err := a.BeginTOTPSetup(ctx, user.ID, totpCodeForTest(t, secret, *now/1000))
	if err != nil {
		t.Fatal(err)
	}
	codes2, err := a.ConfirmTOTPSetup(ctx, user.ID, totpCodeForTest(t, secret2, *now/1000))
	if err != nil {
		t.Fatal(err)
	}

	// 恢复码可换绑(一次性, 用掉即焚)。
	if _, _, err := a.BeginTOTPSetup(ctx, user.ID, codes2[0]); err != nil {
		t.Fatalf("recovery reverify rejected: %v", err)
	}
	if _, _, err := a.BeginTOTPSetup(ctx, user.ID, codes2[0]); err == nil {
		t.Fatal("reused recovery code accepted for rebind")
	}

	// 登录密码可换绑。
	if _, _, err := a.BeginTOTPSetup(ctx, user.ID, "password-123"); err != nil {
		t.Fatalf("password reverify rejected: %v", err)
	}
}
