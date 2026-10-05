package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxErrorBody = 64 << 10

type Timeouts struct {
	Connect        time.Duration
	Stream         time.Duration
	Block          time.Duration
	Models         time.Duration
	Idle           time.Duration
	ResponseHeader time.Duration
}

func DefaultTimeouts() Timeouts {
	return Timeouts{
		Connect:        20 * time.Second,
		Stream:         300 * time.Second,
		Block:          180 * time.Second,
		Models:         15 * time.Second,
		Idle:           60 * time.Second,
		ResponseHeader: 300 * time.Second,
	}
}

type Option func(*clientOptions)

type clientOptions struct {
	timeouts Timeouts
	retry    RetryPolicy
	circuit  *CircuitBreaker
	sleep    func(context.Context, time.Duration) error
	random   func() float64
}

func WithTimeouts(timeouts Timeouts) Option {
	return func(options *clientOptions) {
		if timeouts.Connect > 0 {
			options.timeouts.Connect = timeouts.Connect
		}
		if timeouts.Stream > 0 {
			options.timeouts.Stream = timeouts.Stream
		}
		if timeouts.Block > 0 {
			options.timeouts.Block = timeouts.Block
		}
		if timeouts.Models > 0 {
			options.timeouts.Models = timeouts.Models
		}
		if timeouts.Idle > 0 {
			options.timeouts.Idle = timeouts.Idle
		}
		if timeouts.ResponseHeader > 0 {
			options.timeouts.ResponseHeader = timeouts.ResponseHeader
		}
	}
}

func WithIdleTimeout(idle time.Duration) Option {
	return func(options *clientOptions) {
		options.timeouts.Idle = max(idle, 0)
	}
}

func WithCircuitBreaker(breaker *CircuitBreaker) Option {
	return func(options *clientOptions) {
		options.circuit = breaker
	}
}

type Client struct {
	config   Config
	http     *http.Client
	timeouts Timeouts
	retry    RetryPolicy
	circuit  *CircuitBreaker
	sleep    func(context.Context, time.Duration) error
	random   func() float64
}

func NewClient(config Config, options ...Option) (*Client, error) {
	config = config.Normalized()
	settings := clientOptions{
		timeouts: DefaultTimeouts(), retry: DefaultRetryPolicy(),
		sleep: sleepWithContext, random: randomUnit,
	}
	for _, option := range options {
		option(&settings)
	}
	if strings.ContainsAny(config.APIKey, "\r\n") {
		return nil, errors.New("AI API key contains an invalid newline")
	}

	dialer := &net.Dialer{Timeout: settings.timeouts.Connect, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = dialer.DialContext
	transport.ResponseHeaderTimeout = settings.timeouts.ResponseHeader
	if config.Proxy != nil {
		proxyURL, err := url.Parse(*config.Proxy)
		if err != nil || proxyURL.Host == "" || proxyURL.Hostname() == "" {
			return nil, errors.New("invalid AI proxy configuration")
		}
		switch proxyURL.Scheme {
		case "http", "https", "socks5":
		default:
			return nil, errors.New("unsupported AI proxy scheme")
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &Client{
		config:   config,
		http:     &http.Client{Transport: &wireTransport{base: transport, idle: settings.timeouts.Idle}},
		timeouts: settings.timeouts,
		retry:    settings.retry,
		circuit:  settings.circuit,
		sleep:    settings.sleep,
		random:   settings.random,
	}, nil
}

func (c *Client) Config() Config {
	config := c.config
	if c.config.MaxTokens != nil {
		maxTokens := *c.config.MaxTokens
		config.MaxTokens = &maxTokens
	}
	if c.config.Proxy != nil {
		proxy := *c.config.Proxy
		config.Proxy = &proxy
	}
	return config
}

func (c *Client) endpoint(path string) string {
	return strings.TrimRight(c.config.BaseURL, "/") + path
}

func (c *Client) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode AI request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), reader)
	if err != nil {
		return nil, fmt.Errorf("build AI request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.config.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	}
	return request, nil
}

type HTTPError struct {
	Operation  string
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s: HTTP %d", e.Operation, e.StatusCode)
	}
	return fmt.Sprintf("%s: HTTP %d: %s", e.Operation, e.StatusCode, e.Body)
}

func responseError(operation string, response *http.Response) error {
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
	message := strings.TrimSpace(string(body))
	var payload struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && len(payload.Error) != 0 {
		var nested struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(payload.Error, &nested) == nil && nested.Message != "" {
			message = nested.Message
		} else {
			var plain string
			if json.Unmarshal(payload.Error, &plain) == nil && plain != "" {
				message = plain
			}
		}
	}
	if readErr != nil && message == "" {
		message = readErr.Error()
	}
	return &HTTPError{Operation: operation, StatusCode: response.StatusCode, Body: truncate(message, 500)}
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && (value[end]&0xc0) == 0x80 {
		end--
	}
	return value[:end] + "…"
}

func safeStreamFallback(err error) bool {
	status, ok := statusCodeForError(err)
	if !ok {
		return false
	}
	switch status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed,
		http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}
