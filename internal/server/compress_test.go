package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func gunzipBody(t *testing.T, body []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func doRequest(t *testing.T, handler http.Handler, method, target string, header map[string]string) *http.Response {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	for key, value := range header {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Result()
}

func jsonHandler(body string, status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

func TestCompressResponsesGzipsLargeJSONAndKeepsStatus(t *testing.T) {
	body := `{"items":[` + strings.Repeat(`{"name":"entry","size":1234},`, 200) + `{}]}`
	for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
		handler := compressResponses(false, jsonHandler(body, status))
		response := doRequest(t, handler, http.MethodPost, "/rpc", map[string]string{"Accept-Encoding": "gzip, br"})
		if response.StatusCode != status {
			t.Fatalf("status = %d, want %d", response.StatusCode, status)
		}
		if response.Header.Get("Content-Encoding") != "gzip" {
			t.Fatalf("Content-Encoding = %q", response.Header.Get("Content-Encoding"))
		}
		if !strings.Contains(response.Header.Get("Vary"), "Accept-Encoding") {
			t.Fatalf("Vary = %q", response.Header.Get("Vary"))
		}
		raw, _ := io.ReadAll(response.Body)
		if len(raw) >= len(body)/2 {
			t.Fatalf("compressed %d of %d bytes", len(raw), len(body))
		}
		if got := gunzipBody(t, raw); string(got) != body {
			t.Fatal("decoded body differs")
		}
	}
}

func TestCompressResponsesLeavesUnsuitableResponsesAlone(t *testing.T) {
	large := strings.Repeat("x", 4096)
	cases := []struct {
		name    string
		handler http.Handler
		header  map[string]string
	}{
		{"tiny", jsonHandler(`{"ok":true}`, 200), map[string]string{"Accept-Encoding": "gzip"}},
		{"no accept", jsonHandler(large, 200), nil},
		{"gzip refused", jsonHandler(large, 200), map[string]string{"Accept-Encoding": "gzip;q=0"}},
		{"range", jsonHandler(large, 200), map[string]string{"Accept-Encoding": "gzip", "Range": "bytes=0-10"}},
		{"png", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = io.WriteString(w, large)
		}), map[string]string{"Accept-Encoding": "gzip"}},
		{"already encoded", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "br")
			_, _ = io.WriteString(w, large)
		}), map[string]string{"Accept-Encoding": "gzip"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := doRequest(t, compressResponses(false, tc.handler), http.MethodPost, "/rpc", tc.header)
			if encoding := response.Header.Get("Content-Encoding"); encoding == "gzip" {
				t.Fatalf("response was gzipped")
			}
			body, _ := io.ReadAll(response.Body)
			if len(body) == 0 {
				t.Fatal("empty body")
			}
		})
	}
}

func TestCompressResponsesBlobSniffsText(t *testing.T) {
	binary := bytes.Repeat([]byte{0, 1, 2, 3, 0xff, 0xfe}, 1000)
	text := []byte(strings.Repeat("hello world\n", 500))
	serve := func(data []byte) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(data))
		})
	}
	handler := func(data []byte) http.Handler { return compressResponses(true, serve(data)) }
	response := doRequest(t, handler(text), http.MethodGet, "/files/blob", map[string]string{"Accept-Encoding": "gzip"})
	raw, _ := io.ReadAll(response.Body)
	if response.Header.Get("Content-Encoding") != "gzip" || !bytes.Equal(gunzipBody(t, raw), text) {
		t.Fatalf("text blob not gzipped: %q", response.Header.Get("Content-Encoding"))
	}
	response = doRequest(t, handler(binary), http.MethodGet, "/files/blob", map[string]string{"Accept-Encoding": "gzip"})
	raw, _ = io.ReadAll(response.Body)
	if response.Header.Get("Content-Encoding") != "" || !bytes.Equal(raw, binary) {
		t.Fatal("binary blob must pass through untouched")
	}
	response = doRequest(t, handler(text), http.MethodGet, "/files/blob", map[string]string{"Accept-Encoding": "gzip", "Range": "bytes=0-9"})
	raw, _ = io.ReadAll(response.Body)
	if response.StatusCode != http.StatusPartialContent || response.Header.Get("Content-Encoding") != "" || string(raw) != string(text[:10]) {
		t.Fatalf("range response altered: %d %q", response.StatusCode, raw)
	}
}

func TestCompressResponsesStreamsOnFlush(t *testing.T) {
	release := make(chan struct{})
	handler := compressResponses(false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, strings.Repeat("a", 2048))
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "tail")
	}))
	server := httptest.NewServer(handler)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	request.Header.Set("Accept-Encoding", "gzip")
	transport := &http.Transport{DisableCompression: true}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	// The first 2048 bytes arrive although the handler has not finished: the flush was not buffered away.
	first := make([]byte, 2048)
	if _, err := io.ReadFull(reader, first); err != nil {
		t.Fatalf("flushed data not delivered before handler end: %v", err)
	}
	close(release)
	rest, _ := io.ReadAll(reader)
	if string(rest) != "tail" {
		t.Fatalf("rest = %q", rest)
	}
}

func TestCompressResponsesKeepsWebSocketUpgradeWorking(t *testing.T) {
	handler := compressResponses(false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_ = conn.Write(r.Context(), websocket.MessageText, []byte("hi"))
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	server := httptest.NewServer(handler)
	defer server.Close()
	conn, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{
		HTTPClient: server.Client(),
		HTTPHeader: http.Header{"Accept-Encoding": {"gzip"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	_, data, err := conn.Read(t.Context())
	if err != nil || string(data) != "hi" {
		t.Fatalf("read = %q, %v", data, err)
	}
}

func TestStaticHandlerGzipETagAndConditionalRequests(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := strings.Repeat("console.log('nexterm');\n", 500)
	png := bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 1000)
	writeFile := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("assets/app-abc.js", []byte(script))
	writeFile("assets/logo.png", png)
	writeFile("index.html", []byte("<html><head></head><body>"+strings.Repeat("x", 2000)+"</body></html>"))
	handler := NewStaticHandler(root)
	gzipHeader := map[string]string{"Accept-Encoding": "gzip"}

	response := doRequest(t, handler, http.MethodGet, "/assets/app-abc.js", gzipHeader)
	raw, _ := io.ReadAll(response.Body)
	if response.Header.Get("Content-Encoding") != "gzip" || string(gunzipBody(t, raw)) != script {
		t.Fatal("script not served gzipped")
	}
	if !strings.Contains(response.Header.Get("Vary"), "Accept-Encoding") || !strings.Contains(response.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("headers: %v", response.Header)
	}
	etag := response.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	response = doRequest(t, handler, http.MethodGet, "/assets/app-abc.js", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": etag})
	if response.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional status = %d", response.StatusCode)
	}

	// Identity clients get the plain bytes with a different validator.
	response = doRequest(t, handler, http.MethodGet, "/assets/app-abc.js", nil)
	plain, _ := io.ReadAll(response.Body)
	if response.Header.Get("Content-Encoding") != "" || string(plain) != script || response.Header.Get("ETag") == etag {
		t.Fatalf("identity response wrong: %v", response.Header)
	}
	// Range requests bypass compression.
	response = doRequest(t, handler, http.MethodGet, "/assets/app-abc.js", map[string]string{"Accept-Encoding": "gzip", "Range": "bytes=0-4"})
	partial, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusPartialContent || response.Header.Get("Content-Encoding") != "" || string(partial) != script[:5] {
		t.Fatalf("range response wrong: %d %q", response.StatusCode, partial)
	}
	// Already compressed formats are never gzipped.
	response = doRequest(t, handler, http.MethodGet, "/assets/logo.png", gzipHeader)
	imageBody, _ := io.ReadAll(response.Body)
	if response.Header.Get("Content-Encoding") != "" || !bytes.Equal(imageBody, png) {
		t.Fatal("png must be served as is")
	}
	// index.html revalidates and carries the transport marker.
	response = doRequest(t, handler, http.MethodGet, "/", gzipHeader)
	raw, _ = io.ReadAll(response.Body)
	if response.Header.Get("Cache-Control") != "no-cache" || !strings.Contains(string(gunzipBody(t, raw)), TransportMarker) {
		t.Fatalf("index response wrong: %v", response.Header)
	}
	indexTag := response.Header.Get("ETag")
	response = doRequest(t, handler, http.MethodGet, "/some/spa/route", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": indexTag})
	if response.StatusCode != http.StatusNotModified {
		t.Fatalf("index conditional status = %d", response.StatusCode)
	}
}

func TestStaticCacheIsBounded(t *testing.T) {
	cache := newStaticCache()
	for index := 0; index < staticCacheMaxItems+50; index++ {
		key := staticKey{path: strings.Repeat("p", 1) + string(rune('a'+index%26)) + strings.Repeat("q", index)}
		cache.mu.Lock()
		cache.entries[key] = &staticEntry{}
		cache.order = append(cache.order, key)
		cache.mu.Unlock()
		cache.account(key, 1)
	}
	if len(cache.entries) > staticCacheMaxItems {
		t.Fatalf("cache holds %d entries", len(cache.entries))
	}
}
