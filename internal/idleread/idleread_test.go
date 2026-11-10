package idleread

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// get downloads url under an idle window and returns the body length and error.
func get(t *testing.T, url string, idle time.Duration) (int64, error) {
	t.Helper()
	ctx, watchdog, cancel := WithIdleTimeout(context.Background(), idle)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, Cause(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	n, err := io.Copy(io.Discard, watchdog.Reader(response.Body))
	return n, Cause(ctx, err)
}

func TestStalledBodyFailsWithIdleError(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	_, err := get(t, server.URL, 100*time.Millisecond)
	var idle *IdleError
	if !errors.As(err, &idle) {
		t.Fatalf("stalled download error = %v, want IdleError", err)
	}
}

func TestStalledHeadersFailWithIdleError(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	_, err := get(t, server.URL, 100*time.Millisecond)
	var idle *IdleError
	if !errors.As(err, &idle) {
		t.Fatalf("stalled headers error = %v, want IdleError", err)
	}
}

// A transfer that keeps delivering data finishes even when it takes far longer than the idle window.
func TestSlowProgressingBodyCompletes(t *testing.T) {
	const chunks = 8
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for range chunks {
			_, _ = w.Write([]byte("x"))
			w.(http.Flusher).Flush()
			time.Sleep(40 * time.Millisecond)
		}
	}))
	t.Cleanup(server.Close)
	n, err := get(t, server.URL, 200*time.Millisecond)
	if err != nil || n != chunks {
		t.Fatalf("slow download = %d bytes, %v", n, err)
	}
}
