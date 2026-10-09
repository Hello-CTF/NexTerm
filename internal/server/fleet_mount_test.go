package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	fleetserver "github.com/Hello-CTF/NexTerm/internal/fleet/server"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func newTestFleet(t *testing.T, authOff bool) *fleetserver.Service {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service, err := fleetserver.New(fleetserver.Config{
		DB:       database.DB(),
		Accounts: account.New(database.DB()),
		AuthOff:  authOff,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func postJSON(t *testing.T, client *http.Client, url, body string) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

func TestFleetRoutesMounted(t *testing.T) {
	config := testConfig(t, false)
	config.Fleet = newTestFleet(t, false)
	_, httpServer := newTestHTTP(t, config)
	client := httpServer.Client()

	// agent 合同路由已挂载: 凭证无效得到 403 (而不是 404 重定向/未注册)。
	if status := postJSON(t, client, httpServer.URL+"/agent/sync", `{"device_id":"x","secret":"y"}`); status != http.StatusForbidden {
		t.Fatalf("/agent/sync status = %d, want 403", status)
	}
	if status := postJSON(t, client, httpServer.URL+"/agent/current-url", `{"device_id":"x","secret":"y","url":"https://a.example.com","reason":"failover"}`); status != http.StatusForbidden {
		t.Fatalf("/agent/current-url status = %d, want 403", status)
	}
	// 用户态 fleet 路由已挂载: 无会话得到 401。
	response, err := client.Get(httpServer.URL + "/fleet/devices")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/fleet/devices status = %d, want 401", response.StatusCode)
	}
	// 图片与 v2 同步路由保持原样: /healthz 仍公开。
	health, err := client.Get(httpServer.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200", health.StatusCode)
	}
}

func TestFleetRoutesAuthOffRejected(t *testing.T) {
	config := testConfig(t, false)
	config.Options.Auth = AuthOff
	config.Fleet = newTestFleet(t, true)
	_, httpServer := newTestHTTP(t, config)
	client := httpServer.Client()

	for _, path := range []string{"/agent/sync", "/agent/current-url"} {
		body := `{"device_id":"x","secret":"y"}`
		if strings.HasSuffix(path, "current-url") {
			body = `{"device_id":"x","secret":"y","url":"https://a.example.com","reason":"failover"}`
		}
		if status := postJSON(t, client, httpServer.URL+path, body); status != http.StatusForbidden {
			t.Fatalf("%s status = %d, want 403", path, status)
		}
	}
	response, err := client.Get(httpServer.URL + "/fleet/devices")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("/fleet/devices status = %d, want 403", response.StatusCode)
	}
}

func TestFleetRoutesAbsentInSyncOnly(t *testing.T) {
	config := testConfig(t, true)
	config.Fleet = newTestFleet(t, false)
	_, httpServer := newTestHTTP(t, config)
	client := httpServer.Client()

	// 同步模式不挂 fleet: 路由不存在 (mux 落到 404, 而不是 401/403)。
	if status := postJSON(t, client, httpServer.URL+"/agent/sync", `{"device_id":"x","secret":"y"}`); status != http.StatusNotFound {
		t.Fatalf("/agent/sync (sync-only) status = %d, want 404", status)
	}
}
