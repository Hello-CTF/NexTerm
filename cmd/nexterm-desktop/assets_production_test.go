//go:build production

package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

// The embedded handler must answer nested asset requests with the asset
// itself, not the SPA index fallback. On Windows the lookup once received
// native-separator paths, which embed.FS rejects, so every /assets/*.js
// request silently returned index.html; WebView2 blocked the module scripts
// and the desktop smoke timed out without a verdict (M68).
func TestProductionEmbeddedServesNestedAssetsAsFiles(t *testing.T) {
	files := fstest.MapFS{
		"index.html":                    &fstest.MapFile{Data: []byte(`<!doctype html><html><head></head><body><div id="root"></div><script type="module" src="/assets/index-abc123.js"></script></body></html>`)},
		"assets/index-abc123.js":        &fstest.MapFile{Data: []byte(`export const nested = "js-module-payload";`)},
		"assets/nested/chunk-def456.js": &fstest.MapFile{Data: []byte(`export const nested = "deep-chunk-payload";`)},
		"icon.png":                      &fstest.MapFile{Data: []byte("\x89PNG\r\n\x1a\n")},
	}
	handler, err := newEmbeddedDesktopAssets(files)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path    string
		payload string
	}{
		{path: "/assets/index-abc123.js", payload: "js-module-payload"},
		{path: "/assets/nested/chunk-def456.js", payload: "deep-chunk-payload"},
	} {
		response := productionAssetRequest(t, handler, http.MethodGet, test.path, "")
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", test.path, response.Code)
		}
		body := response.Body.String()
		if !strings.Contains(body, test.payload) {
			t.Fatalf("%s body = %q, want the asset payload (index fallback?)", test.path, body)
		}
		if strings.Contains(body, `<div id="root">`) {
			t.Fatalf("%s served the SPA index fallback: %q", test.path, body)
		}
		if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, "javascript") {
			t.Fatalf("%s Content-Type = %q, want a JavaScript MIME type", test.path, contentType)
		}
		if cacheControl := response.Header().Get("Cache-Control"); !strings.Contains(cacheControl, "immutable") {
			t.Fatalf("%s Cache-Control = %q", test.path, cacheControl)
		}
		if response.Header().Get("ETag") == "" {
			t.Fatalf("%s ETag is missing", test.path)
		}
	}
	// A single-segment asset serves as a file; an unknown path falls back to
	// the SPA index.
	icon := productionAssetRequest(t, handler, http.MethodGet, "/icon.png", "")
	if icon.Code != http.StatusOK || !strings.Contains(icon.Header().Get("Content-Type"), "image/png") {
		t.Fatalf("single-segment asset: status %d, Content-Type %q, body %q", icon.Code, icon.Header().Get("Content-Type"), icon.Body.String())
	}
	missing := productionAssetRequest(t, handler, http.MethodGet, "/assets/missing-123.js", "")
	if missing.Code != http.StatusOK || missing.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("unknown asset should fall back to the SPA index, got status %d, Content-Type %q", missing.Code, missing.Header().Get("Content-Type"))
	}
	if !strings.Contains(missing.Body.String(), desktopTransportMarker) {
		t.Fatalf("SPA fallback must serve the injected index, got %q", missing.Body.String())
	}
}

func TestProductionEmbeddedReactDist(t *testing.T) {
	handler, err := newDesktopAssets("")
	if err != nil {
		t.Fatal(err)
	}
	var modulePath string
	for _, path := range []string{"/", "/workspace/session-1"} {
		request := httptest.NewRequest(http.MethodGet, "http://wails.local"+path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.Code)
		}
		body := response.Body.String()
		if !strings.Contains(body, `<div id="root"></div>`) || !strings.Contains(body, "/assets/index-") || !strings.Contains(body, desktopTransportMarker) {
			t.Fatalf("%s embedded index = %s", path, body)
		}
		if strings.Contains(body, `/wails/runtime.js`) {
			t.Fatalf("%s contains duplicate runtime: %s", path, body)
		}
		if modulePath == "" {
			modulePath = regexp.MustCompile(`src="([^"]+\.js)"`).FindStringSubmatch(body)[1]
		}
	}
	request := httptest.NewRequest(http.MethodGet, "http://wails.local"+modulePath, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.Len() == 0 {
		t.Fatalf("embedded module %s status = %d, bytes = %d", modulePath, response.Code, response.Body.Len())
	}
	if got := response.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("module Cache-Control = %q", got)
	}
}

func TestProductionEmbeddedETagRevalidationAndHead(t *testing.T) {
	handler, err := newDesktopAssets("")
	if err != nil {
		t.Fatal(err)
	}
	modulePath := productionModulePath(t, handler)
	var indexETag, moduleETag string
	for _, test := range []struct {
		name          string
		path          string
		expectedCache string
		destination   *string
	}{
		{name: "index", path: "/", expectedCache: "no-cache", destination: &indexETag},
		{name: "hashed module", path: modulePath, expectedCache: "public, max-age=31536000, immutable", destination: &moduleETag},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := productionAssetRequest(t, handler, http.MethodGet, test.path, "")
			if response.Code != http.StatusOK || response.Body.Len() == 0 {
				t.Fatalf("initial status = %d, bytes = %d", response.Code, response.Body.Len())
			}
			etag := response.Header().Get("ETag")
			if etag == "" || response.Header().Get("Cache-Control") != test.expectedCache {
				t.Fatalf("headers = ETag %q, Cache-Control %q", etag, response.Header().Get("Cache-Control"))
			}
			*test.destination = etag
			head := productionAssetRequest(t, handler, http.MethodHead, test.path, "")
			if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("ETag") != etag || head.Header().Get("Cache-Control") != test.expectedCache {
				t.Fatalf("HEAD status = %d, bytes = %d, headers = %+v", head.Code, head.Body.Len(), head.Header())
			}
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				notModified := productionAssetRequest(t, handler, method, test.path, etag)
				if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 || notModified.Header().Get("ETag") != etag {
					t.Fatalf("%s 304 status = %d, bytes = %d, headers = %+v", method, notModified.Code, notModified.Body.Len(), notModified.Header())
				}
			}
			list := productionAssetRequest(t, handler, http.MethodGet, test.path, `"other-etag", `+etag)
			if list.Code != http.StatusNotModified || list.Body.Len() != 0 {
				t.Fatalf("ETag list status = %d, bytes = %d", list.Code, list.Body.Len())
			}
			wildcard := productionAssetRequest(t, handler, http.MethodGet, test.path, "*")
			if wildcard.Code != http.StatusNotModified || wildcard.Body.Len() != 0 {
				t.Fatalf("wildcard status = %d, bytes = %d", wildcard.Code, wildcard.Body.Len())
			}
			mismatch := productionAssetRequest(t, handler, http.MethodGet, test.path, `W/"different"`)
			if mismatch.Code != http.StatusOK || mismatch.Body.Len() == 0 {
				t.Fatalf("mismatch status = %d, bytes = %d", mismatch.Code, mismatch.Body.Len())
			}
		})
	}
	if indexETag == moduleETag {
		t.Fatal("index and hashed module share an ETag")
	}
	if desktopContentETag([]byte("before")) == desktopContentETag([]byte("after")) {
		t.Fatal("content change did not change ETag")
	}
}

func TestProductionEmbeddedErrorsAndSelfContainment(t *testing.T) {
	t.Chdir(t.TempDir())
	handler, err := newDesktopAssets("")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method string
		path   string
		status int
	}{
		{method: http.MethodPost, path: "/", status: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: "/../secret", status: http.StatusBadRequest},
		{method: http.MethodGet, path: "/%2e%2e/secret", status: http.StatusBadRequest},
		{method: http.MethodGet, path: "/a%5cb", status: http.StatusBadRequest},
	} {
		response := productionAssetRequest(t, handler, test.method, test.path, "")
		if response.Code != test.status {
			t.Fatalf("%s %s status = %d, want %d", test.method, test.path, response.Code, test.status)
		}
	}
}

func productionModulePath(t *testing.T, handler http.Handler) string {
	t.Helper()
	response := productionAssetRequest(t, handler, http.MethodGet, "/", "")
	if response.Code != http.StatusOK {
		t.Fatalf("index status = %d", response.Code)
	}
	matches := regexp.MustCompile(`src="([^"]+\.js)"`).FindStringSubmatch(response.Body.String())
	if len(matches) != 2 {
		t.Fatalf("module script not found in %s", response.Body.String())
	}
	return matches[1]
}

func productionAssetRequest(t *testing.T, handler http.Handler, method, path, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://wails.local"+path, nil)
	if ifNoneMatch != "" {
		request.Header.Set("If-None-Match", ifNoneMatch)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
