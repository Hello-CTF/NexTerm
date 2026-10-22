package sync

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

type testInstance struct {
	db      *store.Store
	vault   *vault.Vault
	service *Service
}

// syncTestProtector stands in for platform key storage while the vault still
// encrypts credentials and settings with its production DEK implementation.
type syncTestProtector struct {
	key [32]byte
}

func (p *syncTestProtector) Protect(data []byte) ([]byte, error) {
	block, err := aes.NewCipher(p.key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, data, nil), nil
}

func (p *syncTestProtector) Unprotect(envelope []byte) ([]byte, error) {
	block, err := aes.NewCipher(p.key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(envelope) < aead.NonceSize() {
		return nil, errors.New("invalid test envelope")
	}
	return aead.Open(nil, envelope[:aead.NonceSize()], envelope[aead.NonceSize():], nil)
}

func newSyncTestVault(t *testing.T, db *store.Store, masterPassword string) *vault.Vault {
	t.Helper()
	ctx := context.Background()
	protector := &syncTestProtector{}
	if _, err := rand.Read(protector.key[:]); err != nil {
		t.Fatal(err)
	}
	credentialVault := vault.Load(ctx, db, vault.WithProtector(protector))
	var err error
	if masterPassword != "" {
		err = credentialVault.InitMaster(ctx, masterPassword)
	} else {
		err = credentialVault.InitDPAPI(ctx, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(credentialVault.Lock)
	return credentialVault
}

func newTestInstance(t *testing.T, unlocked bool, options ...Option) *testInstance {
	return newTestInstanceWithMaster(t, unlocked, false, options...)
}

func newMasterTestInstance(t *testing.T, options ...Option) *testInstance {
	return newTestInstanceWithMaster(t, true, true, options...)
}

func newTestInstanceWithMaster(t *testing.T, unlocked, master bool, options ...Option) *testInstance {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.AssetEnsureBuiltinLocal(ctx); err != nil {
		t.Fatal(err)
	}
	var credentialVault *vault.Vault
	if unlocked {
		password := ""
		if master {
			password = "sync-test-master"
		}
		credentialVault = newSyncTestVault(t, db, password)
	} else {
		credentialVault = vault.Load(ctx, db)
	}
	return &testInstance{db: db, vault: credentialVault, service: New(db, credentialVault, append([]Option{WithMetadata("test", true)}, options...)...)}
}

func testPtr[T any](value T) *T { return &value }

// waitForClockToAdvance blocks until the wall clock's millisecond reading is
// strictly newer than the moment of the call, so the next timestamped write
// (an LWW revision or VerifiedAt) is ordered after everything that came
// before. It is a condition wait with a single bounded deadline: the timer is
// created once and reused across iterations, stopped on return, and the wait
// fails loudly when the deadline fires without the clock advancing.
func waitForClockToAdvance(t *testing.T) {
	t.Helper()
	if !waitForClockToAdvanceWith(time.Now, 2*time.Second) {
		t.Fatal("wall clock did not advance past the previous millisecond within 2s")
	}
}

// waitForClockToAdvanceWith reports whether the wall clock advanced past the
// moment of the call within timeout. now is injectable so tests can freeze or
// roll the clock without real time passing; the timer is created once and
// reused across iterations so the bound is real even when the clock stalls.
func waitForClockToAdvanceWith(now func() time.Time, timeout time.Duration) bool {
	from := now().UnixMilli()
	ticker := time.NewTicker(50 * time.Microsecond)
	defer ticker.Stop()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for now().UnixMilli() <= from {
		select {
		case <-ticker.C:
		case <-timer.C:
			return false
		}
	}
	return true
}

func float64PointersEqual(left, right *float64) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func requireCode(t *testing.T, err error, code ipc.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %s", code)
	}
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("expected error code %s, got %v", code, err)
	}
}

func putTestCredential(t *testing.T, instance *testInstance, id, name, kind, secret string) {
	t.Helper()
	nonce, blob, err := instance.vault.EncryptCredential(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.db.CredentialPut(context.Background(), store.CredentialInput{
		ID: id, Name: name, Kind: kind, Nonce: nonce, Blob: blob, KEKHint: instance.vault.KEKHint(),
	}); err != nil {
		t.Fatal(err)
	}
}

func putTestGroup(t *testing.T, instance *testInstance, id string, parentID *string, name string) {
	t.Helper()
	if _, err := instance.db.GroupUpsert(context.Background(), id, parentID, name, 0, 1, 1); err != nil {
		t.Fatal(err)
	}
}

func putTestAsset(t *testing.T, instance *testInstance, row store.AssetRow) store.AssetRow {
	t.Helper()
	created, err := instance.db.AssetUpsert(context.Background(), row)
	if err != nil {
		t.Fatal(err)
	}
	result, err := instance.db.AssetGet(context.Background(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = created
	return result
}

func TestWaitForClockToAdvance(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)

	t.Run("advance returns true promptly", func(t *testing.T) {
		calls := 0
		now := func() time.Time {
			calls++
			if calls == 1 {
				return base
			}
			return base.Add(time.Millisecond)
		}
		if !waitForClockToAdvanceWith(now, 50*time.Millisecond) {
			t.Fatal("clock advanced but the wait reported a timeout")
		}
	})

	t.Run("frozen clock fails within the budget", func(t *testing.T) {
		now := func() time.Time { return base }
		if waitForClockToAdvanceWith(now, 50*time.Millisecond) {
			t.Fatal("frozen clock must not report success")
		}
	})

	t.Run("rolled-back clock fails within the budget", func(t *testing.T) {
		calls := 0
		now := func() time.Time {
			calls++
			if calls == 1 {
				return base
			}
			return base.Add(-time.Hour)
		}
		if waitForClockToAdvanceWith(now, 50*time.Millisecond) {
			t.Fatal("rolled-back clock must not report success")
		}
	})
}
