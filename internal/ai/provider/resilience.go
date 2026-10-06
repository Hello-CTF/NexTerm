package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	sdkopenai "github.com/meguminnnnnnnnn/go-openai"
)

type ErrorClass string

const (
	ErrorClassUnknown      ErrorClass = "unknown"
	ErrorClassCancellation ErrorClass = "cancellation"
	ErrorClassClient       ErrorClass = "client"
	ErrorClassRateLimit    ErrorClass = "rate_limit"
	ErrorClassServer       ErrorClass = "server"
	ErrorClassTransport    ErrorClass = "transport"
	ErrorClassProtocol     ErrorClass = "protocol"
)

func ClassifyError(err error) ErrorClass {
	if err == nil {
		return ""
	}
	if isResponseHeaderTimeout(err) {
		return ErrorClassTransport
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrorClassCancellation
	}
	if status, ok := statusCodeForError(err); ok {
		return classifyHTTPStatus(status)
	}
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
		return ErrorClassProtocol
	}
	var netErr net.Error
	if errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return ErrorClassTransport
	}
	return ErrorClassUnknown
}

func isResponseHeaderTimeout(err error) bool {
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		return false
	}
	return strings.Contains(err.Error(), "timeout awaiting response headers")
}

func classifyHTTPStatus(status int) ErrorClass {
	switch {
	case status == http.StatusTooManyRequests:
		return ErrorClassRateLimit
	case status >= 500 && status <= 599:
		return ErrorClassServer
	case status >= 400 && status <= 499:
		return ErrorClassClient
	default:
		return ErrorClassProtocol
	}
}

func statusCodeForError(err error) (int, bool) {
	var statusErr *statusError
	var httpErr *HTTPError
	var componentErr *einoopenai.APIError
	var sdkErr *sdkopenai.APIError
	var requestErr *sdkopenai.RequestError
	switch {
	case errors.As(err, &statusErr):
		return statusErr.status, statusErr.status != 0
	case errors.As(err, &httpErr):
		return httpErr.StatusCode, true
	case errors.As(err, &componentErr):
		return componentErr.HTTPStatusCode, componentErr.HTTPStatusCode != 0
	case errors.As(err, &sdkErr):
		return sdkErr.HTTPStatusCode, sdkErr.HTTPStatusCode != 0
	case errors.As(err, &requestErr):
		return requestErr.HTTPStatusCode, requestErr.HTTPStatusCode != 0
	default:
		return 0, false
	}
}

const maxRetryAfterDelay = 30 * time.Second

type RetryPolicy struct {
	MaxRetries     int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Jitter         float64
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries: 2, InitialBackoff: 200 * time.Millisecond,
		MaxBackoff: 2 * time.Second, Jitter: 0.2,
	}
}

func withRetryHooks(sleep func(context.Context, time.Duration) error, random func() float64) Option {
	return func(options *clientOptions) {
		if sleep != nil {
			options.sleep = sleep
		}
		if random != nil {
			options.random = random
		}
	}
}

func (p RetryPolicy) backoff(retry int, random func() float64) time.Duration {
	if retry <= 0 {
		return 0
	}
	delay := p.InitialBackoff
	for index := 1; index < retry && delay < p.MaxBackoff; index++ {
		if delay > p.MaxBackoff/2 {
			delay = p.MaxBackoff
		} else {
			delay *= 2
		}
	}
	if p.Jitter != 0 && delay > 0 {
		factor := 1 + (random()*2-1)*p.Jitter
		delay = time.Duration(float64(delay) * factor)
		if delay > p.MaxBackoff {
			delay = p.MaxBackoff
		}
	}
	return max(delay, 0)
}

func randomUnit() float64 {
	return rand.Float64()
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type requestSafety struct {
	wroteRequest atomic.Bool
	gotResponse  atomic.Bool
}

type attemptState struct {
	request        *requestSafety
	frameSeen      atomic.Bool
	streaming      bool
	runID          string
	callID         string
	requestedModel string
	servingModel   atomic.Value
	cacheCreation  atomic.Uint64
	retryAfter     atomic.Int64
}

func (s *attemptState) captureRetryAfter(value string, now time.Time) {
	delay := parseRetryAfter(value, now)
	if delay > 0 {
		s.retryAfter.Store(int64(delay))
	}
}

func (s *attemptState) retryAfterHint() time.Duration {
	return time.Duration(s.retryAfter.Load())
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > int(maxRetryAfterDelay/time.Second) {
			return maxRetryAfterDelay
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		return max(date.Sub(now), 0)
	}
	return 0
}

func (s *attemptState) setServingModel(model string) {
	if model != "" {
		s.servingModel.Store(model)
	}
}

func (s *attemptState) actualModel() string {
	model, _ := s.servingModel.Load().(string)
	return model
}

func (s *attemptState) setCacheCreation(tokens uint64) {
	if tokens > 0 {
		s.cacheCreation.Store(tokens)
	}
}

func (s *attemptState) cacheCreationTokens() uint64 {
	return s.cacheCreation.Load()
}

type attemptStateKey struct{}

func (s *attemptState) trackContext(ctx context.Context) context.Context {
	safety := &requestSafety{}
	s.request = safety
	trace := &httptrace.ClientTrace{
		WroteHeaders:         func() { safety.wroteRequest.Store(true) },
		WroteRequest:         func(httptrace.WroteRequestInfo) { safety.wroteRequest.Store(true) },
		GotFirstResponseByte: func() { safety.gotResponse.Store(true) },
	}
	ctx = context.WithValue(ctx, attemptStateKey{}, s)
	return httptrace.WithClientTrace(ctx, trace)
}

func attemptFromContext(ctx context.Context) *attemptState {
	state, _ := ctx.Value(attemptStateKey{}).(*attemptState)
	return state
}

func replayAllowed(err error, state *attemptState) bool {
	if err == nil || state == nil || state.frameSeen.Load() {
		return false
	}
	switch ClassifyError(err) {
	case ErrorClassRateLimit, ErrorClassServer:
		return true
	case ErrorClassTransport:
		return state.request != nil && !state.request.wroteRequest.Load() && !state.request.gotResponse.Load()
	default:
		return false
	}
}
