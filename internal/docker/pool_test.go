package docker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGenerationalProviderClosesOnlyMatchingGeneration(t *testing.T) {
	var generation atomic.Uint64
	generation.Store(1)
	var created []uint64
	var mu sync.Mutex
	factory := func(_ context.Context, _ string, value uint64) (Backend, error) {
		mu.Lock()
		created = append(created, value)
		mu.Unlock()
		return &stubBackend{}, nil
	}
	provider := NewGenerationalProvider(GenerationSourceFunc(func(string) (uint64, error) {
		return generation.Load(), nil
	}), factory, nil)
	first, err := provider.SDK(t.Context(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := provider.SDK(t.Context(), "s1")
	if err != nil || again != first {
		t.Fatalf("same generation was not reused: %v", err)
	}
	generation.Store(2)
	second, err := provider.SDK(t.Context(), "s1")
	if err != nil || second == first {
		t.Fatalf("new generation was not created: %v", err)
	}
	if first.(*stubBackend).closed.Load() != 1 {
		t.Fatal("old backend was not closed")
	}
	if err := provider.CloseGeneration("s1", 1); err != nil {
		t.Fatal(err)
	}
	if second.(*stubBackend).closed.Load() != 0 {
		t.Fatal("stale cleanup closed current backend")
	}
	if err := provider.CloseGeneration("s1", 2); err != nil {
		t.Fatal(err)
	}
	if second.(*stubBackend).closed.Load() != 1 {
		t.Fatal("matching backend was not closed")
	}
}

func TestGenerationalProviderDoesNotReturnStaleFactoryResult(t *testing.T) {
	var generation atomic.Uint64
	generation.Store(1)
	var calls atomic.Int32
	stale := &stubBackend{}
	current := &stubBackend{}
	factory := func(_ context.Context, _ string, value uint64) (Backend, error) {
		if calls.Add(1) == 1 {
			generation.Store(2)
			return stale, nil
		}
		return current, nil
	}
	provider := NewGenerationalProvider(GenerationSourceFunc(func(string) (uint64, error) {
		return generation.Load(), nil
	}), factory, nil)
	backend, err := provider.SDK(t.Context(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if backend != current || stale.closed.Load() != 1 || calls.Load() != 2 {
		t.Fatalf("stale result leaked: backend=%p stale closes=%d calls=%d", backend, stale.closed.Load(), calls.Load())
	}
}

func TestGenerationalProviderSingleFlight(t *testing.T) {
	var calls atomic.Int32
	factory := func(context.Context, string, uint64) (Backend, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return &stubBackend{}, nil
	}
	provider := NewGenerationalProvider(GenerationSourceFunc(func(string) (uint64, error) { return 1, nil }), factory, nil)
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := provider.SDK(context.Background(), "s1"); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	if calls.Load() != 1 {
		t.Fatalf("factory calls = %d", calls.Load())
	}
}
