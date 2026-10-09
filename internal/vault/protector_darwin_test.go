//go:build darwin

package vault

import (
	"bytes"
	"os"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

func TestKeychainRoundTrip(t *testing.T) {
	if os.Getenv("NEXTERM_TEST_KEYCHAIN") != "1" {
		t.Skip("set NEXTERM_TEST_KEYCHAIN=1 to use the isolated Go login-keychain item")
	}
	productionAccount := keychainAccount
	keychainAccount = "vault-dek-go-test-" + ids.New()
	t.Cleanup(func() { keychainAccount = productionAccount })
	_ = cleanupKeychainForTests()
	t.Cleanup(func() { _ = cleanupKeychainForTests() })
	key := make([]byte, DEKLength)
	for i := range key {
		key[i] = byte(i)
	}
	envelope, err := systemProtect(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(envelope, keychainMarker) {
		t.Fatalf("envelope=%q", envelope)
	}
	opened, err := systemUnprotect(envelope)
	if err != nil || !bytes.Equal(opened, key) {
		t.Fatalf("keychain round trip opened=%x err=%v", opened, err)
	}
}

func TestKeychainRejectsForeignEnvelope(t *testing.T) {
	if _, err := systemUnprotect([]byte("not-keychain")); err == nil {
		t.Fatal("foreign envelope must fail before accessing Keychain")
	}
}
