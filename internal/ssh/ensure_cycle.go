package ssh

import (
	"context"
	"sync"
)

// cycleCache memoizes read-only remote probes within one ensure cycle so the
// same PATH lookup or helper probe is not repeated for every candidate location.
// It lives only as long as the cycle's context, never across retries or calls.
type cycleCache struct {
	mu       sync.Mutex
	path     map[string][]string
	probes   map[string]error
	disabled bool
}

type cycleCacheContextKey struct{}

func withCycleCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, cycleCacheContextKey{}, &cycleCache{path: map[string][]string{}, probes: map[string]error{}})
}

// cycleCacheFrom returns a cache that remembers nothing when ctx carries none.
func cycleCacheFrom(ctx context.Context) *cycleCache {
	if cache, ok := ctx.Value(cycleCacheContextKey{}).(*cycleCache); ok {
		return cache
	}
	return &cycleCache{disabled: true}
}

func (c *cycleCache) pathBinaries(goos string) ([]string, bool) {
	if c.disabled {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	binaries, ok := c.path[goos]
	return append([]string(nil), binaries...), ok
}

func (c *cycleCache) storePathBinaries(goos string, binaries []string) {
	if c.disabled {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.path[goos] = append([]string(nil), binaries...)
}

func (c *cycleCache) probeResult(binary string) (error, bool) {
	if c.disabled {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	err, ok := c.probes[binary]
	return err, ok
}

func (c *cycleCache) storeProbe(binary string, err error) {
	if c.disabled {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.probes[binary] = err
}

func (c *cycleCache) forgetProbe(binary string) {
	if c.disabled || binary == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.probes, binary)
}
