package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestStaticSPAMarkerCacheAndHead(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	index := "<html><head><title>NexTerm</title></head><body>app</body></html>"
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}
	assetName := "app-123456789012345678901234.js"
	if err := os.WriteFile(filepath.Join(root, "assets", assetName), []byte("console.log('ok')"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := testConfig(t, false)
	config.Options.WebRoot = root
	_, httpServer := newTestHTTP(t, config)

	for _, path := range []string{"/", "/sessions/session-1"} {
		response, err := httpServer.Client().Get(httpServer.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if response.StatusCode != http.StatusOK || !strings.Contains(text, TransportMarker) || strings.Index(text, TransportMarker) > strings.Index(text, "</head>") {
			t.Fatalf("GET %s = %d %s", path, response.StatusCode, text)
		}
		if response.Header.Get("Cache-Control") != "no-cache" || response.Header.Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatalf("GET %s headers = %+v", path, response.Header)
		}
	}

	response, err := httpServer.Client().Get(httpServer.URL + "/assets/" + assetName)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/javascript") {
		t.Fatalf("asset response = %d %+v", response.StatusCode, response.Header)
	}

	request, err := http.NewRequest(http.MethodHead, httpServer.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || len(body) != 0 {
		t.Fatalf("HEAD body = %q, %v", body, err)
	}
	if length, _ := strconv.Atoi(response.Header.Get("Content-Length")); length != len(InjectTransportMarker(index)) {
		t.Fatalf("HEAD content length = %s", response.Header.Get("Content-Length"))
	}
}

func TestStaticRejectsTraversalAndMissingIndex(t *testing.T) {
	root := t.TempDir()
	handler := NewStaticHandler(root)
	for _, path := range []string{"/../secret", "/%2e%2e/secret", "/a/../../secret", "/a%5cb"} {
		request := httptest.NewRequest(http.MethodGet, "http://example.test"+path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400", path, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing index status = %d", response.Code)
	}
}

func TestStaticPathAllowsNormalAssets(t *testing.T) {
	path, err := safeStaticPath("/assets/index.js")
	if err != nil || path != filepath.Join("assets", "index.js") {
		t.Fatalf("path = %q, %v", path, err)
	}
}
