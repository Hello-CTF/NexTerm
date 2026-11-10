package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/metrics"
)

func scrape(server *Server) string {
	writer := metrics.NewWriter()
	server.WriteMetrics(writer)
	return string(writer.Bytes())
}

func TestMetricsCountLoginFailureWithoutUsername(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	_, _ = fixture.initSuperadmin(t, "alice", "password-alice")
	failure := `nexterm_auth_login_total{step="password",result="failure"} `
	if !strings.Contains(scrape(fixture.server), failure+"0\n") {
		t.Fatalf("failure counter not zero before login:\n%s", scrape(fixture.server))
	}

	fixture.login(t, "alice", "password-alice")
	call := fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "alice", "password": "wrong-password"}, nil, "", nil)
	if call.status != http.StatusForbidden {
		t.Fatalf("wrong password status = %d", call.status)
	}

	output := scrape(fixture.server)
	for _, want := range []string{
		failure + "1\n",
		`nexterm_auth_login_total{step="password",result="success"} 1` + "\n",
		`nexterm_http_requests_total{method="POST",route="/auth/login",code="4xx"} 1` + "\n",
		`nexterm_http_requests_total{method="POST",route="/auth/login",code="2xx"} 1` + "\n",
		`nexterm_http_request_duration_seconds_count{method="POST",route="/auth/login"} 2` + "\n",
		`nexterm_websocket_connections{kind="events"} 0` + "\n",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("metrics missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "alice") {
		t.Fatalf("metrics expose a username:\n%s", output)
	}
}

func TestMetricsRouteTemplateKeepsWildcards(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	fixture.call(t, http.MethodGet, "/ws/channel/some-secret-id", nil, nil, "", nil)
	fixture.call(t, http.MethodGet, "/no/such/route/for/user-path", nil, nil, "", nil)
	output := scrape(fixture.server)
	if !strings.Contains(output, `route="/ws/channel/{id}"`) {
		t.Fatalf("channel route not templated:\n%s", output)
	}
	if strings.Contains(output, "some-secret-id") || strings.Contains(output, "user-path") {
		t.Fatalf("metrics expose a request path:\n%s", output)
	}
}
