package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"
)

const officialMinisignPublicKey = "RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3"

const officialPrehashedSignature = "untrusted comment: signature from minisign secret key\n" +
	"RUQf6LRCGA9i559r3g7V1qNyJDApGip8MfqcadIgT9CuhV3EMhHoN1mGTkUidF/z7SrlQgXdy8ofjb7bNJJylDOocrCo8KLzZwo=\n" +
	"trusted comment: timestamp:1635443258\tfile:test\thashed\n" +
	"/cj37GK60vryibFn+ftOgbCvW9NKhKYgjVpFFQUcWPAnjO23wrvVDTt7cloNC06maoBli9q6qwZDXXoaxweICQ==\n"

func TestVerifyMinisignOfficialPrehashedVector(t *testing.T) {
	publicKey, err := parseMinisignPublicKey("untrusted comment: official test key\n" + officialMinisignPublicKey + "\n")
	if err != nil {
		t.Fatal(err)
	}
	const trustedComment = "timestamp:1635443258\tfile:test\thashed"
	if err := verifyMinisign([]byte("test"), []byte(officialPrehashedSignature), publicKey, trustedComment); err != nil {
		t.Fatalf("official minisign vector failed: %v", err)
	}
	if err := verifyMinisign([]byte("changed"), []byte(officialPrehashedSignature), publicKey, trustedComment); err == nil {
		t.Fatal("changed message must fail signature verification")
	}
	if err := verifyMinisign([]byte("test"), []byte(officialPrehashedSignature), publicKey, "another release"); err == nil {
		t.Fatal("a trusted comment for another release must fail")
	}
}

func TestVerifyMinisignRejectsChangedSignatureKeyID(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(officialPrehashedSignature), "\n")
	signature, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(signature) != 74 {
		t.Fatalf("signature length = %d, want 74", len(signature))
	}
	original := append([]byte(nil), signature...)
	originalGlobalSignature := lines[3]
	for index := 2; index < 10; index++ {
		signature[index] ^= 0xff
	}
	if bytes.Equal(original[2:10], signature[2:10]) {
		t.Fatal("test must change the signature key ID")
	}
	if !bytes.Equal(original[:2], signature[:2]) || !bytes.Equal(original[10:], signature[10:]) {
		t.Fatal("test must leave the algorithm and primary signature unchanged")
	}
	lines[1] = base64.StdEncoding.EncodeToString(signature)
	if lines[3] != originalGlobalSignature {
		t.Fatal("test must leave the global signature unchanged")
	}

	publicKey, err := parseMinisignPublicKey(officialMinisignPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	const trustedComment = "timestamp:1635443258\tfile:test\thashed"
	mutatedSignature := strings.Join(lines, "\n") + "\n"
	if err := verifyMinisign([]byte("test"), []byte(mutatedSignature), publicKey, trustedComment); err == nil {
		t.Fatal("a signature with only its key ID changed must be rejected")
	}
}

func TestParseMinisignPublicKeyRequiresEdAlgorithm(t *testing.T) {
	decoded, err := base64.StdEncoding.DecodeString(officialMinisignPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 42 {
		t.Fatalf("public key length = %d, want 42", len(decoded))
	}
	tests := []struct {
		algorithm string
		wantError bool
	}{
		{algorithm: "Ed"},
		{algorithm: "ED", wantError: true},
		{algorithm: "XX", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.algorithm, func(t *testing.T) {
			candidate := append([]byte(nil), decoded...)
			copy(candidate[:2], test.algorithm)
			encoded := base64.StdEncoding.EncodeToString(candidate)
			_, err := parseMinisignPublicKey(encoded)
			if test.wantError && err == nil {
				t.Fatalf("public key with algorithm %q must be rejected", test.algorithm)
			}
			if !test.wantError && err != nil {
				t.Fatalf("public key with algorithm %q must be accepted: %v", test.algorithm, err)
			}
		})
	}
}

func TestVerifyMinisignRejectsLegacySignature(t *testing.T) {
	publicKey, err := parseMinisignPublicKey(officialMinisignPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	const legacySignature = "untrusted comment: signature from minisign secret key\n" +
		"RWQf6LRCGA9i59SLOFxz6NxvASXDJeRtuZykwQepbDEGt87ig1BNpWaVWuNrm73YiIiJbq71Wi+dP9eKL8OC351vwIasSSbXxwA=\n" +
		"trusted comment: timestamp:1635442742\tfile:test\n" +
		"0YteLgV960ia80vnA/fHbvkyjl/IoP/HNOCaZfrF0CdhAlp7ok+Tpkya+VpWPX5C/Is3q8a/kEDSY7fBmmgJCg==\n"
	if err := verifyMinisign([]byte("test"), []byte(legacySignature), publicKey, "timestamp:1635442742\tfile:test"); err == nil {
		t.Fatal("legacy non-prehashed signature must be rejected")
	}
}

func TestVerifyMinisignRejectsTampering(t *testing.T) {
	key := newTestMinisignKey(1)
	publicKey, err := parseMinisignPublicKey(key.encodedPublic)
	if err != nil {
		t.Fatal(err)
	}
	message := []byte("signed checksum manifest")
	const trustedComment = "NexTerm 1.2.3 SHA256SUMS"
	signature := key.sign(message, trustedComment)
	if err := verifyMinisign(message, []byte(signature), publicKey, trustedComment); err != nil {
		t.Fatal(err)
	}
	if err := verifyMinisign(message, []byte(key.sign([]byte("another manifest"), trustedComment)), publicKey, trustedComment); err == nil {
		t.Fatal("a signature for another manifest must fail")
	}
	tamperedComment := strings.Replace(signature, "trusted comment: "+trustedComment, "trusted comment: forged", 1)
	if err := verifyMinisign(message, []byte(tamperedComment), publicKey, "forged"); err == nil {
		t.Fatal("changing the trusted comment must fail global signature verification")
	}
	otherKey, err := parseMinisignPublicKey(newTestMinisignKey(2).encodedPublic)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyMinisign(message, []byte(signature), otherKey, trustedComment); err == nil {
		t.Fatal("a signature from an untrusted key must fail")
	}
}

func TestMinisignTrustConfiguration(t *testing.T) {
	legacy := &Manager{}
	if _, required, err := legacy.minisignTrust(); err != nil || required {
		t.Fatalf("legacy policy = required %v, err %v", required, err)
	}
	requiredWithoutKey := &Manager{RequireSignedChecksums: true}
	if _, _, err := requiredWithoutKey.minisignTrust(); err == nil {
		t.Fatal("required signatures without a trusted key must fail")
	}
	invalidKey := &Manager{MinisignPublicKey: "not-a-key"}
	if _, _, err := invalidKey.minisignTrust(); err == nil {
		t.Fatal("an invalid configured key must fail")
	}

	key := newTestMinisignKey(42)
	previousDefault := defaultMinisignPublicKey
	defaultMinisignPublicKey = key.encodedPublic
	defer func() { defaultMinisignPublicKey = previousDefault }()
	trust, required, err := (&Manager{}).minisignTrust()
	if err != nil || !required || trust.keyID != key.keyID {
		t.Fatalf("build-time key policy = key ID %x, required %v, err %v", trust.keyID, required, err)
	}
}

type testMinisignKey struct {
	keyID         [8]byte
	private       ed25519.PrivateKey
	encodedPublic string
}

func newTestMinisignKey(seed byte) testMinisignKey {
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	public := private.Public().(ed25519.PublicKey)
	keyID := [8]byte{seed, seed, seed, seed, seed, seed, seed, seed}
	encoded := make([]byte, 0, 42)
	encoded = append(encoded, 'E', 'd')
	encoded = append(encoded, keyID[:]...)
	encoded = append(encoded, public...)
	return testMinisignKey{
		keyID:         keyID,
		private:       private,
		encodedPublic: base64.StdEncoding.EncodeToString(encoded),
	}
}

func (key testMinisignKey) publicKeyFile() string {
	return "untrusted comment: test-only key\n" + key.encodedPublic + "\n"
}

func (key testMinisignKey) sign(message []byte, trustedComment string) string {
	digest := blake2b.Sum512(message)
	signature := ed25519.Sign(key.private, digest[:])
	encoded := make([]byte, 0, 74)
	encoded = append(encoded, 'E', 'D')
	encoded = append(encoded, key.keyID[:]...)
	encoded = append(encoded, signature...)
	globalMessage := make([]byte, 0, len(signature)+len(trustedComment))
	globalMessage = append(globalMessage, signature...)
	globalMessage = append(globalMessage, trustedComment...)
	globalSignature := ed25519.Sign(key.private, globalMessage)
	return fmt.Sprintf("untrusted comment: test-only signature\n%s\ntrusted comment: %s\n%s\n",
		base64.StdEncoding.EncodeToString(encoded), trustedComment, base64.StdEncoding.EncodeToString(globalSignature))
}
