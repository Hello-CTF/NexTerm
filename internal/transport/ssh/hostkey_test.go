package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

func TestHostKeyPendingChangedAndApproval(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryHostKeyStore()
	first := hostKeyForTest(t)
	second := hostKeyForTest(t)
	other := hostKeyForTest(t)

	err := verifyHostKey(ctx, store, "example.test", 22, false, nil, first)
	if !errors.Is(err, ErrHostKeyPending) {
		t.Fatalf("unknown key = %v", err)
	}
	var keyErr *HostKeyError
	if !errors.As(err, &keyErr) || !keyErr.Pending || keyErr.Presented.Fingerprint == "" {
		t.Fatalf("missing pending details: %#v", err)
	}
	if err := verifyHostKey(ctx, store, "example.test", 22, false, &HostKeyApproval{Fingerprint: "SHA256:wrong"}, first); !errors.Is(err, ErrHostKeyPending) {
		t.Fatalf("wrong approval accepted key: %v", err)
	}
	presented := newHostKey("example.test", 22, first)
	if err := verifyHostKey(ctx, store, "example.test", 22, false, &HostKeyApproval{Fingerprint: presented.Fingerprint}, first); err != nil {
		t.Fatal(err)
	}
	if err := verifyHostKey(ctx, store, "example.test", 22, false, nil, first); err != nil {
		t.Fatalf("persisted key rejected: %v", err)
	}

	if err := verifyHostKey(ctx, store, "example.test", 22, true, nil, second); !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("auto-accept-unknown replaced a changed key: %v", err)
	}
	secondInfo := newHostKey("example.test", 22, second)
	if err := verifyHostKey(ctx, store, "example.test", 22, false, &HostKeyApproval{Fingerprint: secondInfo.Fingerprint}, second); !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("approval without replace accepted change: %v", err)
	}
	if err := verifyHostKey(ctx, store, "example.test", 22, false, &HostKeyApproval{Fingerprint: secondInfo.Fingerprint, Replace: true}, second); err != nil {
		t.Fatal(err)
	}
	if err := verifyHostKey(ctx, store, "example.test", 22, false, nil, first); !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("old key remained accepted after replacement: %v", err)
	}
	if err := verifyHostKey(ctx, store, "example.test", 22, false, nil, other); !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("unapproved third key accepted: %v", err)
	}
}

func TestFileHostKeyStorePersistsAndConflicts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "hostkeys.json")
	store, err := NewFileHostKeyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	key := newHostKey("example.test", 2222, hostKeyForTest(t))
	if err := store.PutHostKey(ctx, key, false); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewFileHostKeyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := reloaded.HostKeys(ctx, key.Host, key.Port)
	if err != nil || len(keys) != 1 || keys[0].Fingerprint != key.Fingerprint {
		t.Fatalf("reloaded keys = %+v, %v", keys, err)
	}
	conflict := newHostKey(key.Host, key.Port, hostKeyForTest(t))
	if err := reloaded.PutHostKey(ctx, conflict, false); !errors.Is(err, ErrHostKeyConflict) {
		t.Fatalf("conflicting insert = %v", err)
	}
	if err := reloaded.PutHostKey(ctx, conflict, true); err != nil {
		t.Fatal(err)
	}
	keys, _ = reloaded.HostKeys(ctx, key.Host, key.Port)
	if len(keys) != 1 || keys[0].Fingerprint != conflict.Fingerprint {
		t.Fatalf("replacement keys = %+v", keys)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("host key file mode = %o", info.Mode().Perm())
		}
	}
	var disk map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &disk); err != nil || disk["version"] != float64(1) {
		t.Fatalf("store is not versioned JSON: %v, %s", err, data)
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	host, port, err := normalizeEndpoint("[2001:0db8::1]", 0)
	if err != nil || host != "2001:db8::1" || port != 22 {
		t.Fatalf("normalized = %q %d %v", host, port, err)
	}
	host, port, err = normalizeEndpoint("EXAMPLE.test", 2222)
	if err != nil || host != "example.test" || port != 2222 {
		t.Fatalf("normalized = %q %d %v", host, port, err)
	}
}

func hostKeyForTest(t *testing.T) gossh.PublicKey {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer.PublicKey()
}
