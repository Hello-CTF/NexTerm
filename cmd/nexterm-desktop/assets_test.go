//go:build !production

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testDesktopAssets(t *testing.T) http.Handler {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	index := `<!doctype html><html><head><meta charset="UTF-8"><script type="module" crossorigin src="/assets/index-production123.js"></script></head><body><div id="root"></div></body></html>`
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "index-production123.js"), []byte(`export const transport = window.__NEXTERM_TRANSPORT__;`), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := newDesktopAssets(root)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestDesktopAssetsServeProductionReactAndSPA(t *testing.T) {
	handler := testDesktopAssets(t)
	for _, path := range []string{"/", "/workspace/session-1"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://wails.local"+path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			body := response.Body.String()
			if !strings.Contains(body, `/assets/index-production123.js`) || !strings.Contains(body, desktopTransportMarker) {
				t.Fatalf("production index = %s", body)
			}
			if strings.Contains(body, "/wails/runtime.js") || strings.Contains(body, "Go application core is running") {
				t.Fatalf("skeleton or duplicate runtime leaked into index: %s", body)
			}
			if strings.Index(body, desktopTransportMarker) > strings.Index(body, `/assets/index-production123.js`) {
				t.Fatalf("desktop marker was not installed before module execution: %s", body)
			}
			if got := response.Header().Get("Cache-Control"); got != "no-cache" {
				t.Fatalf("index Cache-Control = %q", got)
			}
		})
	}
}

func TestDesktopAssetsServeHashedFilesAndHead(t *testing.T) {
	handler := testDesktopAssets(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		request := httptest.NewRequest(method, "http://wails.local/assets/index-production123.js", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", method, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
			t.Fatalf("%s Content-Type = %q", method, got)
		}
		if got := response.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
			t.Fatalf("%s Cache-Control = %q", method, got)
		}
		if method == http.MethodHead && response.Body.Len() != 0 {
			t.Fatalf("HEAD body = %q", response.Body.String())
		}
		if method == http.MethodGet && !strings.Contains(response.Body.String(), "window.__NEXTERM_TRANSPORT__") {
			t.Fatalf("asset body = %q", response.Body.String())
		}
	}
}

func TestDesktopAssetsRejectMissingRootMethodsAndTraversal(t *testing.T) {
	if _, err := newDesktopAssets(t.TempDir()); err == nil {
		t.Fatal("expected missing production index error")
	}
	handler := testDesktopAssets(t)
	for _, test := range []struct {
		method string
		path   string
		status int
	}{
		{method: http.MethodPost, path: "/", status: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: "/../secret", status: http.StatusBadRequest},
		{method: http.MethodGet, path: "/%2e%2e/secret", status: http.StatusBadRequest},
		{method: http.MethodGet, path: "/a/../../secret", status: http.StatusBadRequest},
		{method: http.MethodGet, path: "/a%5cb", status: http.StatusBadRequest},
	} {
		request := httptest.NewRequest(test.method, "http://wails.local"+test.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("%s %s status = %d, want %d", test.method, test.path, response.Code, test.status)
		}
	}
}

func TestDesktopAssetsBuiltReactDist(t *testing.T) {
	root := filepath.Join("..", "..", "dist")
	if _, err := os.Stat(filepath.Join(root, "index.html")); err != nil {
		t.Skip("production dist is not built")
	}
	handler, err := newDesktopAssets(root)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://wails.local/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("production dist status = %d", response.Code)
	}
	body := response.Body.String()
	if !strings.Contains(body, `<div id="root"></div>`) || !strings.Contains(body, "/assets/index-") || !strings.Contains(body, desktopTransportMarker) {
		t.Fatalf("production dist index = %s", body)
	}
	if strings.Contains(body, `/wails/runtime.js`) {
		t.Fatalf("duplicate Wails runtime in production dist index = %s", body)
	}
}
