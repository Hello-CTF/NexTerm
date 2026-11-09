package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestServeRoutesHTTPServerErrorsToAppLogger(t *testing.T) {
	var logs syncBuffer
	application, err := New(Config{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	transport := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("handler exploded")
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- application.Serve(ctx, ServeConfig{Listen: "127.0.0.1:0", Listener: listener, Transport: transport, Stderr: io.Discard})
	}()
	defer func() {
		cancel()
		select {
		case <-result:
		case <-time.After(15 * time.Second):
			t.Error("server did not stop")
		}
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	// net/http logs the panic before closing the connection, so the failed request orders the log write.
	if response, err := client.Get("http://" + listener.Addr().String() + "/"); err == nil {
		response.Body.Close()
		t.Fatal("panicking handler returned a response")
	}
	got := logs.String()
	if !strings.Contains(got, "handler exploded") || !strings.Contains(got, "level=WARN") || !strings.Contains(got, "component=http") {
		t.Fatalf("http server error not in the app log: %q", got)
	}
}
