package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func serveGuard(t *testing.T, method, target string, headers map[string]string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	called := false
	handler := runtimeRequestGuard(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		called = true
		rw.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec, called
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
