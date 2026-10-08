package sync

import (
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
