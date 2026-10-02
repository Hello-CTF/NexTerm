package vault

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	privateKey := "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----"
	for _, test := range []struct {
		input  string
		secret string
	}{
		{"password=hunter2 x=1", "hunter2"},
		{"api_key: sk-123456", "sk-123456"},
		{"mysql -uroot -pSup3rSecret db", "Sup3rSecret"},
		{"redis -a Sup3rSecret", "Sup3rSecret"},
		{"Authorization: Bearer abc.def.ghi", "abc.def.ghi"},
		{privateKey, "abc"},
		{"redis://admin:Secret1@host:6379/0", "Secret1"},
	} {
		if got := Redact(test.input); strings.Contains(got, test.secret) || !strings.Contains(got, "***") {
			t.Errorf("Redact(%q)=%q", test.input, got)
		}
	}
	if got := Redact("docker ps -a"); got != "docker ps -a" {
		t.Fatalf("normal text changed: %q", got)
	}
}
