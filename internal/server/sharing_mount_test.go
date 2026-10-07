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

// 分享路由经 fleet.Mount 挂上真实 server: 管理路由要会话; 公开 token URL 的
// 普通 GET 复用配置的静态入口服务既有 SPA (不校验 token, 响应 no-store),
// WS upgrade 仍走 token 把关 (无效 token 403 而不是 101)。
func TestSharingRoutesMounted(t *testing.T) {
	root := t.TempDir()
	index := "<html><head><title>NexTerm</title></head><body>app</body></html>"
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}
	config := testConfig(t, false)
	config.Options.WebRoot = root
	config.Fleet = newTestFleet(t, false)
	_, httpServer := newTestHTTP(t, config)
	client := httpServer.Client()

	response, err := client.Get(httpServer.URL + "/share/links")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/share/links status = %d, want 401", response.StatusCode)
	}

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

	// 普通 GET /share/public/{token} 服务既有 SPA/index (与 "/" 同一响应体),
	// 不做 token 校验, 响应 no-store。
	shareResponse, err := client.Get(httpServer.URL + "/share/public/invalid-token")
	if err != nil {
		t.Fatal(err)
	}
	shareBody, err := io.ReadAll(shareResponse.Body)
	shareResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if shareResponse.StatusCode != http.StatusOK || string(shareBody) != string(indexBody) {
		t.Fatalf("GET /share/public/{token} = %d, body differs from / SPA", shareResponse.StatusCode)
	}
	if shareResponse.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("GET /share/public/{token} Cache-Control = %q, want no-store", shareResponse.Header.Get("Cache-Control"))
	}

	// WS upgrade 仍走 token 把关: 无效 token 403 而不是 101。
	conn, handshake, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/share/public/invalid-token", nil)
	if err == nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		t.Fatal("invalid token WS handshake succeeded, want 403")
	}
	if handshake == nil || handshake.StatusCode != http.StatusForbidden {
		t.Fatalf("invalid token WS handshake = %v, want 403", handshake)
	}

	response, err = client.Get(httpServer.URL + "/share/host-shares")
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

	// 同步模式不挂 fleet/分享: 路由不存在 (404, 而不是 401/403)。
	for _, path := range []string{"/share/links", "/share/public/x", "/share/host-shares"} {
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
