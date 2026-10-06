package server

import (
	"net/http"
	"testing"
)

// 分享路由经 fleet.Mount 挂上真实 server: 管理路由要会话, 公开数据面匿名
// 可达 (token 把关, 无效 token 得到 403 而不是 404/重定向)。
func TestSharingRoutesMounted(t *testing.T) {
	config := testConfig(t, false)
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

	response, err = client.Get(httpServer.URL + "/share/public/invalid-token")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("/share/public/{token} status = %d, want 403", response.StatusCode)
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
