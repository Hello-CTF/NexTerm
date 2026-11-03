package vault

import (
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// testKeyCheckSetting is spelled out so the tests pin the persisted key name.
const testKeyCheckSetting = "vault.key_check"

// slotKeychain models the legacy macOS behaviour: one slot shared by every
// data directory, each Protect overwriting the previous key.
type slotKeychain struct {
	mu  sync.Mutex
	key []byte
}

type slotProtector struct{ slot *slotKeychain }

func (p slotProtector) Protect(data []byte) ([]byte, error) {
	p.slot.mu.Lock()
	defer p.slot.mu.Unlock()
	p.slot.key = append([]byte(nil), data...)
	return []byte("slot:v1"), nil
}

func (p slotProtector) Unprotect(envelope []byte) ([]byte, error) {
	p.slot.mu.Lock()
	defer p.slot.mu.Unlock()
	if string(envelope) != "slot:v1" || len(p.slot.key) == 0 {
		return nil, errors.New("slot empty")
	}
	return append([]byte(nil), p.slot.key...), nil
}

// envelopeProtector gives every Protect its own envelope and can pretend that
// "env:v1" is the legacy format.
type envelopeProtector struct {
	mu          sync.Mutex
	keys        map[string][]byte
	seq         int
	failProtect bool
	protects    int
	issued      [][]byte
	discarded   [][]byte
	afterIssue  func()
}

func newEnvelopeProtector() *envelopeProtector {
	return &envelopeProtector{keys: map[string][]byte{}}
}

func (p *envelopeProtector) Protect(data []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.protects++
	if p.failProtect {
		return nil, errors.New("protect refused")
	}
	p.seq++
	envelope := "env:v2:" + string(rune('a'+p.seq))
	p.keys[envelope] = append([]byte(nil), data...)
	p.issued = append(p.issued, []byte(envelope))
	hook := p.afterIssue
	p.mu.Unlock()
	if hook != nil {
		hook()
	}
	p.mu.Lock()
	return []byte(envelope), nil
}

// Discard drops an envelope that was issued but never persisted.
func (p *envelopeProtector) Discard(envelope []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.discarded = append(p.discarded, append([]byte(nil), envelope...))
	delete(p.keys, string(envelope))
	return nil
}

func (p *envelopeProtector) liveEntries() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.issued) - len(p.discarded)
}

func (p *envelopeProtector) Unprotect(envelope []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key, ok := p.keys[string(envelope)]
	if !ok {
		return nil, errors.New("unknown envelope")
	}
	return append([]byte(nil), key...), nil
}

func (p *envelopeProtector) IsLegacyEnvelope(envelope []byte) bool {
	return string(envelope) == "env:v1"
}

func (p *envelopeProtector) setLegacy(key []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys["env:v1"] = append([]byte(nil), key...)
}

func testDEK(seed byte) *secretKey {
	key := &secretKey{}
	for i := range key.bytes {
		key.bytes[i] = seed + byte(i)
	}
	return key
}

func putCredential(t *testing.T, db *store.Store, dek *secretKey, plaintext string) store.CredentialRow {
	t.Helper()
	nonce, blob, err := sealCredential(dek, []byte(plaintext))
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.CredentialPut(context.Background(), store.CredentialInput{Name: plaintext, Kind: "password", Nonce: nonce, Blob: blob})
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.CredentialGetRow(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func putCorruptCredential(t *testing.T, db *store.Store) {
	t.Helper()
	nonce := make([]byte, nonceLength)
	if _, err := db.CredentialPut(context.Background(), store.CredentialInput{Name: "corrupt", Kind: "password", Nonce: nonce, Blob: []byte("garbage-garbage-garbage")}); err != nil {
		t.Fatal(err)
	}
}

func putSecretSetting(t *testing.T, db *store.Store, key string, dek *secretKey, plaintext, wrapper string) {
	t.Helper()
	nonce, blob, err := seal(dek, []byte(plaintext), secretAAD)
	if err != nil {
		t.Fatal(err)
	}
	envelope := store.SecretEnvelopePrefix + base64.StdEncoding.EncodeToString(append(nonce, blob...))
	if wrapper != "" {
		envelope = `{"apiKey":"` + envelope + `"` + wrapper + `}`
	}
	if err := db.SettingSet(context.Background(), key, envelope); err != nil {
		t.Fatal(err)
	}
}

// seedDPAPIVault writes a DPAPI-mode vault without a key-check, as older builds did.
func seedDPAPIVault(t *testing.T, db *store.Store, protector Protector, dek *secretKey) {
	t.Helper()
	envelope, err := protector.Protect(dek.bytes[:])
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SettingSetMany(context.Background(), map[string]string{
		settingMode:     string(ModeDPAPI),
		settingEnvelope: base64.StdEncoding.EncodeToString(envelope),
	}); err != nil {
		t.Fatal(err)
	}
}

func requireKeyCheckStored(t *testing.T, db *store.Store, want bool) {
	t.Helper()
	value, found, err := db.SettingGet(context.Background(), testKeyCheckSetting)
	if err != nil {
		t.Fatal(err)
	}
	if found && value != "" != want || !found && want {
		t.Fatalf("key-check stored=%v, want %v", found && value != "", want)
	}
}

func TestSharedKeychainSlotOverwriteIsReportedNotSilentlyUnlocked(t *testing.T) {
	ctx := context.Background()
	slot := &slotKeychain{}
	dbA, dbB := testStore(t), testStore(t)
	a := loadTestVault(dbA, slotProtector{slot})
	if err := a.InitDPAPI(ctx, ""); err != nil {
		t.Fatal(err)
	}
	nonce, blob, err := a.EncryptCredential(ctx, "secret-a")
	if err != nil {
		t.Fatal(err)
	}
	row := store.CredentialRow{Cipher: store.CipherAES256GCM, Nonce: nonce, Blob: blob}
	if _, err := dbA.CredentialPut(ctx, store.CredentialInput{Name: "a", Kind: "password", Nonce: nonce, Blob: blob}); err != nil {
		t.Fatal(err)
	}

	b := loadTestVault(dbB, slotProtector{slot})
	if err := b.InitDPAPI(ctx, ""); err != nil {
		t.Fatal(err)
	}

	reloaded := loadTestVault(dbA, slotProtector{slot})
	if reloaded.Status().Unlocked {
		t.Fatal("vault whose keychain entry was overwritten must not report unlocked")
	}
	_, err = reloaded.DecryptCredential(ctx, row)
	requireVaultCode(t, err, ipc.CodeCrypto)
	_, err = reloaded.EncryptSecret(ctx, "x")
	requireVaultCode(t, err, ipc.CodeCrypto)
	requireVaultCode(t, reloaded.InitDPAPI(ctx, ""), ipc.CodeCrypto)
}

func TestKeyCheckWrittenOnInit(t *testing.T) {
	ctx := context.Background()
	master := testStore(t)
	if err := loadTestVault(master, &fakeProtector{}).InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	requireKeyCheckStored(t, master, true)

	dpapi := testStore(t)
	if err := loadTestVault(dpapi, &fakeProtector{}).InitDPAPI(ctx, ""); err != nil {
		t.Fatal(err)
	}
	requireKeyCheckStored(t, dpapi, true)
}

func TestKeyCheckTamperedRejectsMasterUnlock(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	v := loadTestVault(db, &fakeProtector{})
	if err := v.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if err := v.UnlockMaster(ctx, "correct-password"); err != nil {
		t.Fatalf("matching key-check must unlock: %v", err)
	}
	v.Lock()
	for name, value := range map[string]string{
		"foreign dek": mustSealKeyCheck(t, testDEK(9)),
		"bad base64":  "!!!not-base64!!!",
		"too short":   base64.StdEncoding.EncodeToString([]byte("x")),
	} {
		if err := db.SettingSet(ctx, testKeyCheckSetting, value); err != nil {
			t.Fatal(err)
		}
		requireVaultCode(t, v.UnlockMaster(ctx, "correct-password"), ipc.CodeCrypto)
		if v.Status().Unlocked {
			t.Fatalf("%s: mismatching key-check must leave the vault locked", name)
		}
	}
}

func mustSealKeyCheck(t *testing.T, dek *secretKey) string {
	t.Helper()
	raw, err := sealKeyCheck(dek)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestKeyCheckSealVerifyRoundTrip(t *testing.T) {
	raw := mustSealKeyCheck(t, testDEK(1))
	if err := verifyKeyCheck(testDEK(1), raw); err != nil {
		t.Fatal(err)
	}
	if err := verifyKeyCheck(testDEK(2), raw); err == nil {
		t.Fatal("wrong DEK must fail verification")
	}
}

func TestLegacyVaultWithoutKeyCheckAdoptsMatchingDEK(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	p := &fakeProtector{}
	dek := testDEK(3)
	seedDPAPIVault(t, db, p, dek)
	row := putCredential(t, db, dek, "legacy-secret")

	v := loadTestVault(db, p)
	if !v.Status().Unlocked {
		t.Fatal("legacy vault with matching DEK must unlock")
	}
	plaintext, err := v.DecryptCredential(ctx, row)
	if err != nil || string(plaintext) != "legacy-secret" {
		t.Fatalf("plaintext=%q err=%v", plaintext, err)
	}
	requireKeyCheckStored(t, db, true)
}

func TestLegacyVaultWithoutKeyCheckRejectsWrongDEK(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	p := &fakeProtector{}
	seedDPAPIVault(t, db, p, testDEK(3))
	putCredential(t, db, testDEK(3), "legacy-secret")
	// The platform now hands back a different key, as after a keychain overwrite.
	if _, err := p.Protect(testDEK(7).bytes[:]); err != nil {
		t.Fatal(err)
	}

	v := loadTestVault(db, p)
	if v.Status().Unlocked {
		t.Fatal("mismatching DEK must not unlock")
	}
	_, err := v.EncryptSecret(ctx, "x")
	requireVaultCode(t, err, ipc.CodeCrypto)
	requireKeyCheckStored(t, db, false)
}

func TestLegacyVaultWithoutCiphertextUnlocksAndAdopts(t *testing.T) {
	db := testStore(t)
	p := &fakeProtector{}
	seedDPAPIVault(t, db, p, testDEK(3))
	v := loadTestVault(db, p)
	if !v.Status().Unlocked {
		t.Fatal("vault without ciphertext must unlock")
	}
	requireKeyCheckStored(t, db, true)
}

func TestLegacyVaultProbeToleratesCorruptCredential(t *testing.T) {
	db := testStore(t)
	p := &fakeProtector{}
	dek := testDEK(3)
	seedDPAPIVault(t, db, p, dek)
	putCorruptCredential(t, db)
	putCredential(t, db, dek, "good")
	v := loadTestVault(db, p)
	if !v.Status().Unlocked {
		t.Fatal("one decryptable credential must be enough to unlock")
	}
	requireKeyCheckStored(t, db, true)
}

func TestLegacyVaultProbeUsesSecretSettings(t *testing.T) {
	for name, wrapper := range map[string]string{"plain": "", "embedded in json": `,"model":"x"`} {
		t.Run(name, func(t *testing.T) {
			db := testStore(t)
			p := &fakeProtector{}
			dek := testDEK(3)
			seedDPAPIVault(t, db, p, dek)
			putSecretSetting(t, db, "ai.provider.key", dek, "sk-test", wrapper)
			v := loadTestVault(db, p)
			if !v.Status().Unlocked {
				t.Fatal("matching enc:v1 setting must unlock")
			}
			requireKeyCheckStored(t, db, true)

			wrongDB := testStore(t)
			wrongP := &fakeProtector{}
			seedDPAPIVault(t, wrongDB, wrongP, testDEK(3))
			putSecretSetting(t, wrongDB, "ai.provider.key", testDEK(8), "sk-test", wrapper)
			wrong := loadTestVault(wrongDB, wrongP)
			if wrong.Status().Unlocked {
				t.Fatal("enc:v1 setting under another DEK must not unlock")
			}
			requireKeyCheckStored(t, wrongDB, false)
		})
	}
}

func TestLegacyEnvelopeMigratesAfterVerifiedUnlock(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	p := newEnvelopeProtector()
	dek := testDEK(4)
	p.setLegacy(dek.bytes[:])
	row := putCredential(t, db, dek, "migrate-me")
	if err := db.SettingSetMany(ctx, map[string]string{
		settingMode:     string(ModeDPAPI),
		settingEnvelope: base64.StdEncoding.EncodeToString([]byte("env:v1")),
	}); err != nil {
		t.Fatal(err)
	}

	v := loadTestVault(db, p)
	if !v.Status().Unlocked {
		t.Fatal("legacy envelope must stay readable")
	}
	stored, _, _ := db.SettingGet(ctx, settingEnvelope)
	raw, _ := base64.StdEncoding.DecodeString(stored)
	if p.IsLegacyEnvelope(raw) || len(raw) == 0 {
		t.Fatalf("envelope was not rewritten: %q", raw)
	}
	again := loadTestVault(db, p)
	plaintext, err := again.DecryptCredential(ctx, row)
	if err != nil || string(plaintext) != "migrate-me" {
		t.Fatalf("after migration plaintext=%q err=%v", plaintext, err)
	}
}

func TestLegacyEnvelopeMigrationFailureKeepsUnlockedAndEnvelope(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	p := newEnvelopeProtector()
	dek := testDEK(4)
	p.setLegacy(dek.bytes[:])
	putCredential(t, db, dek, "migrate-me")
	if err := db.SettingSetMany(ctx, map[string]string{
		settingMode:     string(ModeDPAPI),
		settingEnvelope: base64.StdEncoding.EncodeToString([]byte("env:v1")),
	}); err != nil {
		t.Fatal(err)
	}
	p.failProtect = true
	v := loadTestVault(db, p)
	if !v.Status().Unlocked {
		t.Fatal("failed migration must not lock the vault")
	}
	stored, _, _ := db.SettingGet(ctx, settingEnvelope)
	if raw, _ := base64.StdEncoding.DecodeString(stored); string(raw) != "env:v1" {
		t.Fatalf("envelope changed despite failed Protect: %q", raw)
	}
}

func TestLegacyEnvelopeNotMigratedWhenDEKMismatches(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	p := newEnvelopeProtector()
	p.setLegacy(testDEK(5).bytes[:])
	putCredential(t, db, testDEK(4), "other-dek")
	if err := db.SettingSetMany(ctx, map[string]string{
		settingMode:     string(ModeDPAPI),
		settingEnvelope: base64.StdEncoding.EncodeToString([]byte("env:v1")),
	}); err != nil {
		t.Fatal(err)
	}
	v := loadTestVault(db, p)
	if v.Status().Unlocked {
		t.Fatal("mismatching DEK must not unlock")
	}
	if p.protects != 0 {
		t.Fatalf("mismatching DEK must not be migrated, Protect called %d times", p.protects)
	}
	stored, _, _ := db.SettingGet(ctx, settingEnvelope)
	if raw, _ := base64.StdEncoding.DecodeString(stored); string(raw) != "env:v1" {
		t.Fatalf("envelope changed: %q", raw)
	}
}

func TestSeparateEnvelopesKeepTwoDatabasesIndependent(t *testing.T) {
	ctx := context.Background()
	p := newEnvelopeProtector()
	dbA, dbB := testStore(t), testStore(t)
	a := loadTestVault(dbA, p)
	if err := a.InitDPAPI(ctx, ""); err != nil {
		t.Fatal(err)
	}
	nonce, blob, err := a.EncryptCredential(ctx, "secret-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbA.CredentialPut(ctx, store.CredentialInput{Name: "a", Kind: "password", Nonce: nonce, Blob: blob}); err != nil {
		t.Fatal(err)
	}
	b := loadTestVault(dbB, p)
	if err := b.InitDPAPI(ctx, ""); err != nil {
		t.Fatal(err)
	}

	reloaded := loadTestVault(dbA, p)
	if !reloaded.Status().Unlocked {
		t.Fatal("database A must stay unlocked after database B initialised against the same protector")
	}
	row := store.CredentialRow{Cipher: store.CipherAES256GCM, Nonce: nonce, Blob: blob}
	plaintext, err := reloaded.DecryptCredential(ctx, row)
	if err != nil || string(plaintext) != "secret-a" {
		t.Fatalf("plaintext=%q err=%v", plaintext, err)
	}
}

func TestDPAPIKeyMismatchReportsCryptoCode(t *testing.T) {
	ctx := context.Background()
	p := newEnvelopeProtector()
	db := testStore(t)
	dek := testDEK(9)
	putCredential(t, db, testDEK(50), "other-key")
	seedDPAPIVault(t, db, p, dek)
	v := loadTestVault(db, p)
	requireVaultCode(t, v.InitDPAPI(ctx, ""), ipc.CodeCrypto)
	if v.Status().Unlocked {
		t.Fatal("mismatched DPAPI key must not unlock")
	}
}

func requireOnlyIssuedEnvelopeDiscarded(t *testing.T, p *envelopeProtector, issuedBefore int) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.issued) != issuedBefore+1 || len(p.discarded) != 1 {
		t.Fatalf("issued=%d discarded=%d, want one new envelope discarded", len(p.issued)-issuedBefore, len(p.discarded))
	}
	if string(p.discarded[0]) != string(p.issued[len(p.issued)-1]) {
		t.Fatalf("discarded %q, want the freshly issued %q", p.discarded[0], p.issued[len(p.issued)-1])
	}
}

func TestFirstDEKPersistFailureDiscardsIssuedEnvelope(t *testing.T) {
	db := testStore(t)
	p := newEnvelopeProtector()
	p.afterIssue = func() { _ = db.Close() }
	v := loadTestVault(db, p)
	if err := v.InitDPAPI(context.Background(), ""); err == nil {
		t.Fatal("expected persistence failure")
	}
	requireOnlyIssuedEnvelopeDiscarded(t, p, 0)
	if p.liveEntries() != 0 {
		t.Fatalf("live entries=%d", p.liveEntries())
	}
}

func TestLegacyMigrationPersistFailureDiscardsOnlyNewEnvelope(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	p := newEnvelopeProtector()
	dek := testDEK(4)
	p.setLegacy(dek.bytes[:])
	putCredential(t, db, dek, "keep")
	if err := db.SettingSetMany(ctx, map[string]string{
		settingMode:     string(ModeDPAPI),
		settingEnvelope: base64.StdEncoding.EncodeToString([]byte("env:v1")),
	}); err != nil {
		t.Fatal(err)
	}
	p.afterIssue = func() { _ = db.Close() }
	if v := loadTestVault(db, p); !v.Status().Unlocked {
		t.Fatal("vault must stay unlocked when migration cannot persist")
	}
	requireOnlyIssuedEnvelopeDiscarded(t, p, 0)
	if _, ok := p.keys["env:v1"]; !ok {
		t.Fatal("the old envelope must not be discarded")
	}
}

func TestMasterToDPAPIPersistFailureDiscardsIssuedEnvelope(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	p := newEnvelopeProtector()
	v := loadTestVault(db, p)
	if err := v.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	p.afterIssue = func() { _ = db.Close() }
	if err := v.InitDPAPI(ctx, "correct-password"); err == nil {
		t.Fatal("expected persistence failure")
	}
	requireOnlyIssuedEnvelopeDiscarded(t, p, 0)
}
