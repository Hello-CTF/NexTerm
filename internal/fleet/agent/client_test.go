package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthAcceptsGenuineNexTermPayload(t *testing.T) {
	var sawAuthorization, sawCookie bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuthorization = r.Header.Get("Authorization") != ""
		_, cookieErr := r.Cookie("nexterm_session")
		sawCookie = cookieErr == nil
		if r.URL.Path != PathHealth {
			t.Errorf("probe path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "service": "nexterm-server", "version": "dev"})
	}))
	defer server.Close()
	client, err := NewEndpointClient(BaseURLEntry{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Health(ctx); err != nil {
		t.Fatal(err)
	}
	if sawAuthorization || sawCookie {
		t.Fatal("health probe must not carry credentials")
	}
}

func TestHealthRejectsFakeResponses(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"plain 200 HTML": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("<html>captive portal</html>"))
		},
		"ok false": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "service": "nexterm-server"})
		},
		"wrong service": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "service": "something-else"})
		},
		"status 500": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
		"redirect elsewhere": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://example.com/", http.StatusFound)
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(handler)
			defer server.Close()
			client, err := NewEndpointClient(BaseURLEntry{URL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := client.Health(ctx); err == nil {
				t.Fatal("fake health response must be rejected")
			}
		})
	}
}

func TestEnrollSyncAndCurrentURL(t *testing.T) {
	type syncCall struct {
		DeviceID string  `json:"device_id"`
		Sample   *Sample `json:"sample"`
	}
	var gotSync syncCall
	var gotCurrentURL struct {
		URL    string `json:"url"`
		Reason string `json:"reason"`
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		var request EnrollRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Code != "enroll-code" || request.Name != "dev-box" || request.Platform == "" {
			t.Errorf("enroll request = %+v", request)
		}
		_ = json.NewEncoder(w).Encode(EnrollResponse{
			DeviceID: "01J5DEVICE0000000000000000", Secret: "device-secret",
			BaseURLs:          []BaseURLEntry{{URL: "https://fleet.example.com"}},
			MetricsIntervalMS: 60000, DesiredAutostart: true, TerminalEnabled: true,
		})
	})
	mux.HandleFunc("POST "+PathSync, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotSync); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(SyncResponse{DesiredAutostart: false, MetricsIntervalMS: 30000, TerminalEnabled: true})
	})
	mux.HandleFunc("POST "+PathCurrentURL, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotCurrentURL); err != nil {
			t.Error(err)
		}
		w.Write([]byte(`{"ok":true}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client, err := NewEndpointClient(BaseURLEntry{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	enrolled, err := client.Enroll(ctx, EnrollRequest{Code: "enroll-code", Name: "dev-box", Platform: "linux", AppVersion: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if enrolled.DeviceID == "" || enrolled.Secret != "device-secret" || len(enrolled.BaseURLs) != 1 {
		t.Fatalf("enroll response = %+v", enrolled)
	}

	response, err := client.Sync(ctx, SyncRequest{
		DeviceID: enrolled.DeviceID, Secret: enrolled.Secret,
		Sample:       &Sample{TS: 1700000000000, CPUPct: 12.5, MemUsed: 1, MemTotal: 2, DiskUsed: 3, DiskTotal: 4, UptimeS: 5},
		ServiceState: &ServiceState{Installed: true, Enabled: true, Active: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.DesiredAutostart || response.MetricsIntervalMS != 30000 {
		t.Fatalf("sync response = %+v", response)
	}
	if gotSync.DeviceID != enrolled.DeviceID || gotSync.Sample == nil || gotSync.Sample.CPUPct != 12.5 {
		t.Fatalf("sync request = %+v", gotSync)
	}

	if err := client.ReportCurrentURL(ctx, enrolled.DeviceID, enrolled.Secret, "https://second.example.com", "failover"); err != nil {
		t.Fatal(err)
	}
	if gotCurrentURL.URL != "https://second.example.com" || gotCurrentURL.Reason != "failover" {
		t.Fatalf("current-url request = %+v", gotCurrentURL)
	}
}

func TestSyncMapsServerFailureShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"ok":false,"error":{"code":"forbidden","message":"设备已吊销"}}`))
	}))
	defer server.Close()
	client, err := NewEndpointClient(BaseURLEntry{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = client.Sync(ctx, SyncRequest{DeviceID: "d", Secret: "s"})
	var serverError *ServerError
	if !errors.As(err, &serverError) {
		t.Fatalf("Sync error = %v, want *ServerError", err)
	}
	if serverError.Status != http.StatusForbidden || serverError.Code != "forbidden" || !strings.Contains(serverError.Message, "吊销") {
		t.Fatalf("server error = %+v", serverError)
	}
	if !isForbidden(err) {
		t.Fatal("isForbidden must recognize the mapped failure")
	}
}
