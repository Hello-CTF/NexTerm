package vault

import "testing"

func TestPrivateKeyPayloadLegacyAndJSONForms(t *testing.T) {
	pem := "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----"
	legacy := ParsePrivateKeyPayload(pem)
	if legacy.Key == nil || *legacy.Key != pem || legacy.IsRef() || legacy.Passphrase != nil {
		t.Fatalf("legacy payload=%+v", legacy)
	}

	referenced := ReferencedPrivateKey("C:/Users/x/.ssh/id_rsa", stringPtr(" pw "))
	parsed := ParsePrivateKeyPayload(referenced.Encode())
	if !parsed.IsRef() || parsed.File == nil || *parsed.File != "C:/Users/x/.ssh/id_rsa" || parsed.Passphrase == nil || *parsed.Passphrase != "pw" {
		t.Fatalf("referenced payload=%+v", parsed)
	}

	empty := ParsePrivateKeyPayload("{}")
	if empty.Key == nil || *empty.Key != "{}" || empty.IsRef() {
		t.Fatalf("empty JSON must remain literal key content: %+v", empty)
	}
}

func TestPrivateKeyPassphraseNormalizationAndChange(t *testing.T) {
	payload := InlinePrivateKey("KEY", stringPtr("   "))
	if payload.Passphrase != nil {
		t.Fatal("blank passphrase should be absent")
	}
	payload = payload.WithPassphrase(stringPtr("new"))
	if payload.Key == nil || *payload.Key != "KEY" || payload.Passphrase == nil || *payload.Passphrase != "new" {
		t.Fatalf("payload=%+v", payload)
	}
	payload = payload.WithPassphrase(stringPtr(""))
	if payload.Passphrase != nil || payload.Key == nil {
		t.Fatalf("clearing passphrase changed payload incorrectly: %+v", payload)
	}
}
