package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestAccountCSRFRejectionLogged(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	var logs bytes.Buffer
	fixture.server.logger = slog.New(slog.NewTextHandler(&logs, nil))
	session, _ := fixture.initSuperadmin(t, "root", "password-csrf1")

	call := fixture.call(t, http.MethodPost, "/auth/logout", nil, session, "", nil)
	if call.status != http.StatusForbidden {
		t.Fatalf("csrf missing status=%d", call.status)
	}
	output := logs.String()
	for _, want := range []string{`level=WARN msg="account csrf rejected"`, "path=/auth/logout", "remote=127.0.0.1"} {
		if !strings.Contains(output, want) {
			t.Fatalf("csrf rejection log missing %q: %q", want, output)
		}
	}
	if strings.Contains(output, session.csrf) || strings.Contains(output, session.cookie.Value) {
		t.Fatalf("csrf log leaks session material: %q", output)
	}
}
