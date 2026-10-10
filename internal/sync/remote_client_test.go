package sync

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
