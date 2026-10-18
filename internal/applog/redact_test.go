package applog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactText(t *testing.T) {
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
		{`{"password":"json-value","role":"admin"}`, "json-value"},
		{`{"password":"prefix\"suffix-value","role":"admin"}`, "suffix-value"},
		{"password='two words' role=admin", "two words"},
	} {
		if got := RedactText(test.input); strings.Contains(got, test.secret) || !strings.Contains(got, "***") {
			t.Errorf("RedactText(%q)=%q", test.input, got)
		}
	}
	if got := RedactText("docker ps -a"); got != "docker ps -a" {
		t.Fatalf("normal text changed: %q", got)
	}
	redacted := RedactText(`{"password":"prefix\"suffix-value","role":"admin"}`)
	var payload map[string]string
	if err := json.Unmarshal([]byte(redacted), &payload); err != nil || payload["password"] != "***" || payload["role"] != "admin" {
		t.Fatalf("redacted JSON is invalid or changed unrelated fields: %q, %v", redacted, err)
	}
}
