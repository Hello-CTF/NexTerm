package vault

import "testing"

func TestPrivateKeyPayloadJSONForms(t *testing.T) {
	referenced := ReferencedPrivateKey("C:/Users/x/.ssh/id_rsa", stringPtr(" pw "))
	parsed := ParsePrivateKeyPayload(referenced.Encode())
	if !parsed.IsRef() || parsed.File == nil || *parsed.File != "C:/Users/x/.ssh/id_rsa" || parsed.Passphrase == nil || *parsed.Passphrase != "pw" {
		t.Fatalf("referenced payload=%+v", parsed)
	}
}

func TestPrivateKeyPayloadRequiresStructuredKeyOrReference(t *testing.T) {
	pem := "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----"
	for _, raw := range []string{pem, "{}", `{"passphrase":"orphan"}`, "{not-json"} {
		payload := ParsePrivateKeyPayload(raw)
		if payload.Key != nil || payload.File != nil || payload.Passphrase != nil {
			t.Errorf("non-payload %q parsed as %+v", raw, payload)
		}
	}
}

func TestPrivateKeyPassphraseNormalization(t *testing.T) {
	payload := InlinePrivateKey("KEY", stringPtr("   "))
	if payload.Passphrase != nil {
		t.Fatal("blank passphrase should be absent")
	}
}
