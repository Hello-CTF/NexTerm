package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

type healthProbe struct {
	status int
	body   map[string]any
}

func probeHealthz(t *testing.T, handler http.Handler, path, remoteAddr string, mutate func(*http.Request)) healthProbe {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil)
	request.RemoteAddr = remoteAddr
	if mutate != nil {
		mutate(request)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v (%q)", path, err, recorder.Body.String())
	}
	return healthProbe{status: recorder.Code, body: body}
}

func sortedKeys(body map[string]any) string {
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func healthDBConfig(t *testing.T) (Config, *store.Store) {
	t.Helper()
	db, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	config := testConfig(t, false)
	config.DB = db
	return config, db
}

func newHealthServer(t *testing.T, config Config) *Server {
	t.Helper()
	server, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func TestHealthzStatusFollowsReadiness(t *testing.T) {
	locked := func(context.Context) (any, error) { return vault.Status{Initialized: true, Unlocked: false}, nil }
	unlocked := func(context.Context) (any, error) { return vault.Status{Initialized: true, Unlocked: true}, nil }
	cases := []struct {
		name         string
		requireVault bool
		vaultStatus  func(context.Context) (any, error)
		closeDB      bool
		want         int
	}{
		{name: "healthy", vaultStatus: unlocked, want: http.StatusOK},
		{name: "locked vault not required", vaultStatus: locked, want: http.StatusOK},
		{name: "required vault unlocked", requireVault: true, vaultStatus: unlocked, want: http.StatusOK},
		{name: "required vault locked", requireVault: true, vaultStatus: locked, want: http.StatusServiceUnavailable},
		{name: "required vault without status", requireVault: true, want: http.StatusServiceUnavailable},
		{name: "database down", vaultStatus: unlocked, closeDB: true, want: http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config, db := healthDBConfig(t)
			config.Options.RequireVault = tc.requireVault
			config.VaultStatus = tc.vaultStatus
			server := newHealthServer(t, config)
			if tc.closeDB {
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			for _, remote := range []string{"127.0.0.1:5000", "203.0.113.7:5000"} {
				got := probeHealthz(t, server.Handler(), "/healthz", remote, nil)
				if got.status != tc.want || got.body["ok"] != (tc.want == http.StatusOK) || got.body["service"] != "nexterm-server" {
					t.Fatalf("%s /healthz = %d %v, want %d", remote, got.status, got.body, tc.want)
				}
			}
			live := probeHealthz(t, server.Handler(), "/livez", "203.0.113.7:5000", nil)
			if live.status != http.StatusOK || live.body["ok"] != true || live.body["service"] != "nexterm-server" {
				t.Fatalf("/livez = %d %v", live.status, live.body)
			}
		})
	}
}

func TestHealthzDetailOnlyForLoopbackCallers(t *testing.T) {
	config, _ := healthDBConfig(t)
	config.Options.WebRoot = "/srv/nexterm/web"
	config.Options.TrustedProxyCIDRs = []string{"10.0.0.0/8"}
	config.Options.TrustForwardedFor = true
	config.VaultStatus = func(context.Context) (any, error) { return vault.Status{Unlocked: true}, nil }
	server := newHealthServer(t, config)
	setHeader := func(name, value string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set(name, value) }
	}
	cases := []struct {
		name     string
		remote   string
		mutate   func(*http.Request)
		detailed bool
	}{
		{name: "direct loopback", remote: "127.0.0.1:5000", detailed: true},
		{name: "direct ipv6 loopback", remote: "[::1]:5000", detailed: true},
		{name: "remote caller", remote: "203.0.113.7:5000"},
		{name: "local proxy relaying a remote caller", remote: "127.0.0.1:5000", mutate: setHeader("X-Forwarded-For", "203.0.113.7")},
		{name: "local proxy with X-Real-IP", remote: "127.0.0.1:5000", mutate: setHeader("X-Real-IP", "203.0.113.7")},
		{name: "trusted proxy relaying loopback", remote: "10.0.0.2:5000", mutate: setHeader("X-Forwarded-For", "127.0.0.1"), detailed: true},
		{name: "trusted proxy relaying a remote caller", remote: "10.0.0.2:5000", mutate: setHeader("X-Forwarded-For", "203.0.113.7")},
		{name: "untrusted peer forging loopback", remote: "203.0.113.7:5000", mutate: setHeader("X-Forwarded-For", "127.0.0.1")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := probeHealthz(t, server.Handler(), "/healthz", tc.remote, tc.mutate)
			if got.status != http.StatusOK || got.body["version"] != "test-version" {
				t.Fatalf("/healthz = %d %v", got.status, got.body)
			}
			if !tc.detailed {
				if keys := sortedKeys(got.body); keys != "ok,service,version" {
					t.Fatalf("anonymous payload keys = %s, want only ok,service,version", keys)
				}
				return
			}
			if got.body["webRoot"] != "/srv/nexterm/web" || got.body["db"] == nil || got.body["vault"] == nil {
				t.Fatalf("loopback payload lacks detail: %v", got.body)
			}
		})
	}
}

func TestHealthzSuperadminProbeDoesNotSlideSession(t *testing.T) {
	fixture := newAccountFixtureListen(t, AuthOn, "0.0.0.0:8080")
	admin, _ := fixture.initSuperadmin(t, "root", "password-health-3")
	// Age the session past the slide-persist window so a sliding validation would rewrite it.
	if _, err := fixture.db.Exec(`UPDATE user_session SET touched_at = touched_at - 3600000, expires_at = expires_at - 3600000`); err != nil {
		t.Fatal(err)
	}
	readSession := func() (int64, int64) {
		t.Helper()
		var touched, expires int64
		if err := fixture.db.QueryRow(`SELECT touched_at, expires_at FROM user_session`).Scan(&touched, &expires); err != nil {
			t.Fatal(err)
		}
		return touched, expires
	}
	touchedBefore, expiresBefore := readSession()
	got := probeHealthz(t, fixture.server.Handler(), "/healthz", "203.0.113.7:5000", func(r *http.Request) { r.AddCookie(admin.cookie) })
	if got.status != http.StatusOK || got.body["commands"] == nil {
		t.Fatalf("superadmin /healthz = %d %v", got.status, got.body)
	}
	if touchedAfter, expiresAfter := readSession(); touchedAfter != touchedBefore || expiresAfter != expiresBefore {
		t.Fatalf("session slid: touched %d -> %d, expires %d -> %d", touchedBefore, touchedAfter, expiresBefore, expiresAfter)
	}
}

func TestHealthzDetailForSuperadminSession(t *testing.T) {
	fixture := newAccountFixtureListen(t, AuthOn, "0.0.0.0:8080")
	admin, _ := fixture.initSuperadmin(t, "root", "password-health-1")
	fixture.createUser(t, "member", "password-health-2")
	member := fixture.login(t, "member", "password-health-2")
	withCookie := func(session *accountTestSession) func(*http.Request) {
		return func(r *http.Request) { r.AddCookie(session.cookie) }
	}

	got := probeHealthz(t, fixture.server.Handler(), "/healthz", "203.0.113.7:5000", withCookie(admin))
	if got.status != http.StatusOK || got.body["commands"] == nil || got.body["liveChannels"] == nil {
		t.Fatalf("superadmin /healthz = %d %v", got.status, got.body)
	}
	for name, mutate := range map[string]func(*http.Request){
		"member":    withCookie(member),
		"anonymous": nil,
		"bogus":     func(r *http.Request) { r.AddCookie(&http.Cookie{Name: admin.cookie.Name, Value: "not-a-session"}) },
	} {
		got := probeHealthz(t, fixture.server.Handler(), "/healthz", "203.0.113.7:5000", mutate)
		if keys := sortedKeys(got.body); got.status != http.StatusOK || keys != "ok,service,version" {
			t.Fatalf("%s /healthz = %d keys %s", name, got.status, keys)
		}
	}
}
