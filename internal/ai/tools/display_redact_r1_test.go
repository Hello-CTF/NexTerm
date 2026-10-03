package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestR1DisplayIncludesSubmittedKeys(t *testing.T) {
	display := DisplayCall(Call{Name: "send_keys", Args: json.RawMessage(`{"keys":"touch /tmp/x<enter>","enter":false}`)})
	if !strings.Contains(display, "touch /tmp/x") {
		t.Fatalf("display = %q", display)
	}
}

func TestR1AuditRedactsAuthorizationAndInlineMySQLPassword(t *testing.T) {
	value := RedactText("Authorization: Bearer topsecret mysql -pSecret")
	for _, secret := range []string{"topsecret", "Secret"} {
		if strings.Contains(value, secret) {
			t.Fatalf("secret %q remains in %q", secret, value)
		}
	}
	redacted := redactArguments(json.RawMessage(`{"commands":["mysql -pSecret","curl -H 'Authorization: Bearer topsecret'"]}`))
	if text := fmt.Sprint(redacted); strings.Contains(text, "Secret") || strings.Contains(text, "topsecret") {
		t.Fatalf("arguments contain credentials: %s", text)
	}
}
