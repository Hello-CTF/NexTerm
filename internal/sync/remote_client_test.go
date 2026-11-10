package sync

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSyncHTTPClientCachesPerInsecureFlag(t *testing.T) {
	secure := syncHTTPClient(false)
	if secure != syncHTTPClient(false) {
		t.Fatal("secure client must be cached and reused")
	}
	insecure := syncHTTPClient(true)
	if insecure != syncHTTPClient(true) {
		t.Fatal("insecure client must be cached and reused")
	}
	if secure == insecure {
		t.Fatal("clients must differ by insecure flag")
	}
	if secure.Transport != syncHTTPClient(false).Transport {
		t.Fatal("transport must be reused across requests")
	}
	if !insecure.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify {
		t.Fatal("insecure client must skip TLS verification")
	}
	if secure.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify {
		t.Fatal("secure client must keep TLS verification")
	}
}

func TestSyncHTTPClientReusesConnections(t *testing.T) {
	var accepts atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	server.Listener = &countingListener{Listener: server.Listener, accepts: &accepts}
	server.Start()
	defer server.Close()

	client := syncHTTPClient(false)
	for range 2 {
		response, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
	if got := accepts.Load(); got != 1 {
		t.Fatalf("connection was not reused: %d accepts for 2 requests", got)
	}
}

type countingListener struct {
	net.Listener
	accepts *atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err == nil {
		l.accepts.Add(1)
	}
	return connection, err
}

func TestRemoteClientPushCompressesRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Encoding"); got != "gzip" {
			t.Errorf("request Content-Encoding = %q, want gzip", got)
			http.Error(w, "missing gzip", http.StatusBadRequest)
			return
		}
		reader, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Error(err)
			http.Error(w, "invalid gzip", http.StatusBadRequest)
			return
		}
		defer reader.Close()
		var request PushRequest
		if err := json.NewDecoder(reader).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if len(request.Objects) != 1 || len(request.Objects[0].Blob) != 2048 {
			t.Errorf("request objects = %+v", request.Objects)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(PushResponse{Protocol: ProtocolVersion, Head: "next", MaxSeq: 1, Applied: 1})
	}))
	defer server.Close()
	client := &remoteClient{base: server.URL}
	response, err := client.push(context.Background(), "head", []WireObject{{ID: "obj-1", Blob: make([]byte, 2048)}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Applied != 1 || response.Head != "next" {
		t.Fatalf("push response = %+v", response)
	}
}

// A route the server does not serve falls to its web app, which answers HTTP
// 200 with index.html; the error must say a web page came back, not only that
// the JSON was invalid.
func TestRemoteClientNamesAWebPageInsteadOfJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>NexTerm</title>"))
	}))
	defer server.Close()
	client, err := newRemoteClient(RemoteConfig{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ListSessions(t.Context(), 0)
	if err == nil || !strings.Contains(err.Error(), "服务端返回了网页") {
		t.Fatalf("ListSessions error = %v, want it to name the web page", err)
	}
}

// A held CSRF token goes stale when the server's CSRF key changes while the
// session stays valid; the client re-reads the token once and resends the
// write with its original body.
func TestRemoteClientRefreshesStaleCSRFTokenOnce(t *testing.T) {
	var pushes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/auth/me":
			_, _ = io.WriteString(w, `{"user":{"id":"user-1"},"csrf_token":"fresh"}`)
		case "/sync/v2/push":
			pushes.Add(1)
			cookies := r.Header.Values("Cookie")
			if len(cookies) != 1 || cookies[0] != sessionCookieName+"=cookie" {
				t.Errorf("push cookies = %q", cookies)
			}
			if r.Header.Get(csrfHeaderName) != "fresh" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"ok":false,"error":{"code":"forbidden","message":"CSRF 校验失败：页面已过期，请刷新后重试"}}`)
				return
			}
			var request PushRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.KnownHead != "head" {
				t.Errorf("retried push body = %+v, %v", request, err)
			}
			_ = json.NewEncoder(w).Encode(PushResponse{Protocol: ProtocolVersion, Head: "next", MaxSeq: 1, Applied: 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := &remoteClient{base: server.URL, cookieToken: "cookie", csrfToken: "stale"}
	response, err := client.push(context.Background(), "head", []WireObject{{ID: "obj-1", Blob: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Applied != 1 || pushes.Load() != 2 || client.csrfToken != "fresh" {
		t.Fatalf("push response = %+v pushes = %d token = %q", response, pushes.Load(), client.csrfToken)
	}
}
