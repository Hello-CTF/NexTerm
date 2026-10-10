package account

import (
	"context"
	"encoding/base64"
	"os"
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
