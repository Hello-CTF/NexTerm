package sync

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

func TestSyncTokenEncryptedAtRestWithPlaintextBackup(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, true)

	token, err := instance.service.Token(ctx)
	if err != nil || token == "" {
		t.Fatalf("token=%q err=%v", token, err)
	}
	stored, found, err := instance.db.SettingGet(ctx, settingToken)
	if err != nil || !found {
		t.Fatalf("sync.token found=%v err=%v", found, err)
	}
	if !strings.HasPrefix(stored, store.SecretEnvelopePrefix) {
		t.Fatalf("sync.token is not an encrypted envelope: %q", stored)
	}
	if strings.Contains(stored, token) {
		t.Fatal("sync.token envelope contains plaintext token")
	}
	plaintext, err := instance.vault.DecryptSecret(ctx, stored)
	if err != nil || plaintext != token {
		t.Fatalf("envelope decrypt=%q err=%v", plaintext, err)
	}
	backup, found, err := instance.db.SettingGet(ctx, settingTokenBackup)
	if err != nil || !found || backup != token {
		t.Fatalf("plaintext backup=%q found=%v err=%v", backup, found, err)
	}

	instance.vault.Lock()
	if valid, err := instance.service.VerifyToken(ctx, token); err != nil || !valid {
		t.Fatalf("hash-based verification must not need the vault: valid=%v err=%v", valid, err)
	}
	recovered, err := instance.service.Token(ctx)
	if err != nil || recovered != token {
		t.Fatalf("locked vault must fall back to the rollback key: %q err=%v", recovered, err)
	}
}

func TestSyncTokenLockedVaultWithoutBackupFailsExplicit(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, true)
	if _, err := instance.service.Token(ctx); err != nil {
		t.Fatal(err)
	}
	if err := instance.db.SettingDelete(ctx, settingTokenBackup); err != nil {
		t.Fatal(err)
	}
	instance.vault.Lock()
	_, err := instance.service.Token(ctx)
	requireCode(t, err, ipc.CodeVaultLocked)
}

func TestSyncTokenNotInitVaultFallsBackToBackup(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, true)
	token, err := instance.service.Token(ctx)
	if err != nil || token == "" {
		t.Fatalf("token=%q err=%v", token, err)
	}
	for _, key := range []string{"vault.mode", "vault.master_salt", "vault.dek_envelope"} {
		if err := instance.db.SettingDelete(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	notInitVault := vault.Load(ctx, instance.db)
	if notInitVault.Status().Initialized {
		t.Fatal("test setup must produce an uninitialized vault")
	}
	service := New(instance.db, notInitVault, WithMetadata("test", true))
	recovered, err := service.Token(ctx)
	if err != nil || recovered != token {
		t.Fatalf("not-init vault must fall back to the rollback key: %q err=%v", recovered, err)
	}
}

func TestSyncTokenRotateLockedVaultFailsWithoutMutation(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, true)
	original, err := instance.service.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	storedBefore, _, _ := instance.db.SettingGet(ctx, settingToken)
	backupBefore, _, _ := instance.db.SettingGet(ctx, settingTokenBackup)

	instance.vault.Lock()
	rotated, err := instance.service.RotateToken(ctx)
	requireCode(t, err, ipc.CodeVaultLocked)
	if rotated != "" {
		t.Fatalf("locked rotation returned a token: %q", rotated)
	}
	storedAfter, _, _ := instance.db.SettingGet(ctx, settingToken)
	backupAfter, _, _ := instance.db.SettingGet(ctx, settingTokenBackup)
	if storedAfter != storedBefore || backupAfter != backupBefore {
		t.Fatal("locked rotation mutated token settings")
	}
	if valid, _ := instance.service.VerifyToken(ctx, original); !valid {
		t.Fatal("locked rotation must leave the previous token valid")
	}
	tokens, _ := instance.service.TokenList(ctx)
	for _, token := range tokens {
		if token.ID == adminTokenID && token.RevokedAt != nil {
			t.Fatal("locked rotation mutated the admin row")
		}
	}
}

func TestLegacyMigrationLockedVaultFailsClosed(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, true)
	const legacy = "legacy-plaintext-token"
	if err := instance.db.SettingSet(ctx, settingToken, legacy); err != nil {
		t.Fatal(err)
	}
	instance.vault.Lock()
	if _, err := instance.service.VerifyToken(ctx, legacy); err == nil {
		t.Fatal("locked vault must block migration instead of writing hash or settings")
	} else {
		requireCode(t, err, ipc.CodeVaultLocked)
	}
	if tokens, _ := instance.service.TokenList(ctx); len(tokens) != 0 {
		t.Fatalf("locked migration wrote a token row: %+v", tokens)
	}
	stored, _, _ := instance.db.SettingGet(ctx, settingToken)
	if stored != legacy {
		t.Fatalf("locked migration rewrote the legacy setting: %q", stored)
	}
	if _, found, _ := instance.db.SettingGet(ctx, settingTokenBackup); found {
		t.Fatal("locked migration wrote the plaintext backup")
	}
}

func TestSyncTokenDualReadUpgradesLegacyPlaintext(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, true)
	const legacy = "legacy-plaintext-token"
	if err := instance.db.SettingSet(ctx, settingToken, legacy); err != nil {
		t.Fatal(err)
	}
	token, err := instance.service.Token(ctx)
	if err != nil || token != legacy {
		t.Fatalf("dual-read token=%q err=%v", token, err)
	}
	stored, _, _ := instance.db.SettingGet(ctx, settingToken)
	if !strings.HasPrefix(stored, store.SecretEnvelopePrefix) {
		t.Fatalf("unlocked vault must upgrade legacy plaintext on write: %q", stored)
	}
	backup, _, _ := instance.db.SettingGet(ctx, settingTokenBackup)
	if backup != legacy {
		t.Fatalf("backup missing after upgrade: %q", backup)
	}
	if valid, _ := instance.service.VerifyToken(ctx, legacy); !valid {
		t.Fatal("upgraded token stopped verifying")
	}
}

func TestSyncLinkEncryptedRoundTripAndLockedErrors(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, true)
	linkToken := "link-peer-token"
	insecure := false
	if _, err := instance.service.LinkSet(ctx, LinkPatch{URL: "https://peer.example.com", Token: &linkToken, Insecure: &insecure}); err != nil {
		t.Fatal(err)
	}
	stored, found, err := instance.db.SettingGet(ctx, settingLink)
	if err != nil || !found {
		t.Fatalf("sync.link found=%v err=%v", found, err)
	}
	if !strings.HasPrefix(stored, store.SecretEnvelopePrefix) || strings.Contains(stored, linkToken) {
		t.Fatalf("sync.link must be an envelope without plaintext: %q", stored)
	}
	link, err := instance.service.LinkGet(ctx)
	if err != nil || link.Token != linkToken || link.URL != "https://peer.example.com" {
		t.Fatalf("link=%+v err=%v", link, err)
	}

	instance.vault.Lock()
	if _, err := instance.service.LinkGet(ctx); err == nil {
		t.Fatal("locked vault must not silently read the encrypted link")
	} else {
		requireCode(t, err, ipc.CodeVaultLocked)
	}
	if _, err := instance.service.LinkSet(ctx, LinkPatch{URL: "https://other.example.com"}); err == nil {
		t.Fatal("locked vault must reject link writes explicitly")
	} else {
		requireCode(t, err, ipc.CodeVaultLocked)
	}
}

func TestSyncLinkLegacyPlaintextDualRead(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, false)
	legacy := Link{URL: "https://legacy.example.com", TokenKind: TokenKindServer, Token: "legacy-link-token", Insecure: true}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.db.SettingSet(ctx, settingLink, string(encoded)); err != nil {
		t.Fatal(err)
	}
	link, err := instance.service.LinkGet(ctx)
	if err != nil || link.Token != legacy.Token || link.URL != legacy.URL || !link.Insecure {
		t.Fatalf("legacy link=%+v err=%v", link, err)
	}
}
