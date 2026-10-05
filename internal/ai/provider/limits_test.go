package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDefaultTimeoutsIncludeResponseHeader(t *testing.T) {
	if got := DefaultTimeouts().ResponseHeader; got != 300*time.Second {
		t.Fatalf("default ResponseHeader = %v, want 300s", got)
	}
}

func TestResponseHeaderTimeoutClassifiedTransport(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		time.Sleep(300 * time.Millisecond)
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"late"}}]}`))
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, Model: "m"},
		WithTimeouts(Timeouts{ResponseHeader: 50 * time.Millisecond, Block: 5 * time.Second}),
		WithRetryPolicy(RetryPolicy{MaxRetries: 3}),
	)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = client.ChatBlock(context.Background(), ChatRequest{})
	if err == nil {
		t.Fatal("stalled response headers succeeded")
	}
	if got := ClassifyError(err); got != ErrorClassTransport {
		t.Fatalf("ClassifyError() = %q, want %q (err=%v)", got, ErrorClassTransport, err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("response header timeout took %v", elapsed)
	}
	if calls.Load() != 1 {
		t.Fatalf("header timeout was replayed after request write: calls = %d", calls.Load())
	}
}

func TestConfigMaxTokensClampAndJSON(t *testing.T) {
	unset := Config{Model: "m"}.Normalized()
	if unset.MaxTokens != nil {
		t.Fatalf("default MaxTokens = %v, want nil", *unset.MaxTokens)
	}
	var legacy Config
	if err := json.Unmarshal([]byte(`{"model":"m","contextWindow":64000}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.MaxTokens != nil {
		t.Fatalf("legacy JSON MaxTokens = %v, want nil", *legacy.MaxTokens)
	}
	cases := []struct {
		name    string
		window  uint64
		value   int
		clamped int
	}{
		{name: "zero rises to one", window: 32_768, value: 0, clamped: 1},
		{name: "negative rises to one", window: 32_768, value: -5, clamped: 1},
		{name: "half of default window", window: 32_768, value: 20_000, clamped: 16_384},
		{name: "hard limit caps large windows", window: 2_000_000, value: 99_999, clamped: 32_768},
		{name: "small window halves", window: 1_000, value: 900, clamped: 500},
		{name: "value inside range kept", window: 128_000, value: 4_096, clamped: 4_096},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			config := Config{Model: "m", ContextWindow: test.window, MaxTokens: &test.value}.Normalized()
			if config.MaxTokens == nil || *config.MaxTokens != test.clamped {
				t.Fatalf("MaxTokens = %v, want %d", config.MaxTokens, test.clamped)
			}
		})
	}
}

type capturedRequest struct {
	body map[string]any
}

func newCaptureClient(t *testing.T, config Config, captured *capturedRequest) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		captured.body = body
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	t.Cleanup(server.Close)
	config.BaseURL = server.URL
	if config.Model == "" {
		config.Model = "m"
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestChatRequestPassesMaxTokens(t *testing.T) {
	var captured capturedRequest
	maxTokens := 4_096
	client := newCaptureClient(t, Config{ContextWindow: 128_000, MaxTokens: &maxTokens}, &captured)
	if _, err := client.ChatBlock(context.Background(), ChatRequest{}); err != nil {
		t.Fatal(err)
	}
	if captured.body["max_tokens"] != float64(4_096) {
		t.Fatalf("max_tokens on wire = %v", captured.body["max_tokens"])
	}
}

func TestChatRequestOmitsMaxTokensWhenUnset(t *testing.T) {
	var captured capturedRequest
	client := newCaptureClient(t, Config{}, &captured)
	if _, err := client.ChatBlock(context.Background(), ChatRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, exists := captured.body["max_tokens"]; exists {
		t.Fatalf("max_tokens leaked without configuration: %v", captured.body["max_tokens"])
	}
}

func TestCircuitBreakerCountsOnlyTransportAndServer(t *testing.T) {
	breaker := NewCircuitBreaker(2, time.Hour)
	breaker.RecordFailure(ErrorClassClient)
	breaker.RecordFailure(ErrorClassRateLimit)
	breaker.RecordFailure(ErrorClassCancellation)
	breaker.RecordFailure(ErrorClassUnknown)
	if snapshot := breaker.Snapshot(); snapshot.ConsecutiveFailures != 0 {
		t.Fatalf("non-transport failures counted: %+v", snapshot)
	}
	breaker.RecordFailure(ErrorClassServer)
	breaker.RecordFailure(ErrorClassTransport)
	snapshot := breaker.Snapshot()
	if snapshot.ConsecutiveFailures != 2 || !snapshot.Open(time.Now()) {
		t.Fatalf("breaker did not open: %+v", snapshot)
	}
	if breaker.Allow() {
		t.Fatal("open circuit admitted a request")
	}
	breaker.RecordSuccess()
	if snapshot := breaker.Snapshot(); snapshot.ConsecutiveFailures != 0 || snapshot.Open(time.Now()) {
		t.Fatalf("success did not reset breaker: %+v", snapshot)
	}
}

func TestCircuitBreakerCooldownExpires(t *testing.T) {
	breaker := NewCircuitBreaker(1, time.Minute)
	now := time.Now()
	breaker.now = func() time.Time { return now }
	breaker.RecordFailure(ErrorClassTransport)
	if breaker.Allow() {
		t.Fatal("breaker allowed request while cooling down")
	}
	now = now.Add(2 * time.Minute)
	if !breaker.Allow() {
		t.Fatal("breaker stayed open after cooldown")
	}
	breaker.RecordFailure(ErrorClassServer)
	if breaker.Allow() {
		t.Fatal("half-open failure did not reopen breaker")
	}
	if snapshot := breaker.Snapshot(); snapshot.ConsecutiveFailures != 2 {
		t.Fatalf("half-open failures = %+v", snapshot)
	}
}

func TestClientCircuitBreakerFailFast(t *testing.T) {
	var calls atomic.Int32
	var healthy atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if healthy.Load() {
			_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
			return
		}
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(`{"error":{"message":"injected"}}`))
	}))
	defer server.Close()
	breaker := NewCircuitBreaker(2, time.Hour)
	now := time.Now()
	breaker.now = func() time.Time { return now }
	client, err := NewClient(Config{BaseURL: server.URL, Model: "m"},
		WithCircuitBreaker(breaker),
		WithRetryPolicy(RetryPolicy{MaxRetries: 0}),
	)
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 2; round++ {
		if _, err := client.ChatBlock(context.Background(), ChatRequest{}); err == nil {
			t.Fatal("failing server succeeded")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
	_, err = client.ChatBlock(context.Background(), ChatRequest{})
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("circuit-open error = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("open circuit hit the server: calls = %d", calls.Load())
	}
	healthy.Store(true)
	now = now.Add(2 * time.Hour)
	completion, err := client.ChatBlock(context.Background(), ChatRequest{})
	if err != nil || completion.Content != "ok" {
		t.Fatalf("post-cooldown ChatBlock() = %+v, %v", completion, err)
	}
	if snapshot := breaker.Snapshot(); snapshot.ConsecutiveFailures != 0 || snapshot.Open(time.Now()) {
		t.Fatalf("recovery did not reset breaker: %+v", snapshot)
	}
}

func TestCircuitBreakerConcurrentRace(t *testing.T) {
	breaker := NewCircuitBreaker(3, 50*time.Millisecond)
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for round := 0; round < 50; round++ {
				if breaker.Allow() {
					breaker.RecordSuccess()
				} else {
					breaker.RecordFailure(ErrorClassTransport)
				}
				_ = breaker.Snapshot()
			}
		}(worker)
	}
	wait.Wait()
}
