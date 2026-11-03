package vault

import (
	"regexp"
	"strings"
	"testing"
)

func TestKeychainEnvelopeRoundTrip(t *testing.T) {
	envelope, account, err := newKeychainEnvelope("vault-dek-go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^nexterm-go:keychain:v2:[0-9a-f]{32}$`).Match(envelope) {
		t.Fatalf("envelope=%q", envelope)
	}
	if !regexp.MustCompile(`^vault-dek-go/[0-9a-f]{32}$`).MatchString(account) {
		t.Fatalf("account=%q", account)
	}
	parsed, legacy, err := parseKeychainEnvelope(envelope, "vault-dek-go")
	if err != nil || legacy || parsed != account {
		t.Fatalf("parsed=%q legacy=%v err=%v", parsed, legacy, err)
	}
	other, otherAccount, err := newKeychainEnvelope("vault-dek-go")
	if err != nil || string(other) == string(envelope) || otherAccount == account {
		t.Fatalf("ids must be unique: %q %q", other, otherAccount)
	}
}

func TestKeychainEnvelopeLegacyV1(t *testing.T) {
	account, legacy, err := parseKeychainEnvelope([]byte("nexterm-go:keychain:v1"), "vault-dek-go")
	if err != nil || !legacy || account != "vault-dek-go" {
		t.Fatalf("account=%q legacy=%v err=%v", account, legacy, err)
	}
}

func TestKeychainEnvelopeRejectsMalformed(t *testing.T) {
	id := strings.Repeat("a", 32)
	prefix := "nexterm-go:keychain:v2:"
	for name, envelope := range map[string]string{
		"empty":           "",
		"foreign":         "not-keychain",
		"v1 suffix":       "nexterm-go:keychain:v1:" + id,
		"v1 trailing":     "nexterm-go:keychain:v1 ",
		"no id":           prefix,
		"short id":        prefix + id[:31],
		"long id":         prefix + id + "a",
		"uppercase":       prefix + strings.Repeat("A", 32),
		"non hex":         prefix + strings.Repeat("g", 32),
		"space":           prefix + id[:31] + " ",
		"newline":         prefix + id + "\n",
		"inner newline":   prefix + id[:16] + "\n" + id[17:],
		"trailing suffix": prefix + id + "-x",
		"quote":           prefix + id[:31] + `"`,
	} {
		if account, _, err := parseKeychainEnvelope([]byte(envelope), "vault-dek-go"); err == nil {
			t.Errorf("%s: accepted, account=%q", name, account)
		}
	}
}
