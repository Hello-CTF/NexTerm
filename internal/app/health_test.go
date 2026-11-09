package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/vault"
)

func TestServeHealthReadinessAndRedaction(t *testing.T) {
	locked := false
	application, err := New(Config{Version: "test-version", VaultStatus: func(context.Context) (any, error) {
		return vault.Status{Initialized: true, Unlocked: !locked}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	probe := func(handler http.HandlerFunc, remote string, headers map[string]string) (int, map[string]any) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		request.RemoteAddr = remote
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		recorder := httptest.NewRecorder()
		handler(recorder, request)
		var body map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return recorder.Code, body
	}
	required := application.serveHealth(ServeConfig{SyncOnly: true, RequireVault: true})

	status, body := probe(required, "127.0.0.1:4000", nil)
	if status != http.StatusOK || body["ok"] != true || body["vault"] == nil {
		t.Fatalf("loopback healthy = %d %v", status, body)
	}
	for _, remote := range []struct {
		addr    string
		headers map[string]string
	}{{addr: "203.0.113.7:4000"}, {addr: "127.0.0.1:4000", headers: map[string]string{"X-Forwarded-For": "203.0.113.7"}}} {
		status, body = probe(required, remote.addr, remote.headers)
		if status != http.StatusOK || len(body) != 3 || body["ok"] != true || body["version"] != "test-version" {
			t.Fatalf("anonymous healthy %s = %d %v", remote.addr, status, body)
		}
	}

	locked = true
	if status, body = probe(required, "127.0.0.1:4000", nil); status != http.StatusServiceUnavailable || body["ok"] != false {
		t.Fatalf("required locked vault = %d %v", status, body)
	}
	if status, body = probe(application.serveHealth(ServeConfig{SyncOnly: true}), "127.0.0.1:4000", nil); status != http.StatusOK || body["ok"] != true {
		t.Fatalf("optional locked vault = %d %v", status, body)
	}
	recorder := httptest.NewRecorder()
	application.serveLive(recorder, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("/livez = %d", recorder.Code)
	}
}
