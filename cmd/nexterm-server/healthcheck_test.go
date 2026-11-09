package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthcheckExitCodes(t *testing.T) {
	respond := func(status int, body string) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/healthz" {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(server.Close)
		return server
	}
	healthy := respond(http.StatusOK, `{"ok":true,"service":"nexterm-server","version":"1.0.0"}`)
	unavailable := respond(http.StatusServiceUnavailable, `{"ok":false,"service":"nexterm-server","version":"1.0.0"}`)
	impostor := respond(http.StatusOK, `{"ok":true,"service":"captive-portal"}`)
	notJSON := respond(http.StatusOK, `<html>login</html>`)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := listener.Addr().String()
	_ = listener.Close()

	noEnv := func(string) string { return "" }
	cases := []struct {
		name   string
		args   []string
		getenv func(string) string
		want   int
	}{
		{name: "healthy", args: []string{"--url", healthy.URL + "/healthz"}, want: 0},
		{name: "healthy from listen env", getenv: func(key string) string {
			if key == "NEXTERM_LISTEN" {
				return strings.Replace(healthy.Listener.Addr().String(), "127.0.0.1", "0.0.0.0", 1)
			}
			return ""
		}, want: 0},
		{name: "unavailable", args: []string{"--url=" + unavailable.URL + "/healthz"}, want: 1},
		{name: "not nexterm", args: []string{"--url", impostor.URL + "/healthz"}, want: 1},
		{name: "not json", args: []string{"--url", notJSON.URL + "/healthz"}, want: 1},
		{name: "unreachable", args: []string{"--url", "http://" + closedAddr + "/healthz", "--timeout", "2s"}, want: 1},
		{name: "relative url", args: []string{"--url", "/healthz"}, want: 2},
		{name: "unknown flag", args: []string{"--bogus"}, want: 2},
		{name: "help", args: []string{"--help"}, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getenv := tc.getenv
			if getenv == nil {
				getenv = noEnv
			}
			var stdout, stderr bytes.Buffer
			if got := runHealthcheck(tc.args, getenv, &stdout, &stderr); got != tc.want {
				t.Fatalf("exit = %d, want %d; stdout=%q stderr=%q", got, tc.want, stdout.String(), stderr.String())
			}
		})
	}
}

func TestHealthURLForListen(t *testing.T) {
	for listen, want := range map[string]string{
		"0.0.0.0:8080":   "http://127.0.0.1:8080/healthz",
		":8080":          "http://127.0.0.1:8080/healthz",
		"[::]:8080":      "http://[::1]:8080/healthz",
		"10.0.0.5:9000":  "http://10.0.0.5:9000/healthz",
		"localhost:8080": "http://localhost:8080/healthz",
	} {
		if got, err := healthURLForListen(listen); err != nil || got != want {
			t.Fatalf("healthURLForListen(%q) = %q, %v; want %q", listen, got, err, want)
		}
	}
}
