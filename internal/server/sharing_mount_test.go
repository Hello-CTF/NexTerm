package server

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// Share routes are mounted on a real server via fleet.Mount: management routes require a session, and the removed
// legacy /share/public/{token} route answers 410 with no-store for plain GETs (including paths shaped like real static
// files or encoded dot-segments) and WS upgrades alike, never falling back to the SPA index.
func TestSharingRoutesMounted(t *testing.T) {
	root := t.TempDir()
	index := "<html><head><title>NexTerm</title></head><body>app</body></html>"
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}
	// A real static file shaped like a token path: a plain GET must still get the index, not the file.
	if err := os.MkdirAll(filepath.Join(root, "share", "public"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "share", "public", "special"), []byte("not-the-spa"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := testConfig(t, false)
	config.Options.WebRoot = root
	config.Fleet = newTestFleet(t, false)
	_, httpServer := newTestHTTP(t, config)
	client := httpServer.Client()

	indexResponse, err := client.Get(httpServer.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	indexBody, err := io.ReadAll(indexResponse.Body)
	indexResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if indexResponse.StatusCode != http.StatusOK || !strings.Contains(string(indexBody), TransportMarker) {
		t.Fatalf("GET / = %d %q", indexResponse.StatusCode, indexBody)
	}

	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	plainGets := []struct {
		path   string
		client *http.Client
	}{
		{"/share/public/invalid-token", client},
		{"/share/public/special", client},
		{"/share/public/%2E%2E", &noRedirect},
	}
	for _, plainGet := range plainGets {
		shareResponse, err := plainGet.client.Get(httpServer.URL + plainGet.path)
		if err != nil {
			t.Fatal(err)
		}
		shareBody, err := io.ReadAll(shareResponse.Body)
		shareResponse.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if shareResponse.StatusCode != http.StatusGone || string(shareBody) == string(indexBody) {
			t.Fatalf("GET %s = %d, want 410 without the SPA body", plainGet.path, shareResponse.StatusCode)
		}
		if shareResponse.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s Cache-Control = %q, want no-store", plainGet.path, shareResponse.Header.Get("Cache-Control"))
		}
	}

	// The WS upgrade is refused as well.
	conn, handshake, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/share/public/invalid-token", nil)
	if err == nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		t.Fatal("legacy public WS handshake succeeded, want 410")
	}
	if handshake == nil || handshake.StatusCode != http.StatusGone {
		t.Fatalf("legacy public WS handshake = %v, want 410", handshake)
	}

	response, err := client.Get(httpServer.URL + "/share/host-shares")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/share/host-shares status = %d, want 401", response.StatusCode)
	}
}

func TestSharingRoutesAbsentInSyncOnly(t *testing.T) {
	config := testConfig(t, true)
	config.Fleet = newTestFleet(t, false)
	_, httpServer := newTestHTTP(t, config)
	client := httpServer.Client()

	// Sync mode does not mount fleet/sharing: the routes do not exist (404, not 401/403).
	for _, path := range []string{"/share/public/x", "/share/host-shares"} {
		response, err := client.Get(httpServer.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s (sync-only) status = %d, want 404", path, response.StatusCode)
		}
	}
}
