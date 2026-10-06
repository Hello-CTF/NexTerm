package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	defaultProbeTimeout = 5 * time.Second
	defaultBaseBackoff  = 2 * time.Second
	defaultMaxBackoff   = time.Minute
)

// Prober selects the base URL the agent talks to. Endpoints keep their
// configured order; the current endpoint stays sticky while healthy, failed
// endpoints cool down with per-endpoint exponential backoff, and selection
// only happens at reconnect boundaries.
type Prober struct {
	clients []*EndpointClient

	probeTimeout time.Duration
	baseBackoff  time.Duration
	maxBackoff   time.Duration
	now          func() time.Time

	mu            sync.Mutex
	current       int
	cooldownUntil []time.Time
	backoff       []time.Duration
}

func NewProber(entries []BaseURLEntry) (*Prober, error) {
	clients := make([]*EndpointClient, 0, len(entries))
	for _, entry := range entries {
		client, err := NewEndpointClient(entry)
		if err != nil {
			return nil, err
		}
		clients = append(clients, client)
	}
	if len(clients) == 0 {
		return nil, errors.New("at least one base URL is required")
	}
	prober := &Prober{
		clients:      clients,
		probeTimeout: defaultProbeTimeout,
		baseBackoff:  defaultBaseBackoff,
		maxBackoff:   defaultMaxBackoff,
		now:          time.Now,
		current:      -1,
	}
	prober.cooldownUntil = make([]time.Time, len(clients))
	prober.backoff = make([]time.Duration, len(clients))
	for index := range prober.backoff {
		prober.backoff[index] = prober.baseBackoff
	}
	return prober, nil
}

func (p *Prober) probeTimeoutDuration() time.Duration {
	if p.probeTimeout <= 0 {
		return defaultProbeTimeout
	}
	return p.probeTimeout
}

// Current returns the sticky endpoint, or nil before the first selection.
func (p *Prober) Current() *EndpointClient {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current < 0 {
		return nil
	}
	return p.clients[p.current]
}

// NoEndpointError reports that no endpoint is currently usable and carries
// the earliest time a cooling-down endpoint may be probed again.
type NoEndpointError struct {
	RetryAfter time.Duration
}

func (e *NoEndpointError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%v: all endpoints cooling down, retry after %s", ErrNoEndpoint, e.RetryAfter.Round(time.Millisecond))
	}
	return ErrNoEndpoint.Error()
}

func (e *NoEndpointError) Unwrap() error { return ErrNoEndpoint }

// Select probes endpoints in configured order with the sticky current one
// first and returns the first healthy endpoint. Endpoints in cooldown are
// skipped until their cooldown expires; when every endpoint is cooling down
// or unhealthy it returns a *NoEndpointError carrying the retry hint.
func (p *Prober) Select(ctx context.Context) (*EndpointClient, error) {
	p.mu.Lock()
	order := make([]int, 0, len(p.clients))
	if p.current >= 0 {
		order = append(order, p.current)
	}
	for index := range p.clients {
		if index != p.current {
			order = append(order, index)
		}
	}
	now := p.now()
	var candidates []int
	for _, index := range order {
		if !p.cooldownUntil[index].IsZero() && now.Before(p.cooldownUntil[index]) {
			continue
		}
		candidates = append(candidates, index)
	}
	p.mu.Unlock()

	for _, index := range candidates {
		client := p.clients[index]
		probeCtx, cancel := context.WithTimeout(ctx, p.probeTimeoutDuration())
		err := client.Health(probeCtx)
		cancel()
		if err == nil {
			p.ReportSuccess(client)
			return client, nil
		}
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		p.ReportFailure(client)
	}
	p.mu.Lock()
	now = p.now()
	var earliest time.Time
	for _, until := range p.cooldownUntil {
		if !until.IsZero() && now.Before(until) && (earliest.IsZero() || until.Before(earliest)) {
			earliest = until
		}
	}
	p.mu.Unlock()
	if !earliest.IsZero() {
		return nil, &NoEndpointError{RetryAfter: earliest.Sub(now)}
	}
	return nil, &NoEndpointError{}
}

func (p *Prober) indexOf(client *EndpointClient) int {
	for index, candidate := range p.clients {
		if candidate == client {
			return index
		}
	}
	return -1
}

// ReportSuccess marks the endpoint healthy, clears its cooldown and makes it
// the sticky current endpoint.
func (p *Prober) ReportSuccess(client *EndpointClient) {
	p.mu.Lock()
	defer p.mu.Unlock()
	index := p.indexOf(client)
	if index < 0 {
		return
	}
	p.current = index
	p.cooldownUntil[index] = time.Time{}
	p.backoff[index] = p.baseBackoff
}

// ReportFailure puts the endpoint into cooldown and doubles its backoff up to
// the cap. The sticky current endpoint is only dropped once another endpoint
// wins a later Select.
func (p *Prober) ReportFailure(client *EndpointClient) {
	p.mu.Lock()
	defer p.mu.Unlock()
	index := p.indexOf(client)
	if index < 0 {
		return
	}
	p.cooldownUntil[index] = p.now().Add(p.backoff[index])
	next := p.backoff[index] * 2
	if next > p.maxBackoff || next <= 0 {
		next = p.maxBackoff
	}
	p.backoff[index] = next
}
