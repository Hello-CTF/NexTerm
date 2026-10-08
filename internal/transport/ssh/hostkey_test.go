package ssh

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestHostKeyAlgorithmsCoexistAndReplacePerType(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryHostKeyStore()
	ed25519Key := hostKeyForTest(t)
	ecdsaPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecdsaSigner, err := gossh.NewSignerFromKey(ecdsaPrivate)
	if err != nil {
		t.Fatal(err)
	}
	ecdsaKey := ecdsaSigner.PublicKey()
	ed25519Info := newHostKey("example.test", 22, ed25519Key)
	if err := verifyHostKey(ctx, store, "example.test", 22, false, &HostKeyApproval{Fingerprint: ed25519Info.Fingerprint}, ed25519Key); err != nil {
		t.Fatal(err)
	}
	if err := verifyHostKey(ctx, store, "example.test", 22, false, nil, ecdsaKey); !errors.Is(err, ErrHostKeyPending) || errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("new algorithm was treated as a changed key: %v", err)
	}
	ecdsaInfo := newHostKey("example.test", 22, ecdsaKey)
	if err := verifyHostKey(ctx, store, "example.test", 22, false, &HostKeyApproval{Fingerprint: ecdsaInfo.Fingerprint}, ecdsaKey); err != nil {
		t.Fatal(err)
	}
	if err := verifyHostKey(ctx, store, "example.test", 22, false, nil, ed25519Key); err != nil {
		t.Fatalf("ed25519 trust was removed by ECDSA approval: %v", err)
	}
	if err := verifyHostKey(ctx, store, "example.test", 22, false, nil, ecdsaKey); err != nil {
		t.Fatalf("ECDSA trust was not persisted: %v", err)
	}

	replacement := hostKeyForTest(t)
	replacementInfo := newHostKey("example.test", 22, replacement)
	if err := verifyHostKey(ctx, store, "example.test", 22, false, &HostKeyApproval{Fingerprint: replacementInfo.Fingerprint, Replace: true}, replacement); err != nil {
		t.Fatal(err)
	}
	if err := verifyHostKey(ctx, store, "example.test", 22, false, nil, ecdsaKey); err != nil {
		t.Fatalf("per-type replacement removed ECDSA trust: %v", err)
	}
	if err := verifyHostKey(ctx, store, "example.test", 22, false, nil, ed25519Key); !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("replaced ed25519 key remained trusted: %v", err)
	}
	keys, err := store.HostKeys(ctx, "example.test", 22)
	if err != nil || len(keys) != 2 {
		t.Fatalf("coexisting host keys = %+v, %v", keys, err)
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

func TestFileHostKeyStoreWritesIndentedJSON(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hostkeys.json")
	store, err := NewFileHostKeyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutHostKey(ctx, newHostKey("example.test", 22, hostKeyForTest(t)), false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("\n  \"version\": 1")) {
		t.Fatalf("host key store is not indented JSON:\n%s", data)
	}
}

func TestHostKeyApprovalStoreLedgerApprovesExplicitlyAcceptedKeys(t *testing.T) {
	ctx := context.Background()
	first := hostKeyForTest(t)
	second := hostKeyForTest(t)
	firstInfo := newHostKey("example.test", 22, first)
	secondInfo := newHostKey("example.test", 22, second)

	store := &ledgerHostKeyStore{MemoryHostKeyStore: NewMemoryHostKeyStore()}
	store.approved = []HostKey{{Host: "example.test", Port: 22, KeyType: firstInfo.KeyType, Fingerprint: firstInfo.Fingerprint}}

	if err := verifyHostKey(ctx, store, "example.test", 22, false, nil, second); !errors.Is(err, ErrHostKeyPending) {
		t.Fatalf("ledger fingerprint mismatch accepted unknown key: %v", err)
	}
	if err := verifyHostKey(ctx, store, "other.test", 22, false, nil, first); !errors.Is(err, ErrHostKeyPending) {
		t.Fatalf("ledger entry leaked to a different host: %v", err)
	}
	if err := verifyHostKey(ctx, store, "example.test", 2222, false, nil, first); !errors.Is(err, ErrHostKeyPending) {
		t.Fatalf("ledger entry leaked to a different port: %v", err)
	}
	if err := verifyHostKey(ctx, store, "example.test", 22, false, nil, first); err != nil {
		t.Fatalf("ledger-approved unknown key rejected: %v", err)
	}
	keys, err := store.HostKeys(ctx, "example.test", 22)
	if err != nil || len(keys) != 1 || keys[0].Key != firstInfo.Key {
		t.Fatalf("ledger-approved key was not persisted: %+v, %v", keys, err)
	}

	if err := verifyHostKey(ctx, store, "example.test", 22, false, nil, second); !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("old ledger fingerprint approved a changed key: %v", err)
	}
	store.approved = []HostKey{{Host: "example.test", Port: 22, KeyType: "ssh-rsa", Fingerprint: secondInfo.Fingerprint}}
	if err := verifyHostKey(ctx, store, "example.test", 22, false, nil, second); !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("ledger entry with mismatched key type approved a changed key: %v", err)
	}
	store.approved = []HostKey{{Host: "example.test", Port: 22, KeyType: secondInfo.KeyType, Fingerprint: secondInfo.Fingerprint}}
	if err := verifyHostKey(ctx, store, "example.test", 22, false, nil, second); err != nil {
		t.Fatalf("ledger-approved changed key rejected: %v", err)
	}
	keys, err = store.HostKeys(ctx, "example.test", 22)
	if err != nil || len(keys) != 1 || keys[0].Key != secondInfo.Key {
		t.Fatalf("ledger-approved replacement was not persisted: %+v, %v", keys, err)
	}
	if err := verifyHostKey(ctx, store, "example.test", 22, false, nil, first); !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("replaced old key remained trusted: %v", err)
	}
}

type ledgerHostKeyStore struct {
	*MemoryHostKeyStore
	approved []HostKey
}

func (s *ledgerHostKeyStore) ApprovedHostKeys(ctx context.Context, host string, port int) ([]HostKey, error) {
	var result []HostKey
	for _, key := range s.approved {
		if key.Host == host && key.Port == port {
			result = append(result, key)
		}
	}
	return result, nil
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

func TestHostKeyErrorUserMessage(t *testing.T) {
	pending := &HostKeyError{Pending: true, Presented: HostKey{Host: "203.0.113.10", Port: 2222, KeyType: "ssh-ed25519", Fingerprint: "SHA256:pending"}}
	message := pending.UserMessage()
	for _, want := range []string{"203.0.113.10:2222", "SHA256:pending", "确认"} {
		if !strings.Contains(message, want) {
			t.Fatalf("pending UserMessage() = %q, want it to contain %q", message, want)
		}
	}
	changed := &HostKeyError{Presented: HostKey{Host: "203.0.113.10", Port: 2222, KeyType: "ssh-ed25519", Fingerprint: "SHA256:new"}, Known: []HostKey{{Host: "203.0.113.10", Port: 2222, KeyType: "ssh-ed25519", Fingerprint: "SHA256:old"}}}
	message = changed.UserMessage()
	for _, want := range []string{"203.0.113.10:2222", "SHA256:new", "不一致", "核对"} {
		if !strings.Contains(message, want) {
			t.Fatalf("changed UserMessage() = %q, want it to contain %q", message, want)
		}
	}
}
