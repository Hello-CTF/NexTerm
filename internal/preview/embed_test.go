package preview

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func serverWithHeaders(t *testing.T, headers map[string][]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		for k, values := range headers {
			for _, v := range values {
				rw.Header().Add(k, v)
			}
		}
		_, _ = rw.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestCheckEmbeddableHeaders(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string][]string
		want    string
	}{
		{"no headers", nil, StatusEmbeddable},
		{"xfo deny", map[string][]string{"X-Frame-Options": {"DENY"}}, StatusBlocked},
		{"xfo sameorigin lower", map[string][]string{"X-Frame-Options": {"sameorigin"}}, StatusBlocked},
		{"xfo multi value", map[string][]string{"X-Frame-Options": {"ALLOWALL", "DENY"}}, StatusBlocked},
		{"xfo comma value", map[string][]string{"X-Frame-Options": {"ALLOWALL, SAMEORIGIN"}}, StatusBlocked},
		{"xfo other", map[string][]string{"X-Frame-Options": {"ALLOWALL"}}, StatusEmbeddable},
		{"csp none", map[string][]string{"Content-Security-Policy": {"default-src 'self'; frame-ancestors 'none'"}}, StatusBlocked},
		{"csp self", map[string][]string{"Content-Security-Policy": {"frame-ancestors 'self'"}}, StatusBlocked},
		{"csp host", map[string][]string{"Content-Security-Policy": {"FRAME-ANCESTORS https://example.com"}}, StatusBlocked},
		{"csp star", map[string][]string{"Content-Security-Policy": {"frame-ancestors *"}}, StatusEmbeddable},
		{"csp second header blocks", map[string][]string{"Content-Security-Policy": {"frame-ancestors *", "frame-ancestors 'none'"}}, StatusBlocked},
		{"csp comma policies", map[string][]string{"Content-Security-Policy": {"frame-ancestors *, frame-ancestors 'self'"}}, StatusBlocked},
		{"csp without frame-ancestors", map[string][]string{"Content-Security-Policy": {"default-src 'none'; script-src 'self'"}}, StatusEmbeddable},
		{"csp report only", map[string][]string{"Content-Security-Policy-Report-Only": {"frame-ancestors 'none'"}}, StatusEmbeddable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := serverWithHeaders(t, tc.headers)
			got, err := CheckEmbeddable(context.Background(), server.Client(), server.URL)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want {
				t.Fatalf("status = %q (%s), want %q", got.Status, got.Reason, tc.want)
			}
			if got.Status == StatusBlocked && got.Reason == "" {
				t.Fatal("blocked result has no reason")
			}
		})
	}
}

func TestCheckEmbeddableFallsBackToGet(t *testing.T) {
	var gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodHead {
			rw.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		gets.Add(1)
		rw.Header().Set("X-Frame-Options", "DENY")
		_, _ = rw.Write([]byte(strings.Repeat("x", 256<<10)))
	}))
	defer server.Close()
	got, err := CheckEmbeddable(context.Background(), server.Client(), server.URL)
	if err != nil || got.Status != StatusBlocked || gets.Load() != 1 {
		t.Fatalf("got %+v err %v gets %d", got, err, gets.Load())
	}
}

func TestCheckEmbeddableRedirects(t *testing.T) {
	target := serverWithHeaders(t, map[string][]string{"X-Frame-Options": {"DENY"}})
	redirect := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		http.Redirect(rw, req, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	got, err := CheckEmbeddable(context.Background(), redirect.Client(), redirect.URL)
	if err != nil || got.Status != StatusBlocked {
		t.Fatalf("followed redirect: got %+v err %v", got, err)
	}
}

func TestCheckEmbeddableRejectsNonHTTPRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Location", "file:///etc/passwd")
		rw.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	got, err := CheckEmbeddable(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == StatusEmbeddable {
		t.Fatalf("file redirect reported embeddable: %+v", got)
	}
}

func TestCheckEmbeddableRedirectLimit(t *testing.T) {
	var hits atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		hits.Add(1)
		http.Redirect(rw, req, server.URL+"/next", http.StatusFound)
	}))
	defer server.Close()
	got, err := CheckEmbeddable(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == StatusEmbeddable {
		t.Fatalf("endless redirect reported embeddable: %+v", got)
	}
	if n := hits.Load(); n > 12 {
		t.Fatalf("too many requests: %d", n)
	}
}

func TestCheckEmbeddableRejectsSchemes(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "ftp://example.com/", "javascript:alert(1)", "", "http://", "not a url"} {
		if _, err := CheckEmbeddable(context.Background(), nil, raw); err == nil {
			t.Fatalf("%q accepted", raw)
		}
	}
}

func TestCheckEmbeddableSendsNoCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
			rw.Header().Set("X-Frame-Options", "DENY")
		}
		http.SetCookie(rw, &http.Cookie{Name: "a", Value: "b"})
	}))
	defer server.Close()
	client := server.Client()
	for i := 0; i < 2; i++ {
		got, err := CheckEmbeddable(context.Background(), client, server.URL)
		if err != nil || got.Status != StatusEmbeddable {
			t.Fatalf("round %d: got %+v err %v", i, got, err)
		}
	}
}

func TestCheckEmbeddableUnreachable(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	got, err := CheckEmbeddable(context.Background(), nil, url)
	if err != nil || got.Status != StatusUnreachable || got.Reason == "" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestCheckEmbeddableTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { <-release }))
	defer server.Close()
	defer close(release)
	start := time.Now()
	got, err := checkEmbeddable(context.Background(), server.Client(), server.URL, 50*time.Millisecond)
	if err != nil || got.Status != StatusUnreachable {
		t.Fatalf("got %+v err %v", got, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout not enforced")
	}
}

func TestRegisterCommandsDesktopOnly(t *testing.T) {
	desktop := ipc.NewDispatcher()
	if err := NewService(true).RegisterCommands(desktop); err != nil {
		t.Fatal(err)
	}
	if !hasCommand(desktop, "preview_embed_check") {
		t.Fatal("desktop service did not register preview_embed_check")
	}
	server := ipc.NewDispatcher()
	if err := NewService(false).RegisterCommands(server); err != nil {
		t.Fatal(err)
	}
	if hasCommand(server, "preview_embed_check") {
		t.Fatal("server mode registered preview_embed_check")
	}
}

func hasCommand(d *ipc.Dispatcher, name string) bool {
	for _, command := range d.Commands() {
		if command == name {
			return true
		}
	}
	return false
}
