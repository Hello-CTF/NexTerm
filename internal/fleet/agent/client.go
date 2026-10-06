package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxResponseBody = 1 << 20

type ServerError struct {
	Status  int
	Code    string
	Message string
}

func (e *ServerError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("server returned status %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("server returned status %d (%s): %s", e.Status, e.Code, e.Message)
}

func (e *ServerError) forbidden() bool {
	return e.Status == http.StatusForbidden
}

type EndpointClient struct {
	entry BaseURLEntry
	base  string
	http  *http.Client
}

func NewEndpointClient(entry BaseURLEntry) (*EndpointClient, error) {
	if err := validateBaseURL(entry.URL); err != nil {
		return nil, err
	}
	base := strings.TrimRight(entry.URL, "/")
	transport := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: entry.Insecure},
	}
	return &EndpointClient{
		entry: entry,
		base:  base,
		http:  &http.Client{Transport: transport},
	}, nil
}

func (c *EndpointClient) BaseURL() string {
	return c.base
}

func (c *EndpointClient) Insecure() bool {
	return c.entry.Insecure
}

func (c *EndpointClient) WSURL() string {
	scheme := "wss"
	if strings.HasPrefix(c.base, "http://") {
		scheme = "ws"
	}
	parsed, err := url.Parse(c.base)
	if err != nil {
		return ""
	}
	return scheme + "://" + parsed.Host + parsed.Path + PathDeviceWS
}

type healthView struct {
	OK      bool   `json:"ok"`
	Service string `json:"service"`
}

// Health probes the public health endpoint. It sends no credential of any
// kind, follows no redirect, and accepts only a genuine NexTerm health
// payload so a captive portal or a look-alike responder counts as unhealthy.
func (c *EndpointClient) Health(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+PathHealth, nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health probe returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return err
	}
	var view healthView
	if err := json.Unmarshal(body, &view); err != nil {
		return fmt.Errorf("health probe returned a non-NexTerm payload: %w", err)
	}
	if !view.OK || view.Service != healthServiceName {
		return fmt.Errorf("health probe rejected: ok=%v service=%q", view.OK, view.Service)
	}
	return nil
}

type EnrollRequest struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	AppVersion string `json:"app_version"`
}

type EnrollResponse struct {
	DeviceID          string         `json:"device_id"`
	Secret            string         `json:"secret"`
	BaseURLs          []BaseURLEntry `json:"base_urls"`
	MetricsIntervalMS int64          `json:"metrics_interval_ms"`
	DesiredAutostart  bool           `json:"desired_autostart"`
	TerminalEnabled   bool           `json:"terminal_enabled"`
}

func (c *EndpointClient) Enroll(ctx context.Context, request EnrollRequest) (*EnrollResponse, error) {
	var response EnrollResponse
	if err := c.doJSON(ctx, http.MethodPost, PathEnroll, request, &response); err != nil {
		return nil, err
	}
	if response.DeviceID == "" || response.Secret == "" {
		return nil, fmt.Errorf("enroll response is missing device credentials")
	}
	return &response, nil
}

type SyncRequest struct {
	DeviceID     string        `json:"device_id"`
	Secret       string        `json:"secret"`
	Sample       *Sample       `json:"sample,omitempty"`
	ServiceState *ServiceState `json:"service_state,omitempty"`
}

type SyncResponse struct {
	DesiredAutostart  bool  `json:"desired_autostart"`
	MetricsIntervalMS int64 `json:"metrics_interval_ms"`
	TerminalEnabled   bool  `json:"terminal_enabled"`
}

func (c *EndpointClient) Sync(ctx context.Context, request SyncRequest) (*SyncResponse, error) {
	var response SyncResponse
	if err := c.doJSON(ctx, http.MethodPost, PathSync, request, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

type currentURLRequest struct {
	DeviceID string `json:"device_id"`
	Secret   string `json:"secret"`
	URL      string `json:"url"`
	Reason   string `json:"reason"`
}

func (c *EndpointClient) ReportCurrentURL(ctx context.Context, deviceID, secret, target, reason string) error {
	request := currentURLRequest{DeviceID: deviceID, Secret: secret, URL: target, Reason: reason}
	return c.doJSON(ctx, http.MethodPost, PathCurrentURL, request, nil)
}

func (c *EndpointClient) doJSON(ctx context.Context, method, path string, request, response any) error {
	var body io.Reader
	if request != nil {
		encoded, err := json.Marshal(request)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(encoded))
	}
	httpRequest, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if request != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpResponse, err := c.http.Do(httpRequest)
	if err != nil {
		return err
	}
	defer func() { _ = httpResponse.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxResponseBody))
	if err != nil {
		return err
	}
	if httpResponse.StatusCode != http.StatusOK {
		var failure struct {
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		serverError := &ServerError{Status: httpResponse.StatusCode, Message: strings.TrimSpace(string(payload))}
		if err := json.Unmarshal(payload, &failure); err == nil && failure.Error != nil {
			serverError.Code = failure.Error.Code
			serverError.Message = failure.Error.Message
		}
		return serverError
	}
	if response != nil {
		if err := json.Unmarshal(payload, response); err != nil {
			return fmt.Errorf("decode %s response: %w", path, err)
		}
	}
	return nil
}

func clampMetricsInterval(ms int64) int64 {
	if ms <= 0 {
		return defaultMetricsIntervalMS
	}
	if ms < minMetricsIntervalMS {
		return minMetricsIntervalMS
	}
	if ms > maxMetricsIntervalMS {
		return maxMetricsIntervalMS
	}
	return ms
}
