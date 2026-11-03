package main

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// serveGuard sends a request that carries the canonical Wails host of the platform under test.
func serveGuard(t *testing.T, method, target string, headers map[string]string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	return serveGuardHost(t, wailsHostsForGOOS(runtime.GOOS)[0], method, target, headers, nil)
}

func serveGuardHost(t *testing.T, host, method, target string, headers map[string]string, next http.HandlerFunc) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	called := false
	handler := runtimeRequestGuard(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		called = true
		if next != nil {
			next(rw, req)
			return
		}
		rw.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec, called
}

func TestWailsHostsForGOOS(t *testing.T) {
	cases := map[string][]string{
		"windows": {"wails.localhost"},
		"android": {"wails.localhost"},
		"linux":   {"localhost", "wails"},
		"darwin":  {"localhost", "wails"},
		"ios":     {"localhost", "wails"},
	}
	for goos, want := range cases {
		got := wailsHostsForGOOS(goos)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: %v want %v", goos, got, want)
		}
	}
}

func TestRuntimeRequestGuardChecksHost(t *testing.T) {
	headers := map[string]string{"x-wails-client-id": "abc", "Sec-Fetch-Site": "same-origin"}
	canonical := wailsHostsForGOOS(runtime.GOOS)[0]
	for _, host := range []string{"example.com", canonical + ".evil.com", "evil." + canonical, "", "127.0.0.1:34115"} {
		rec, called := serveGuardHost(t, host, http.MethodPost, "/wails/runtime", headers, nil)
		if rec.Code != http.StatusForbidden || called {
			t.Errorf("host %q: code = %d, next called = %v", host, rec.Code, called)
		}
	}
	// The dev server adds a port to the host; the name still has to match exactly.
	for _, host := range []string{canonical, strings.ToUpper(canonical), canonical + ":5173"} {
		rec, called := serveGuardHost(t, host, http.MethodPost, "/wails/runtime", headers, nil)
		if rec.Code != http.StatusOK || !called {
			t.Errorf("host %q: code = %d, next called = %v", host, rec.Code, called)
		}
	}
	// Other paths are not host-restricted: the app pages are served only to the webview anyway.
	rec, called := serveGuardHost(t, "example.com", http.MethodGet, "/assets/app.js", nil, nil)
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("static path: code = %d, next called = %v", rec.Code, called)
	}
}

func TestRuntimeRequestGuardRejectsFrameDestinations(t *testing.T) {
	for _, dest := range []string{"iframe", "frame"} {
		for _, path := range []string{"/", "/index.html", "/anything/page", "/wails/runtime", "/wails/stream/poll", "/wails/eventpayload/x"} {
			rec, called := serveGuard(t, http.MethodGet, path, map[string]string{
				"Sec-Fetch-Dest": dest, "x-wails-client-id": "abc", "x-wails-stream-session": "s", "Sec-Fetch-Site": "same-origin",
			})
			if rec.Code != http.StatusForbidden || called {
				t.Errorf("%s %s: code = %d, next called = %v", dest, path, rec.Code, called)
			}
		}
	}
	for _, dest := range []string{"document", "script", "empty", ""} {
		headers := map[string]string{}
		if dest != "" {
			headers["Sec-Fetch-Dest"] = dest
		}
		if rec, called := serveGuard(t, http.MethodGet, "/", headers); rec.Code != http.StatusOK || !called {
			t.Errorf("dest %q: code = %d, next called = %v", dest, rec.Code, called)
		}
	}
}

func TestRuntimeRequestGuardSetsAntiFramingHeaders(t *testing.T) {
	rec, _ := serveGuard(t, http.MethodGet, "/", nil)
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
		t.Fatalf("CSP = %q", got)
	}

	// An existing policy is extended, never replaced, whether the handler writes a body or not.
	for name, handler := range map[string]http.HandlerFunc{
		"write header": func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Content-Security-Policy", "default-src 'self'")
			rw.WriteHeader(http.StatusOK)
		},
		"write body": func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Content-Security-Policy", "default-src 'self'")
			_, _ = rw.Write([]byte("<html></html>"))
		},
		"no write": func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Content-Security-Policy", "default-src 'self'")
		},
	} {
		rec, _ := serveGuard2(t, handler)
		csp := rec.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s: CSP = %q", name, csp)
		}
		if rec.Header().Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: X-Frame-Options missing", name)
		}
	}

	// A permissive frame-ancestors from the handler must not win.
	rec, _ = serveGuard2(t, func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Security-Policy", "frame-ancestors *")
		rw.WriteHeader(http.StatusOK)
	})
	values := rec.Header().Values("Content-Security-Policy")
	if !strings.Contains(strings.Join(values, ","), "frame-ancestors 'none'") {
		t.Fatalf("CSP = %q", values)
	}

	// Rejections carry the headers too.
	rec, called := serveGuard(t, http.MethodPost, "/wails/runtime", nil)
	if called || rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("rejected response lacks headers: called=%v %v", called, rec.Header())
	}
}

func serveGuard2(t *testing.T, next http.HandlerFunc) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	return serveGuardHost(t, wailsHostsForGOOS(runtime.GOOS)[0], http.MethodGet, "/", nil, next)
}

func TestRuntimeRequestGuardRejectsMissingClientID(t *testing.T) {
	rec, called := serveGuard(t, http.MethodPost, "/wails/runtime", map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusForbidden || called {
		t.Fatalf("code = %d, next called = %v", rec.Code, called)
	}
}

func TestRuntimeRequestGuardRejectsQueryParameterCall(t *testing.T) {
	rec, called := serveGuard(t, http.MethodGet, "/wails/runtime?object=0&method=1&args=%7B%7D", nil)
	if rec.Code != http.StatusForbidden || called {
		t.Fatalf("code = %d, next called = %v", rec.Code, called)
	}
}

func TestRuntimeRequestGuardAllowsSameOrigin(t *testing.T) {
	for _, site := range []string{"", "same-origin", "none"} {
		headers := map[string]string{"x-wails-client-id": "abc"}
		if site != "" {
			headers["Sec-Fetch-Site"] = site
		}
		rec, called := serveGuard(t, http.MethodPost, "/wails/runtime", headers)
		if rec.Code != http.StatusOK || !called {
			t.Fatalf("site %q: code = %d, next called = %v", site, rec.Code, called)
		}
	}
}

func TestRuntimeRequestGuardRejectsCrossSite(t *testing.T) {
	for _, site := range []string{"cross-site", "same-site"} {
		rec, called := serveGuard(t, http.MethodPost, "/wails/runtime", map[string]string{"x-wails-client-id": "abc", "Sec-Fetch-Site": site})
		if rec.Code != http.StatusForbidden || called {
			t.Fatalf("site %q: code = %d, next called = %v", site, rec.Code, called)
		}
	}
}

func TestRuntimeRequestGuardFailsPreflight(t *testing.T) {
	for _, path := range []string{"/wails/runtime", "/wails/stream/send", "/wails/eventpayload/abc"} {
		rec, called := serveGuard(t, http.MethodOptions, path, map[string]string{
			"x-wails-client-id":              "abc",
			"Origin":                         "http://evil.example",
			"Access-Control-Request-Method":  "POST",
			"Access-Control-Request-Headers": "x-wails-client-id",
		})
		if rec.Code != http.StatusForbidden || called {
			t.Fatalf("%s: code = %d, next called = %v", path, rec.Code, called)
		}
		for name := range rec.Header() {
			if len(name) >= 14 && http.CanonicalHeaderKey(name)[:14] == "Access-Control" {
				t.Fatalf("%s: unexpected CORS header %s", path, name)
			}
		}
	}
}

func TestRuntimeRequestGuardStreamAndEventPayload(t *testing.T) {
	rec, called := serveGuard(t, http.MethodGet, "/wails/stream/poll", map[string]string{"x-wails-stream-session": "s1", "Sec-Fetch-Site": "same-origin"})
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("stream poll: code = %d, next called = %v", rec.Code, called)
	}
	rec, called = serveGuard(t, http.MethodPost, "/wails/stream/send", map[string]string{"Sec-Fetch-Site": "same-origin"})
	if rec.Code != http.StatusForbidden || called {
		t.Fatalf("stream send without session: code = %d, next called = %v", rec.Code, called)
	}
	rec, called = serveGuard(t, http.MethodGet, "/wails/stream/poll", map[string]string{"x-wails-stream-session": "s1", "Sec-Fetch-Site": "cross-site"})
	if rec.Code != http.StatusForbidden || called {
		t.Fatalf("cross-site stream: code = %d, next called = %v", rec.Code, called)
	}
	// Event payload fetches carry no custom header; only the fetch metadata is checked.
	rec, called = serveGuard(t, http.MethodGet, "/wails/eventpayload/abc", nil)
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("event payload: code = %d, next called = %v", rec.Code, called)
	}
	rec, called = serveGuard(t, http.MethodGet, "/wails/eventpayload/abc", map[string]string{"Sec-Fetch-Site": "cross-site"})
	if rec.Code != http.StatusForbidden || called {
		t.Fatalf("cross-site event payload: code = %d, next called = %v", rec.Code, called)
	}
}

func TestRuntimeRequestGuardPassesOtherPaths(t *testing.T) {
	for _, path := range []string{"/", "/index.html", "/wails/runtime.js", "/wails/transport.js", "/assets/app.js"} {
		for _, method := range []string{http.MethodGet, http.MethodOptions} {
			rec, called := serveGuard(t, method, path, map[string]string{"Sec-Fetch-Site": "cross-site"})
			if rec.Code != http.StatusOK || !called {
				t.Fatalf("%s %s: code = %d, next called = %v", method, path, rec.Code, called)
			}
		}
	}
}
