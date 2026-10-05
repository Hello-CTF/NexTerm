package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRedactTextCoversBareCredentialFormats(t *testing.T) {
	t.Parallel()
	tests := []struct {
		content string
		secret  string
	}{
		{"use sk-abcdefghijklmnopqrstuvwxyz012345 for payments", "sk-abcdefghijklmnopqrstuvwxyz012345"},
		{"key " + "sk-proj-abcdefghijklmnopqrstuvwxyz0123456789 ok", "sk-proj-abcdefghijklmnopqrstuvwxyz0123456789"},
		{"token " + "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789", "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"},
		{"github " + "ghp_abcdefghijklmnopqrstuvwxyz0123456789 end", "ghp_abcdefghijklmnopqrstuvwxyz0123456789"},
		{"github " + "github_pat_abcdefghijklmnopqrstuvwxyz0123456789_ab end", "github_pat_abcdefghijklmnopqrstuvwxyz0123456789_ab"},
		{"slack " + "xoxb-123456789012-abcdefghijklmnopqrstuv end", "xoxb-123456789012-abcdefghijklmnopqrstuv"},
		{"aws " + "AKIAIOSFODNN7EXAMPLE end", "AKIAIOSFODNN7EXAMPLE"},
		{"aws " + "ASIAIOSFODNN7EXAMPLE end", "ASIAIOSFODNN7EXAMPLE"},
		{"google " + "AIzaSyDabcdefghijklmnopqrstuvwxyz012345 end", "AIzaSyDabcdefghijklmnopqrstuvwxyz012345"},
		{"run mysql -prootpass -h db1 nightly", "-prootpass"},
		{"Authorization: Basic dXNlcjpwYXNzd29yZA==", "dXNlcjpwYXNzd29yZA=="},
		{"Authorization: Digest abcdefghijklmnop", "Digest abcdefghijklmnop"},
		{"apikey=hunter2secret", "hunter2secret"},
		{"access_key=hunter2secret", "hunter2secret"},
		{"private_key=hunter2secret", "hunter2secret"},
		{"secret_key=hunter2secret", "hunter2secret"},
		{"auth_token=hunter2secret", "hunter2secret"},
		{"id_token=hunter2secret", "hunter2secret"},
		{"passphrase=hunter2secret", "hunter2secret"},
	}
	for _, test := range tests {
		redacted, changed := RedactText(test.content)
		if !changed || strings.Contains(redacted, test.secret) {
			t.Errorf("RedactText(%q) = %q, changed = %v", test.content, redacted, changed)
		}
	}
}

func TestRedactTextLeavesCredentialLikeProseUnchanged(t *testing.T) {
	t.Parallel()
	benign := []string{
		"ask-me-anything session notes",
		"desk-abcdefghijklmnop naming convention",
		"short sk-12345 code",
		"the api key rotates monthly",
		"password reset link expires in 1h",
		"mysql -p prompts for a password",
		"tokens and keys are managed by the vault",
		"github repository pat is short",
		"akia lowercase is not an aws key",
	}
	for _, content := range benign {
		redacted, changed := RedactText(content)
		if changed || redacted != content {
			t.Errorf("RedactText(%q) = %q, changed = %v", content, redacted, changed)
		}
	}
}

func TestBareCredentialRejectedRedactedAndInjectedSafely(t *testing.T) {
	ctx := context.Background()
	const secret = "sk-abcdefghijklmnopqrstuvwxyz012345"
	content := "payments key " + secret + " rotate monthly"
	store, _ := newMemoryStore(t)
	if _, err := store.Create(ctx, testScope, CreateInput{Topic: "credential", Content: content}); !errors.Is(err, ErrSensitiveContent) {
		t.Fatalf("bare credential create err = %v", err)
	}
	entry, err := store.Create(ctx, testScope, CreateInput{Topic: "credential", Content: content, Secrets: SecretRedact})
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Redacted || strings.Contains(entry.Content, secret) {
		t.Fatalf("redacted entry = %+v", entry)
	}
	var persisted string
	if err := store.db.QueryRow("SELECT content FROM memory_entry WHERE id = ?", entry.ID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(persisted, secret) {
		t.Fatalf("database contains bare credential: %q", persisted)
	}
	mustEnableInjection(t, store, testScope)
	injection, err := store.Inject(ctx, testScope, nil, Selection{}, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if injection.Memory == nil || strings.Contains(injection.Memory.Content, secret) {
		t.Fatalf("prompt contains bare credential: %+v", injection)
	}
}
