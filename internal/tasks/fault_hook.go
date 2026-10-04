package tasks

import "sync"

type faultHook[F any] struct {
	mu sync.RWMutex
	fn F
}

func (h *faultHook[F]) get() F {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.fn
}

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
