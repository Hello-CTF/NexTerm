package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type sleepRecorder struct {
	mu     sync.Mutex
	sleeps []time.Duration
}

func (r *sleepRecorder) record(delay time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sleeps = append(r.sleeps, delay)
}

func (r *sleepRecorder) list() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]time.Duration, len(r.sleeps))
	copy(result, r.sleeps)
	return result
}

func newRetryHookClient(t *testing.T, config Config, maxRetries int, recorder *sleepRecorder) *Client {
	t.Helper()
	client, err := NewClient(config,
		WithRetryPolicy(RetryPolicy{
			MaxRetries: maxRetries, InitialBackoff: 10 * time.Millisecond,
			MaxBackoff: 20 * time.Millisecond, Jitter: 0,
		}),
		withRetryHooks(func(_ context.Context, delay time.Duration) error {
			recorder.record(delay)
			return nil
		}, func() float64 { return 0.5 }),
	)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestRetryAfterHeaderControlsRetryDelay(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			writer.Header().Set("Retry-After", "1")
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = writer.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	recorder := &sleepRecorder{}
	client := newRetryHookClient(t, Config{BaseURL: server.URL, Model: "m"}, 2, recorder)
	completion, err := client.ChatBlock(context.Background(), ChatRequest{})
	if err != nil || completion.Content != "ok" {
		t.Fatalf("ChatBlock() = %+v, %v", completion, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
	if sleeps := recorder.list(); len(sleeps) != 1 || sleeps[0] != time.Second {
		t.Fatalf("sleeps = %v, want [1s]", sleeps)
	}
}

func TestRetryAfterHTTPDateAndCap(t *testing.T) {
	t.Run("http date", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Retry-After", time.Now().Add(2*time.Second).UTC().Format(http.TimeFormat))
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"error":{"message":"unavailable"}}`))
		}))
		defer server.Close()
		recorder := &sleepRecorder{}
		client := newRetryHookClient(t, Config{BaseURL: server.URL, Model: "m"}, 1, recorder)
		_, err := client.ChatBlock(context.Background(), ChatRequest{})
		if err == nil {
			t.Fatal("unavailable chat succeeded")
		}
		if sleeps := recorder.list(); len(sleeps) != 1 || sleeps[0] <= 0 || sleeps[0] > 2*time.Second {
			t.Fatalf("sleeps = %v", sleeps)
		}
	})
	t.Run("capped", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Retry-After", "999")
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = writer.Write([]byte(`{"error":{"message":"slow down"}}`))
		}))
		defer server.Close()
		recorder := &sleepRecorder{}
		client := newRetryHookClient(t, Config{BaseURL: server.URL, Model: "m"}, 1, recorder)
		_, _ = client.ChatBlock(context.Background(), ChatRequest{})
		if sleeps := recorder.list(); len(sleeps) != 1 || sleeps[0] != maxRetryAfterDelay {
			t.Fatalf("sleeps = %v, want [%s]", sleeps, maxRetryAfterDelay)
		}
	})
	t.Run("invalid header falls back to backoff", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Retry-After", "soon")
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = writer.Write([]byte(`{"error":{"message":"slow down"}}`))
		}))
		defer server.Close()
		recorder := &sleepRecorder{}
		client := newRetryHookClient(t, Config{BaseURL: server.URL, Model: "m"}, 1, recorder)
		_, _ = client.ChatBlock(context.Background(), ChatRequest{})
		if sleeps := recorder.list(); len(sleeps) != 1 || sleeps[0] != 10*time.Millisecond {
			t.Fatalf("sleeps = %v, want [10ms]", sleeps)
		}
	})
}

func TestPermanentAuthFailureIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer server.Close()
	recorder := &sleepRecorder{}
	client := newRetryHookClient(t, Config{BaseURL: server.URL, Model: "m", FallbackModel: "backup"}, 3, recorder)
	_, err := client.ChatBlock(context.Background(), ChatRequest{})
	if status, ok := statusCodeForError(err); !ok || status != http.StatusUnauthorized {
		t.Fatalf("error = %v (status=%d ok=%v)", err, status, ok)
	}
	if calls.Load() != 1 || len(recorder.list()) != 0 {
		t.Fatalf("calls = %d sleeps = %v", calls.Load(), recorder.list())
	}
}

func TestStreamIdleTimeoutCutsStalledStream(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher := writer.(http.Flusher)
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
		flusher.Flush()
		waitForDisconnect(request, release)
	}))
	defer server.Close()
	defer close(release)
	client, err := NewClient(Config{BaseURL: server.URL, Model: "m", Stream: true}, WithIdleTimeout(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	var items []StreamItem
	completion, err := client.Chat(context.Background(), ChatRequest{}, func(item StreamItem) { items = append(items, item) })
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("idle error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("idle timeout took %v", elapsed)
	}
	if calls.Load() != 1 {
		t.Fatalf("stalled stream was replayed: calls = %d", calls.Load())
	}
	if len(items) != 1 || items[0].Text != "partial" {
		t.Fatalf("items = %#v", items)
	}
	if completion.Content != "partial" {
		t.Fatalf("partial completion = %+v", completion)
	}
}

func TestStreamIdleTimeoutDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher := writer.(http.Flusher)
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n"))
		flusher.Flush()
		time.Sleep(150 * time.Millisecond)
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, Model: "m", Stream: true}, WithIdleTimeout(0))
	if err != nil {
		t.Fatal(err)
	}
	completion, err := client.Chat(context.Background(), ChatRequest{}, nil)
	if err != nil || completion.Content != "ab" {
		t.Fatalf("Chat() = %+v, %v", completion, err)
	}
}

func TestBlockIdleTimeoutCutsStalledBody(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"choices":`))
		writer.(http.Flusher).Flush()
		waitForDisconnect(request, release)
	}))
	defer server.Close()
	defer close(release)
	client, err := NewClient(Config{BaseURL: server.URL, Model: "m"}, WithIdleTimeout(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = client.ChatBlock(context.Background(), ChatRequest{})
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("idle error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("idle timeout took %v", elapsed)
	}
}

func TestStreamErrorFinalizesPartialUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4}}\n\ndata: invalid\n\n"))
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, Model: "m", Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	completion, err := client.Chat(context.Background(), ChatRequest{}, nil)
	if err == nil {
		t.Fatal("malformed stream succeeded")
	}
	if completion.Content != "partial" {
		t.Fatalf("partial content = %q", completion.Content)
	}
	if completion.Usage.PromptTokens != 9 || completion.Usage.CompletionTokens != 4 {
		t.Fatalf("partial usage = %+v", completion.Usage)
	}
	if completion.RunID == "" || completion.CallID == "" || completion.Usage.RunID != completion.RunID || completion.Usage.CallID != completion.CallID {
		t.Fatalf("partial identity = %+v", completion)
	}
	if completion.Model != "m" || completion.Usage.Model != "m" {
		t.Fatalf("partial model = %+v", completion)
	}
}

func waitForDisconnect(request *http.Request, release <-chan struct{}) {
	select {
	case <-request.Context().Done():
	case <-release:
	}
}

func TestRequestTotalTimeoutIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		waitForDisconnect(request, release)
	}))
	defer server.Close()
	defer close(release)
	recorder := &sleepRecorder{}
	client := newRetryHookClient(t, Config{BaseURL: server.URL, Model: "m"}, 3, recorder)
	client.timeouts.Block = 50 * time.Millisecond
	_, err := client.ChatBlock(context.Background(), ChatRequest{})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
	if calls.Load() != 1 || len(recorder.list()) != 0 {
		t.Fatalf("timeout was retried: calls = %d sleeps = %v", calls.Load(), recorder.list())
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Now()
	if got := parseRetryAfter("2", now); got != 2*time.Second {
		t.Fatalf("delta-seconds = %v", got)
	}
	if got := parseRetryAfter("9999999999", now); got != maxRetryAfterDelay {
		t.Fatalf("overflowing delta-seconds = %v", got)
	}
	if got := parseRetryAfter(now.Add(3*time.Second).UTC().Format(http.TimeFormat), now); got <= 2*time.Second || got > 3*time.Second {
		t.Fatalf("http date = %v", got)
	}
	for _, value := range []string{"", "0", "-1", "soon"} {
		if got := parseRetryAfter(value, now); got != 0 {
			t.Fatalf("parseRetryAfter(%q) = %v", value, got)
		}
	}
}

func TestConcurrentClientUseRace(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if calls.Add(1)%3 == 0 {
			writer.Header().Set("Retry-After", "0")
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = writer.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		encoded, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "ok"}}},
		})
		_, _ = writer.Write(encoded)
	}))
	defer server.Close()
	client := newRetryHookClient(t, Config{BaseURL: server.URL, Model: "m", FallbackModel: "backup"}, 1, &sleepRecorder{})
	done := make(chan error, 12)
	for range 12 {
		go func() {
			_, err := client.ChatBlock(context.Background(), ChatRequest{})
			done <- err
		}()
	}
	for range 12 {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}
