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
	key := make([]byte, DEKLength)
	for i := range key {
		key[i] = byte(i)
	}
	first, err := systemProtect(key)
	if err != nil {
		t.Fatal(err)
	}
	second, err := systemProtect(key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanupKeychainForTests(first, second) })
	if bytes.Equal(first, second) {
		t.Fatal("every protected key needs its own keychain account")
	}
	if systemEnvelopeIsLegacy(first) {
		t.Fatal("new envelopes are not legacy")
	}
	for _, envelope := range [][]byte{first, second} {
		opened, err := systemUnprotect(envelope)
		if err != nil || !bytes.Equal(opened, key) {
			t.Fatalf("keychain round trip opened=%x err=%v", opened, err)
		}
	}
}

func TestKeychainLegacyEnvelopeIsDetected(t *testing.T) {
	if !systemEnvelopeIsLegacy(keychainLegacyMarker) {
		t.Fatal("v1 marker must be reported as legacy")
	}
}

func TestKeychainRejectsForeignEnvelope(t *testing.T) {
	if _, err := systemUnprotect([]byte("not-keychain")); err == nil {
		t.Fatal("foreign envelope must fail before accessing Keychain")
	}
}
