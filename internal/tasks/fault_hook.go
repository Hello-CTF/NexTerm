package tasks

import "sync"

// faultHook is a test-injection indirection whose target may be swapped
// while Manager or spool goroutines are using it. Reads and swaps are
// synchronized so a fault install or restore cannot race an in-flight use,
// which is what turned the release CI's go test -race red on the
// persist_test fault helpers.
type faultHook[F any] struct {
	mu sync.RWMutex
	fn F
}

// get returns the current hook target. The lock is released before the
// caller invokes the target, so a swap can never deadlock against it.
func (h *faultHook[F]) get() F {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.fn
}

// swap replaces the hook target and returns a restore function that puts
// the previous target back. Restoring twice is a harmless no-op.
func (h *faultHook[F]) swap(fn F) (restore func()) {
	h.mu.Lock()
	prev := h.fn
	h.fn = fn
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		h.fn = prev
		h.mu.Unlock()
	}
}
