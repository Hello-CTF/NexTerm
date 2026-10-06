package vault

import (
	"bytes"
	"strings"
	"testing"
)

func TestUserDEKPasswordEnvelopeRoundtrip(t *testing.T) {
	dek, envelopes, recoveryKey, err := GenerateUserDEKEnvelopes("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if len(dek) != DEKLength {
		t.Fatalf("dek length=%d", len(dek))
	}
	unwrapped, err := UnwrapUserDEK("correct horse battery staple", envelopes.DEKEnvelope, envelopes.KDFSalt, envelopes.KDFParams)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(unwrapped, dek) {
		t.Fatal("unwrapped dek mismatch")
	}
	if _, err := UnwrapUserDEK("wrong password", envelopes.DEKEnvelope, envelopes.KDFSalt, envelopes.KDFParams); err == nil {
		t.Fatal("wrong password opened envelope")
	}
	recovered, err := UnwrapUserDEKWithRecovery(recoveryKey, envelopes.RecoveryEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(recovered, dek) {
		t.Fatal("recovery unwrapped dek mismatch")
	}
	if _, err := UnwrapUserDEKWithRecovery("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", envelopes.RecoveryEnvelope); err == nil {
		t.Fatal("wrong recovery key opened envelope")
	}
	if _, err := UnwrapUserDEKWithRecovery(recoveryKey, envelopes.DEKEnvelope); err == nil {
		t.Fatal("password envelope opened under recovery AAD")
	}
	if _, err := UnwrapUserDEK("correct horse battery staple", envelopes.RecoveryEnvelope, envelopes.KDFSalt, envelopes.KDFParams); err == nil {
		t.Fatal("recovery envelope opened under password AAD")
	}
}

func TestRecoveryKeyFormatAndNormalization(t *testing.T) {
	canonical, err := GenerateRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != 32 {
		t.Fatalf("recovery key length=%d, want 32", len(canonical))
	}
	formatted := FormatRecoveryKey(canonical)
	if strings.Count(formatted, "-") != 7 {
		t.Fatalf("recovery key groups=%q", formatted)
	}
	if NormalizeRecoveryKey(formatted) != canonical {
		t.Fatalf("normalize roundtrip failed: %q", formatted)
	}
	if NormalizeRecoveryKey(strings.ToLower(formatted)) != canonical {
		t.Fatal("lowercase normalization failed")
	}
	hash := RecoveryKeyHash(canonical)
	if !VerifyRecoveryKey(formatted, hash) || !VerifyRecoveryKey(strings.ToLower(formatted), hash) {
		t.Fatal("verify failed for formatted or lowercase key")
	}
	if VerifyRecoveryKey("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", hash) {
		t.Fatal("verify accepted wrong key")
	}
}

func TestUserDEKRewrapOnlyPasswordChange(t *testing.T) {
	dek, envelopes, _, err := GenerateUserDEKEnvelopes("old-password-1")
	if err != nil {
		t.Fatal(err)
	}
	dekKey := &secretKey{}
	copy(dekKey.bytes[:], dek)
	ciphertextNonce, ciphertext, err := seal(dekKey, []byte("synthetic credential payload"), credentialAAD)
	if err != nil {
		t.Fatal(err)
	}

	unwrapped, err := UnwrapUserDEK("old-password-1", envelopes.DEKEnvelope, envelopes.KDFSalt, envelopes.KDFParams)
	if err != nil {
		t.Fatal(err)
	}
	newEnvelope, newSalt, newParams, err := WrapUserDEK("new-password-2", unwrapped)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(newEnvelope, envelopes.DEKEnvelope) || bytes.Equal(newSalt, envelopes.KDFSalt) {
		t.Fatal("rewrap did not change envelope bytes")
	}
	again, err := UnwrapUserDEK("new-password-2", newEnvelope, newSalt, newParams)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, dek) {
		t.Fatal("rewrapped dek mismatch")
	}
	rewrappedKey := &secretKey{}
	copy(rewrappedKey.bytes[:], again)
	plaintext, err := open(rewrappedKey, ciphertextNonce, ciphertext, credentialAAD)
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != "synthetic credential payload" {
		t.Fatal("ciphertext under original dek no longer opens after rewrap")
	}
}

func TestUserDEKKDFParamsBounds(t *testing.T) {
	if _, err := parseUserDEKKDFParams(`{"t":3,"m":65536,"p":4}`); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"t":0,"m":65536,"p":4}`,
		`{"t":3,"m":1024,"p":4}`,
		`{"t":3,"m":4194304,"p":4}`,
		`{"t":3,"m":65536,"p":0}`,
		`not-json`,
	} {
		if _, err := parseUserDEKKDFParams(raw); err == nil {
			t.Errorf("params %q accepted", raw)
		}
	}
}
