package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type fakeStep struct {
	status       int
	body         string
	bodyOverride io.ReadCloser
	err          error
	wroteRequest bool
}

type recordedRequest struct {
	model     string
	stream    bool
	toolCount int
}

type fakeProvider struct {
	mu       sync.Mutex
	steps    []fakeStep
	requests []recordedRequest
}

func (f *fakeProvider) RoundTrip(request *http.Request) (*http.Response, error) {
	var body struct {
		Model  string            `json:"model"`
		Stream bool              `json:"stream"`
		Tools  []json.RawMessage `json:"tools"`
	}
	_ = json.NewDecoder(request.Body).Decode(&body)
	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{model: body.Model, stream: body.Stream, toolCount: len(body.Tools)})
	index := len(f.requests) - 1
	step := fakeStep{err: fmt.Errorf("unexpected fake-provider request %d", index)}
	if index < len(f.steps) {
		step = f.steps[index]
	}
	f.mu.Unlock()
	if step.wroteRequest {
		trace := httptrace.ContextClientTrace(request.Context())
		if trace != nil && trace.WroteHeaders != nil {
			trace.WroteHeaders()
		}
	}
	if step.err != nil && step.status == 0 {
		return nil, step.err
	}
	if step.status == 0 {
		step.status = http.StatusOK
	}
	responseBody := io.NopCloser(strings.NewReader(step.body))
	if step.bodyOverride != nil {
		responseBody = step.bodyOverride
	}
	response := &http.Response{
		StatusCode: step.status, Header: make(http.Header),
		Body: responseBody, Request: request,
	}
	return response, step.err
}

func (f *fakeProvider) Requests() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]recordedRequest, len(f.requests))
	copy(result, f.requests)
	return result
}

func newFakeClient(t *testing.T, transport http.RoundTripper, config Config, maxRetries int, sleep func(context.Context, time.Duration) error, random func() float64) *Client {
	t.Helper()
	if config.BaseURL == "" {
		config.BaseURL = "http://provider.test/v1"
	}
	if sleep == nil {
		sleep = func(context.Context, time.Duration) error { return nil }
	}
	if random == nil {
		random = func() float64 { return 0.5 }
	}
	client, err := NewClient(config, withRetryHooks(sleep, random))
	if err != nil {
		t.Fatal(err)
	}
	client.retry = RetryPolicy{
		MaxRetries: maxRetries, InitialBackoff: 100 * time.Millisecond,
		MaxBackoff: 250 * time.Millisecond, Jitter: 0.5,
	}
	client.http.Transport = &wireTransport{base: transport}
	return client
}

func errorStep(status int) fakeStep {
	return fakeStep{status: status, body: `{"error":{"message":"injected failure"}}`}
}

func blockStep(model, content string) fakeStep {
	encoded, _ := json.Marshal(map[string]any{
		"model": model,
		"choices": []any{map[string]any{
			"message": map[string]any{"content": content}, "finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2},
	})
	return fakeStep{status: http.StatusOK, body: string(encoded)}
}

func streamStep(model, content string) fakeStep {
	encoded, _ := json.Marshal(map[string]any{
		"model": model,
		"choices": []any{map[string]any{
			"delta": map[string]any{"content": content}, "finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2},
	})
	return fakeStep{status: http.StatusOK, body: "data: " + string(encoded) + "\n\ndata: [DONE]\n\n"}
}

func requestModels(requests []recordedRequest) []string {
	models := make([]string, len(requests))
	for index, request := range requests {
		models[index] = request.model
	}
	return models
}

func TestProviderErrorClassification(t *testing.T) {
	var syntaxErr error
	if err := json.Unmarshal([]byte(`{"broken"`), new(any)); err != nil {
		syntaxErr = err
	}
	tests := []struct {
		name string
		err  error
		want ErrorClass
	}{
		{name: "bad request", err: &HTTPError{StatusCode: 400}, want: ErrorClassClient},
		{name: "rate limit", err: &HTTPError{StatusCode: 429}, want: ErrorClassRateLimit},
		{name: "server", err: &HTTPError{StatusCode: 503}, want: ErrorClassServer},
		{name: "cancelled", err: fmt.Errorf("wrapped: %w", context.Canceled), want: ErrorClassCancellation},
		{name: "deadline", err: context.DeadlineExceeded, want: ErrorClassCancellation},
		{name: "transport", err: io.ErrUnexpectedEOF, want: ErrorClassTransport},
		{name: "protocol", err: syntaxErr, want: ErrorClassProtocol},
		{name: "unknown", err: errors.New("other"), want: ErrorClassUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyError(test.err); got != test.want {
				t.Fatalf("ClassifyError() = %q, want %q", got, test.want)
			}
		})
	}
	if got := ClassifyError(nil); got != "" {
		t.Fatalf("ClassifyError(nil) = %q", got)
	}
}

func TestRetryBudgetBackoffAndFallbackUsage(t *testing.T) {
	provider := &fakeProvider{steps: []fakeStep{
		errorStep(http.StatusTooManyRequests), errorStep(http.StatusInternalServerError),
		errorStep(http.StatusServiceUnavailable), blockStep("served-backup", "ok"),
	}}
	var sleeps []time.Duration
	randomValues := []float64{0.75, 0.25}
	client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup"}, 1,
		func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			return nil
		},
		func() float64 {
			value := randomValues[0]
			randomValues = randomValues[1:]
			return value
		},
	)
	completion, err := client.ChatBlock(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	requests := provider.Requests()
	if !reflect.DeepEqual(requestModels(requests), []string{"primary", "primary", "backup", "backup"}) {
		t.Fatalf("request models = %v", requestModels(requests))
	}
	if !reflect.DeepEqual(sleeps, []time.Duration{125 * time.Millisecond, 75 * time.Millisecond}) {
		t.Fatalf("backoffs = %v", sleeps)
	}
	if completion.Model != "served-backup" || completion.Usage.Model != "served-backup" || completion.Usage.PromptTokens != 3 || completion.Usage.CompletionTokens != 2 {
		t.Fatalf("fallback completion = %+v", completion)
	}
}

func TestRetryBudgetExhaustionWithoutFallback(t *testing.T) {
	provider := &fakeProvider{steps: []fakeStep{
		errorStep(http.StatusInternalServerError), errorStep(http.StatusInternalServerError), errorStep(http.StatusInternalServerError),
		blockStep("must-not-run", "extra"),
	}}
	var sleeps []time.Duration
	client := newFakeClient(t, provider, Config{Model: "primary"}, 2,
		func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			return nil
		}, nil,
	)
	_, err := client.ChatBlock(context.Background(), ChatRequest{})
	if status, ok := statusCodeForError(err); !ok || status != http.StatusInternalServerError {
		t.Fatalf("terminal error = %v (status=%d, ok=%v)", err, status, ok)
	}
	if got := requestModels(provider.Requests()); !reflect.DeepEqual(got, []string{"primary", "primary", "primary"}) {
		t.Fatalf("requests = %v", got)
	}
	if !reflect.DeepEqual(sleeps, []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}) {
		t.Fatalf("exponential backoffs = %v", sleeps)
	}
}

func TestCancellationDuringBackoffStopsFallback(t *testing.T) {
	provider := &fakeProvider{steps: []fakeStep{errorStep(http.StatusTooManyRequests), blockStep("backup", "must-not-run")}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup"}, 3,
		func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err()
		}, nil,
	)
	_, err := client.ChatBlock(ctx, ChatRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	if got := requestModels(provider.Requests()); !reflect.DeepEqual(got, []string{"primary"}) {
		t.Fatalf("requests after cancellation = %v", got)
	}
}

func TestTransportRetryRequiresUnwrittenRequest(t *testing.T) {
	t.Run("before request write", func(t *testing.T) {
		provider := &fakeProvider{steps: []fakeStep{
			{err: errors.New("injected dial failure")}, blockStep("primary", "recovered"), blockStep("backup", "must-not-run"),
		}}
		client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup"}, 1, nil, nil)
		completion, err := client.ChatBlock(context.Background(), ChatRequest{})
		if err != nil || completion.Content != "recovered" {
			t.Fatalf("ChatBlock() = %+v, %v", completion, err)
		}
		if got := requestModels(provider.Requests()); !reflect.DeepEqual(got, []string{"primary", "primary"}) {
			t.Fatalf("transport retry requests = %v", got)
		}
	})
	t.Run("after request write", func(t *testing.T) {
		provider := &fakeProvider{steps: []fakeStep{
			{err: errors.New("injected ambiguous write failure"), wroteRequest: true}, blockStep("backup", "must-not-run"),
		}}
		client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup"}, 2, nil, nil)
		_, err := client.ChatBlock(context.Background(), ChatRequest{})
		if err == nil || ClassifyError(err) != ErrorClassTransport {
			t.Fatalf("ambiguous transport error = %v (%s)", err, ClassifyError(err))
		}
		if got := requestModels(provider.Requests()); !reflect.DeepEqual(got, []string{"primary"}) {
			t.Fatalf("ambiguous request was replayed = %v", got)
		}
	})
}

func TestBadRequestDoesNotSelectFallbackModel(t *testing.T) {
	t.Run("block", func(t *testing.T) {
		provider := &fakeProvider{steps: []fakeStep{errorStep(http.StatusBadRequest), blockStep("backup", "must-not-run")}}
		client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup"}, 2, nil, nil)
		_, err := client.ChatBlock(context.Background(), ChatRequest{})
		if status, ok := statusCodeForError(err); !ok || status != http.StatusBadRequest {
			t.Fatalf("bad request error = %v (status=%d, ok=%v)", err, status, ok)
		}
		if got := requestModels(provider.Requests()); !reflect.DeepEqual(got, []string{"primary"}) {
			t.Fatalf("400 selected another model = %v", got)
		}
	})
	t.Run("stream compatibility stays on primary", func(t *testing.T) {
		provider := &fakeProvider{steps: []fakeStep{errorStep(http.StatusBadRequest), blockStep("primary", "block-ok")}}
		client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup", Stream: true}, 0, nil, nil)
		var frames []*schema.Message
		result, err := client.runNative(context.Background(), nil, nil, true, func(message *schema.Message) error {
			frames = append(frames, message)
			return nil
		})
		if err != nil || result.completion.Content != "block-ok" {
			t.Fatalf("stream compatibility fallback = %+v, %v", result.completion, err)
		}
		if len(frames) != 1 || frames[0].Content != "block-ok" {
			t.Fatalf("compatibility frames = %+v", frames)
		}
		requests := provider.Requests()
		if !reflect.DeepEqual(requestModels(requests), []string{"primary", "primary"}) || !requests[0].stream || requests[1].stream {
			t.Fatalf("compatibility requests = %+v", requests)
		}
	})
}

func TestFirstStreamFramePreventsReplay(t *testing.T) {
	for _, test := range []struct {
		name       string
		body       string
		wantFrames []string
	}{
		{
			name:       "visible output",
			body:       "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\ndata: invalid\n\n",
			wantFrames: []string{"partial"},
		},
		{
			name:       "usage-only frame",
			body:       "data: {\"usage\":{\"prompt_tokens\":1}}\n\ndata: invalid\n\n",
			wantFrames: []string{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeProvider{steps: []fakeStep{{status: http.StatusOK, body: test.body}, streamStep("backup", "must-not-run")}}
			client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup", Stream: true}, 3, nil, nil)
			var frames []*schema.Message
			_, err := client.runNative(context.Background(), nil, nil, true, func(message *schema.Message) error {
				frames = append(frames, message)
				return nil
			})
			if err == nil {
				t.Fatal("malformed stream succeeded")
			}
			if got := requestModels(provider.Requests()); !reflect.DeepEqual(got, []string{"primary"}) {
				t.Fatalf("post-frame requests = %v", got)
			}
			contents := make([]string, len(frames))
			for index, frame := range frames {
				contents[index] = frame.Content
			}
			if !reflect.DeepEqual(contents, test.wantFrames) {
				t.Fatalf("post-frame stream contents = %#v, want %#v", contents, test.wantFrames)
			}
		})
	}
}

func TestFallbackBeforeFirstStreamFrameEmitsOnlyFallback(t *testing.T) {
	provider := &fakeProvider{steps: []fakeStep{errorStep(http.StatusInternalServerError), streamStep("served-backup", "backup")}}
	client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup", Stream: true}, 0, nil, nil)
	var frames []*schema.Message
	result, err := client.runNative(context.Background(), nil, nil, true, func(message *schema.Message) error {
		frames = append(frames, message)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := requestModels(provider.Requests()); !reflect.DeepEqual(got, []string{"primary", "backup"}) {
		t.Fatalf("fallback requests = %v", got)
	}
	if len(frames) != 1 || frames[0].Content != "backup" ||
		result.completion.Content != "backup" || result.completion.Model != "served-backup" || result.completion.Usage.Model != "served-backup" {
		t.Fatalf("fallback output = %+v, frames=%+v", result.completion, frames)
	}
}

func TestConcurrentFallbackUsesIndependentModels(t *testing.T) {
	var primaryCalls, backupCalls atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		var step fakeStep
		switch body.Model {
		case "primary":
			primaryCalls.Add(1)
			step = errorStep(http.StatusTooManyRequests)
		case "backup":
			backupCalls.Add(1)
			step = blockStep("served-backup", "ok")
		default:
			return nil, fmt.Errorf("unexpected model %q", body.Model)
		}
		return &http.Response{
			StatusCode: step.status, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(step.body)), Request: request,
		}, nil
	})
	client := newFakeClient(t, transport, Config{Model: "primary", FallbackModel: "backup"}, 0, nil, nil)
	const count = 16
	var wait sync.WaitGroup
	failures := make(chan error, count)
	for range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			completion, err := client.ChatBlock(context.Background(), ChatRequest{})
			if err != nil {
				failures <- err
				return
			}
			if completion.Content != "ok" || completion.Model != "served-backup" || completion.Usage.Model != "served-backup" {
				failures <- fmt.Errorf("completion = %+v", completion)
			}
		}()
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if primaryCalls.Load() != count || backupCalls.Load() != count {
		t.Fatalf("calls = primary:%d backup:%d", primaryCalls.Load(), backupCalls.Load())
	}
}
