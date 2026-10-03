//go:build production

package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

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
