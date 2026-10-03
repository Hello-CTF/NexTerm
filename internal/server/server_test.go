package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestRPCEnvelopesAndClientIdentity(t *testing.T) {
	_, httpServer := newTestHTTP(t, testConfig(t, false))

	status, body := postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "sync_digest", nil)
	if status != http.StatusOK || !body.OK || !strings.Contains(string(body.Data), "sync_digest") {
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

func TestSyncAdmissionTokenAndPlatform(t *testing.T) {
	_, httpServer := newTestHTTP(t, testConfig(t, false))
	url := httpServer.URL + "/sync/rpc"

	status, body := postRPC(t, httpServer.Client(), url, "sync_digest", nil)
	if status != http.StatusUnauthorized || body.OK || body.Error == nil || body.Error.Code != ipc.CodeForbidden {
		t.Fatalf("missing token = %d %+v", status, body)
	}
	status, body = postRPC(t, httpServer.Client(), url, "sync_digest", map[string]string{TokenHeader: "wrong"})
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d %+v", status, body)
	}
	status, body = postRPC(t, httpServer.Client(), url, "sync_digest", map[string]string{TokenHeader: "secret"})
	if status != http.StatusOK || !body.OK {
		t.Fatalf("valid token = %d %+v", status, body)
	}
	status, body = postRPC(t, httpServer.Client(), url, "sync_digest", map[string]string{PlatformUserHeader: ""})
	if status != http.StatusOK || !body.OK {
		t.Fatalf("platform admission = %d %+v", status, body)
	}
}

func TestSyncOnlyExactlyThreeCommandsAndTwoRoutes(t *testing.T) {
	_, httpServer := newTestHTTP(t, testConfig(t, true))
	headers := map[string]string{TokenHeader: "secret"}

	for _, command := range SyncOnlyCommands() {
		status, body := postRPC(t, httpServer.Client(), httpServer.URL+"/sync/rpc", command, headers)
		if status != http.StatusOK || !body.OK {
			t.Fatalf("%s = %d %+v", command, status, body)
		}
	}
	for _, command := range []string{"sync_push", "sync_pull", "sync_link_set", "terminal_attach", "app_info"} {
		status, body := postRPC(t, httpServer.Client(), httpServer.URL+"/sync/rpc", command, headers)
		if status != http.StatusOK || body.OK || body.Error == nil || body.Error.Code != ipc.CodeNotFound {
			t.Fatalf("restricted %s = %d %+v", command, status, body)
		}
	}

	response, err := httpServer.Client().Get(httpServer.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	var health Health
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !health.OK || !health.SyncOnly || health.Commands != 3 || health.WebRoot != nil {
		t.Fatalf("health = %+v", health)
	}

	for _, path := range []string{"/rpc", "/ws/events", "/ws/channel/test", "/files/blob", "/"} {
		request, err := http.NewRequest(http.MethodPost, httpServer.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
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

func TestSyncRPCIsRestrictedInFullMode(t *testing.T) {
	_, httpServer := newTestHTTP(t, testConfig(t, false))
	status, body := postRPC(t, httpServer.Client(), httpServer.URL+"/sync/rpc", "app_info", map[string]string{TokenHeader: "secret"})
	if status != http.StatusOK || body.OK || body.Error == nil || body.Error.Code != ipc.CodeNotFound {
		t.Fatalf("full sync response = %d %+v", status, body)
	}
	status, body = postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", "app_info", nil)
	if status != http.StatusOK || !body.OK || !strings.Contains(string(body.Data), "app_info") {
		t.Fatalf("full RPC response = %d %+v", status, body)
	}
}

func TestInjectedSyncRPCHandler(t *testing.T) {
	config := testConfig(t, true)
	config.Tokens = nil
	config.SyncRPC = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/rpc" || r.Method != http.MethodPost {
			t.Errorf("injected handler request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"data":{"source":"m20"}}`))
	})
	_, httpServer := newTestHTTP(t, config)
	status, body := postRPC(t, httpServer.Client(), httpServer.URL+"/sync/rpc", "sync_digest", nil)
	if status != http.StatusOK || !body.OK || !strings.Contains(string(body.Data), "m20") {
		t.Fatalf("injected sync response = %d %+v", status, body)
	}
}

func TestSyncOnlyDispatcherRequiresAllPeerCommands(t *testing.T) {
	dispatcher := ipc.NewDispatcher()
	if err := dispatcher.RegisterRaw("sync_digest", func(context.Context, *ipc.Call) (any, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSyncOnlyDispatcher(dispatcher); err == nil {
		t.Fatal("expected missing command error")
	}
}
