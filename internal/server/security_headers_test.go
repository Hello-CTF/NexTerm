package server

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func scriptHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

func scriptSrc(t *testing.T, csp string) string {
	t.Helper()
	for _, directive := range strings.Split(csp, ";") {
		directive = strings.TrimSpace(directive)
		if strings.HasPrefix(directive, "script-src ") {
			return directive
		}
	}
	t.Fatalf("CSP %q has no script-src", csp)
	return ""
}

func writeIndex(t *testing.T, root, html string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(html), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	config := testConfig(t, false)
	_, httpServer := newTestHTTP(t, config)
	client := httpServer.Client()

	check := func(name string, response *http.Response) {
		t.Helper()
		response.Body.Close()
		if got := response.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s X-Content-Type-Options = %q", name, got)
		}
		if got := response.Header.Get("Referrer-Policy"); got != "same-origin" {
			t.Errorf("%s Referrer-Policy = %q", name, got)
		}
		if got := response.Header.Get("X-Frame-Options"); got != "SAMEORIGIN" {
			t.Errorf("%s X-Frame-Options = %q", name, got)
		}
	}
	response, err := client.Get(httpServer.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	check("healthz", response)
	response, err = client.Get(httpServer.URL + "/no-such-path")
	if err != nil {
		t.Fatal(err)
	}
	check("404", response)
	response, err = client.Post(httpServer.URL+"/rpc", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("rpc status = %d", response.StatusCode)
	}
	check("rpc early exit", response)
}

func TestHSTSOnlyOverTrustedTLS(t *testing.T) {
	config := testConfig(t, false)
	config.Options.TrustedProxyCIDRs = []string{"10.0.0.0/8"}
	server, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	handler := server.Handler()

	serve := func(remote, proto string, tls bool) string {
		request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		request.RemoteAddr = remote
		if proto != "" {
			request.Header.Set("X-Forwarded-Proto", proto)
		}
		if tls {
			request = httptest.NewRequest(http.MethodGet, "https://example.test/healthz", nil)
			request.RemoteAddr = remote
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Header().Get("Strict-Transport-Security")
	}
	if got := serve("203.0.113.1:1000", "", false); got != "" {
		t.Errorf("plain HTTP HSTS = %q", got)
	}
	if got := serve("203.0.113.1:1000", "", true); got != "max-age=31536000" {
		t.Errorf("TLS HSTS = %q", got)
	}
	if got := serve("10.1.2.3:1000", "https", false); got != "max-age=31536000" {
		t.Errorf("trusted proxy HSTS = %q", got)
	}
	if got := serve("203.0.113.1:1000", "https", false); got != "" {
		t.Errorf("untrusted forwarded proto HSTS = %q", got)
	}
}

func TestStaticHTMLCarriesCSPWithInlineScriptHashes(t *testing.T) {
	root := t.TempDir()
	const inline = `window.theme="dark";`
	writeIndex(t, root, `<html><head><script>`+inline+`</script><SCRIPT type="module" src="/a.js">ignored()</SCRIPT></head><body>app</body></html>`)
	if err := os.WriteFile(filepath.Join(root, "a.js"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := testConfig(t, false)
	config.Options.WebRoot = root
	_, httpServer := newTestHTTP(t, config)

	get := func(path string, gzip bool) *http.Response {
		request, _ := http.NewRequest(http.MethodGet, httpServer.URL+path, nil)
		if gzip {
			request.Header.Set("Accept-Encoding", "gzip")
		}
		response, err := httpServer.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response
	}
	src := scriptSrc(t, get("/", false).Header.Get("Content-Security-Policy"))
	marker := strings.TrimSuffix(strings.TrimPrefix(TransportMarker, "<script>"), "</script>")
	want := "script-src 'self' 'wasm-unsafe-eval' " + scriptHash(inline) + " " + scriptHash(marker)
	// Order of hashes is not part of the contract.
	for _, token := range strings.Fields(want) {
		if !strings.Contains(src, token) {
			t.Errorf("script-src %q lacks %s", src, token)
		}
	}
	if got, expected := len(strings.Fields(src)), len(strings.Fields(want)); got != expected {
		t.Errorf("script-src %q has %d sources, want %d (src= scripts must not add hashes)", src, got, expected)
	}
	if get("/a.js", false).Header.Get("Content-Security-Policy") != "" {
		t.Error("JS asset must not carry CSP")
	}
	if get("/", true).Header.Get("Content-Security-Policy") == "" {
		t.Error("gzip branch lacks CSP")
	}
	if !strings.Contains(get("/", false).Header.Get("Content-Security-Policy"), "frame-ancestors 'self'") {
		t.Error("CSP lacks frame-ancestors")
	}

	// A changed index.html is re-read and rehashed.
	const changed = `window.theme="light";`
	writeIndex(t, root, `<html><head><script>`+changed+`</script></head></html>`)
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(filepath.Join(root, "index.html"), future, future)
	src = scriptSrc(t, get("/", false).Header.Get("Content-Security-Policy"))
	if !strings.Contains(src, scriptHash(changed)) || strings.Contains(src, scriptHash(inline)) {
		t.Errorf("script-src %q not updated after the file changed", src)
	}
}

func TestInlineScriptHashesScanner(t *testing.T) {
	html := "<p>x</p><script>a()</script><script src=x></script><script\n type=\"module\">b()</script><ScRiPt>c()"
	got := inlineScriptHashes([]byte(html))
	if len(got) != 2 || got[0] != scriptHash("a()") || got[1] != scriptHash("b()") {
		t.Fatalf("hashes = %v", got)
	}
}

func TestSharePreviewHTMLCarriesCSPAndFrameProtection(t *testing.T) {
	root := t.TempDir()
	writeIndex(t, root, `<html><head></head><body>app</body></html>`)
	f := newPreviewFixture(t, func(config *Config) { config.Options.WebRoot = root })
	response, err := f.client.Get(f.http.URL + "/share/preview/any-token")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !strings.Contains(response.Header.Get("Content-Security-Policy"), "frame-ancestors 'self'") {
		t.Errorf("preview CSP = %q", response.Header.Get("Content-Security-Policy"))
	}
	if response.Header.Get("X-Frame-Options") != "SAMEORIGIN" || response.Header.Get("Referrer-Policy") != "same-origin" {
		t.Errorf("preview headers = %v", response.Header)
	}
}

func TestPlatformModeOmitsFramingHeadersAndCSP(t *testing.T) {
	root := t.TempDir()
	writeIndex(t, root, `<html><head></head><body>app</body></html>`)
	config := testConfig(t, false)
	config.Options.Auth = AuthPlatform
	config.Options.Listen = "0.0.0.0:8080"
	config.Options.WebRoot = root
	config.GatewayAuthKey = testGatewayKey
	config.Accounts = newTestAccounts(t)
	_, httpServer := newTestHTTP(t, config)
	response, err := httpServer.Client().Get(httpServer.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if response.Header.Get("Content-Security-Policy") != "" || response.Header.Get("X-Frame-Options") != "" {
		t.Errorf("platform mode must not set CSP/X-Frame-Options: %v", response.Header)
	}
	if response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("platform mode still needs nosniff")
	}
}
