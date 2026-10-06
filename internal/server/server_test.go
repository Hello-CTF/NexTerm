package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestRPCEnvelopesAndClientIdentity(t *testing.T) {
	_, httpServer := newTestHTTP(t, testConfig(t, false))

	status, body := postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "app_info", nil)
	if status != http.StatusOK || !body.OK || !strings.Contains(string(body.Data), "app_info") {
		t.Fatalf("success response = %d %+v", status, body)
	}
	status, body = postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "null_result", nil)
	if status != http.StatusOK || !body.OK || string(body.Data) != "null" {
		t.Fatalf("null response = %d %+v", status, body)
	}
	status, body = postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "fail", nil)
	if status != http.StatusOK || body.OK || body.Error == nil || body.Error.Code != ipc.CodeVaultLocked {
		t.Fatalf("failure response = %d %+v", status, body)
	}
	status, body = postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "client_id", map[string]string{"X-NexTerm-Client-Id": "browser-a"})
	if status != http.StatusOK || string(body.Data) != `"browser-a"` {
		t.Fatalf("client id response = %d %+v", status, body)
	}
}

func TestSyncOnlyModeServesOnlyHealthAndAccountSurface(t *testing.T) {
	_, httpServer := newTestHTTP(t, testConfig(t, true))

	response, err := httpServer.Client().Get(httpServer.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	var health Health
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !health.OK || !health.SyncOnly || health.Commands != 0 || health.WebRoot != nil {
		t.Fatalf("health = %+v", health)
	}

	for _, path := range []string{"/rpc", "/sync/rpc", "/ws/events", "/ws/channel/test", "/files/blob", "/"} {
		request, err := http.NewRequest(http.MethodPost, httpServer.URL+path, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := httpServer.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("POST %s status = %d, want 404", path, response.StatusCode)
		}
	}
}

func TestSyncRPCRouteIsGoneInFullMode(t *testing.T) {
	_, httpServer := newTestHTTP(t, testConfig(t, false))
	response, err := httpServer.Client().Post(httpServer.URL+"/sync/rpc", "application/json", strings.NewReader(`{"cmd":"app_info","args":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("/sync/rpc status = %d, want 404", response.StatusCode)
	}
	status, body := postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "app_info", nil)
	if status != http.StatusOK || !body.OK || !strings.Contains(string(body.Data), "app_info") {
		t.Fatalf("full RPC response = %d %+v", status, body)
	}
}
