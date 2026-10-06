package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type switchableEndpoint struct {
	server  *httptest.Server
	healthy atomic.Bool
	hits    atomic.Int64
}

func newSwitchableEndpoint(t *testing.T) *switchableEndpoint {
	t.Helper()
	endpoint := &switchableEndpoint{}
	endpoint.healthy.Store(true)
	endpoint.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint.hits.Add(1)
		if !endpoint.healthy.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "service": "nexterm-server"})
	}))
	t.Cleanup(endpoint.server.Close)
	return endpoint
}

func newTestProber(t *testing.T, endpoints ...*switchableEndpoint) (*Prober, *time.Time) {
	t.Helper()
	entries := make([]BaseURLEntry, 0, len(endpoints))
	for _, endpoint := range endpoints {
		entries = append(entries, BaseURLEntry{URL: endpoint.server.URL})
	}
	prober, err := NewProber(entries)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	prober.now = func() time.Time { return clock }
	prober.baseBackoff = time.Second
	prober.maxBackoff = 30 * time.Second
	for index := range prober.backoff {
		prober.backoff[index] = prober.baseBackoff
	}
	return prober, &clock
}

func selectOnce(t *testing.T, prober *Prober) *EndpointClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := prober.Select(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestProberKeepsConfiguredOrderAndStickyCurrent(t *testing.T) {
	first := newSwitchableEndpoint(t)
	second := newSwitchableEndpoint(t)
	prober, _ := newTestProber(t, first, second)

	client := selectOnce(t, prober)
	if client.BaseURL() != first.server.URL {
		t.Fatalf("first selection = %s, want %s", client.BaseURL(), first.server.URL)
	}
	again := selectOnce(t, prober)
	if again != client {
		t.Fatal("healthy current endpoint must stay sticky")
	}
	if second.hits.Load() != 0 {
		t.Fatal("healthy current endpoint must be probed alone, not both endpoints")
	}
}

func TestProberFailsOverAtReconnectBoundary(t *testing.T) {
	first := newSwitchableEndpoint(t)
	second := newSwitchableEndpoint(t)
	prober, clock := newTestProber(t, first, second)

	if client := selectOnce(t, prober); client.BaseURL() != first.server.URL {
		t.Fatalf("initial selection = %s", client.BaseURL())
	}
	first.healthy.Store(false)
	client := selectOnce(t, prober)
	if client.BaseURL() != second.server.URL {
		t.Fatalf("failover selection = %s, want %s", client.BaseURL(), second.server.URL)
	}
	if prober.Current() != client {
		t.Fatal("current must follow the failover target")
	}

	first.healthy.Store(true)
	immediate := selectOnce(t, prober)
	if immediate.BaseURL() != second.server.URL {
		t.Fatal("endpoint in cooldown must not win selection even when healthy again")
	}
	*clock = clock.Add(31 * time.Second)
	sticky := selectOnce(t, prober)
	if sticky.BaseURL() != second.server.URL {
		t.Fatalf("healthy failover target must stay sticky, got %s", sticky.BaseURL())
	}
	second.healthy.Store(false)
	back := selectOnce(t, prober)
	if back.BaseURL() != first.server.URL {
		t.Fatalf("selection after second endpoint failure = %s, want %s", back.BaseURL(), first.server.URL)
	}
}

func TestProberBackoffGrowsPerEndpointAndResets(t *testing.T) {
	endpoint := newSwitchableEndpoint(t)
	endpoint.healthy.Store(false)
	prober, clock := newTestProber(t, endpoint)

	for _, expected := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := prober.Select(ctx)
		cancel()
		var noEndpoint *NoEndpointError
		if !errors.As(err, &noEndpoint) {
			t.Fatalf("Select = %v, want *NoEndpointError", err)
		}
		if noEndpoint.RetryAfter != expected {
			t.Fatalf("retry after = %s, want %s", noEndpoint.RetryAfter, expected)
		}
		*clock = clock.Add(expected)
	}
	endpoint.healthy.Store(true)
	if client := selectOnce(t, prober); client.BaseURL() != endpoint.server.URL {
		t.Fatal("recovered endpoint must become selectable")
	}
	prober.mu.Lock()
	backoff := prober.backoff[0]
	prober.mu.Unlock()
	if backoff != time.Second {
		t.Fatalf("backoff after success = %s, want reset to base", backoff)
	}
}

func TestProberNoEndpointWhenAllCoolingDown(t *testing.T) {
	first := newSwitchableEndpoint(t)
	second := newSwitchableEndpoint(t)
	prober, clock := newTestProber(t, first, second)
	first.healthy.Store(false)
	second.healthy.Store(false)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, err := prober.Select(ctx)
	cancel()
	var noEndpoint *NoEndpointError
	if !errors.As(err, &noEndpoint) {
		t.Fatalf("Select = %v, want *NoEndpointError", err)
	}
	if !errors.Is(err, ErrNoEndpoint) {
		t.Fatal("NoEndpointError must unwrap to ErrNoEndpoint")
	}
	if noEndpoint.RetryAfter <= 0 {
		t.Fatalf("retry hint = %s, want positive", noEndpoint.RetryAfter)
	}
	*clock = clock.Add(noEndpoint.RetryAfter)
	first.healthy.Store(true)
	if client := selectOnce(t, prober); client.BaseURL() != first.server.URL {
		t.Fatal("first endpoint in order must win once its cooldown expires")
	}
}
