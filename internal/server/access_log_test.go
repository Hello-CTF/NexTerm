package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type accessLogSink struct {
	mu      sync.Mutex
	records []map[string]any
	done    chan struct{}
}

func newAccessLogSink() *accessLogSink { return &accessLogSink{done: make(chan struct{}, 16)} }

func (s *accessLogSink) Write(p []byte) (int, error) {
	var record map[string]any
	if err := json.Unmarshal(p, &record); err != nil {
		return 0, err
	}
	if record["msg"] != "http request" {
		return len(p), nil
	}
	s.mu.Lock()
	s.records = append(s.records, record)
	s.mu.Unlock()
	s.done <- struct{}{}
	return len(p), nil
}

func (s *accessLogSink) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("no access log record")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.records[0]
	s.records = s.records[1:]
	return record
}

func newAccessLogServer(t *testing.T, sink *accessLogSink, handler http.Handler) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(sink, nil))
	server := httptest.NewServer(withAccessLog(logger, func(*http.Request) string { return "203.0.113.7" }, handler))
	t.Cleanup(server.Close)
	return server
}

func TestParseAccessLogEnv(t *testing.T) {
	for raw, want := range map[string]bool{"": false, "1": true, "true": true, "0": false} {
		got, err := ParseAccessLogEnv(func(string) string { return raw })
		if err != nil || got != want {
			t.Fatalf("ParseAccessLogEnv(%q) = %v, %v", raw, got, err)
		}
	}
	if _, err := ParseAccessLogEnv(func(string) string { return "yes please" }); err == nil || !strings.Contains(err.Error(), EnvAccessLog) {
		t.Fatalf("invalid value error = %v", err)
	}
}

func TestAccessLogRecordsStatusAndOmitsQueryAndBody(t *testing.T) {
	sink := newAccessLogSink()
	server := newAccessLogServer(t, sink, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch r.URL.Path {
		case "/missing":
			http.NotFound(w, r)
		default:
			_, _ = w.Write([]byte("hello"))
		}
	}))

	response, err := http.Post(server.URL+"/missing?token=query-secret", "text/plain", strings.NewReader("body-secret"))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	record := sink.next(t)
	if record["method"] != "POST" || record["path"] != "/missing" || record["status"] != float64(http.StatusNotFound) || record["client_ip"] != "203.0.113.7" {
		t.Fatalf("record = %v", record)
	}
	if _, ok := record["duration"]; !ok {
		t.Fatalf("record has no duration: %v", record)
	}
	encoded, _ := json.Marshal(record)
	for _, secret := range []string{"query-secret", "body-secret", "token="} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("access log contains %q: %s", secret, encoded)
		}
	}

	response, err = http.Get(server.URL + "/share/preview/link-token/extra")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	record = sink.next(t)
	if record["status"] != float64(http.StatusOK) || record["bytes"] != float64(len("hello")) || record["path"] != "/share/preview/***/extra" {
		t.Fatalf("record = %v", record)
	}
}

func TestAccessLogPathMasksCredentialSegments(t *testing.T) {
	for path, want := range map[string]string{
		"/share/public/abc":    "/share/public/***",
		"/share/preview/abc/x": "/share/preview/***/x",
		"/share/previews":      "/share/previews",
		"/share/preview/":      "/share/preview/",
		"/rpc":                 "/rpc",
	} {
		if got := accessLogPath(path); got != want {
			t.Errorf("accessLogPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestAccessLogKeepsFlusherWorking(t *testing.T) {
	sink := newAccessLogSink()
	release := make(chan struct{})
	server := newAccessLogServer(t, sink, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("first\n"))
		flusher.Flush()
		<-release
	}))

	response, err := http.Get(server.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	close(release)
	if err != nil || line != "first\n" {
		t.Fatalf("flushed line = %q, %v", line, err)
	}
	if record := sink.next(t); record["status"] != float64(http.StatusOK) {
		t.Fatalf("record = %v", record)
	}
}

func TestAccessLogPassesWebSocketHijackThrough(t *testing.T) {
	sink := newAccessLogSink()
	server := newAccessLogServer(t, sink, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Hijacker); !ok {
			http.Error(w, "no hijacker", http.StatusInternalServerError)
			return
		}
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		messageType, data, err := connection.Read(r.Context())
		if err != nil {
			return
		}
		_ = connection.Write(r.Context(), messageType, append([]byte("echo:"), data...))
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws/events?ticket=ws-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if err := connection.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	_, data, err := connection.Read(ctx)
	if err != nil || string(data) != "echo:ping" {
		t.Fatalf("echo = %q, %v", data, err)
	}
	record := sink.next(t)
	if record["status"] != float64(http.StatusSwitchingProtocols) || record["upgraded"] != true || record["path"] != "/ws/events" {
		t.Fatalf("record = %v", record)
	}
}

func TestServerAccessLogUsesTrustedProxyClientIP(t *testing.T) {
	sink := newAccessLogSink()
	config := testConfig(t, false)
	config.Logger = slog.New(slog.NewJSONHandler(sink, nil))
	config.Options.TrustedProxyCIDRs = []string{"127.0.0.1/32"}
	config.Options.TrustForwardedFor = true
	config.Options.AccessLog = true
	_, httpServer := newTestHTTP(t, config)

	request, err := http.NewRequest(http.MethodGet, httpServer.URL+"/healthz?token=query-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Forwarded-For", "198.51.100.4")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	record := sink.next(t)
	if record["msg"] != "http request" || record["path"] != "/healthz" || record["client_ip"] != "198.51.100.4" || record["status"] != float64(response.StatusCode) {
		t.Fatalf("record = %v", record)
	}
}
