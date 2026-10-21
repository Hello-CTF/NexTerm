package store

import "sync"

// transcriptOrderStatus records whether a transcript's chunk timestamps are
// known to be non-decreasing along the seq order — the invariant behind the
// seek's bounded block-max-TS index.
type transcriptOrderStatus int

const (
	transcriptOrderUnknown transcriptOrderStatus = iota
	transcriptOrderOrdered
	transcriptOrderDisordered
)

// transcriptOrderKnown tracks transcripts whose timestamp order was
// established either by an invariant-enforcing write (a first append, a
// synced insert, or a content replacement) or by a full validation scan, so
// later seeks can use the bounded binary-search index instead of the sound
// full-scan fallback. The cache is performance-only: correctness never
// depends on it, and a missing entry simply triggers validation. Bounded
// FIFO eviction keeps the footprint constant.
type transcriptOrderKnown struct {
	mu       sync.Mutex
	statuses map[string]transcriptOrderStatus
	fifo     []string
	capacity int
}

var transcriptOrder = transcriptOrderKnown{
	statuses: make(map[string]transcriptOrderStatus),
	capacity: 1024,
}

// transcriptOrderMark records a transcript's validated timestamp order,
// evicting the oldest entry when the cache is full.
func transcriptOrderMark(transcriptID string, status transcriptOrderStatus) {
	transcriptOrder.mu.Lock()
	defer transcriptOrder.mu.Unlock()
	if _, exists := transcriptOrder.statuses[transcriptID]; !exists {
		transcriptOrder.fifo = append(transcriptOrder.fifo, transcriptID)
		for len(transcriptOrder.fifo) > transcriptOrder.capacity {
			delete(transcriptOrder.statuses, transcriptOrder.fifo[0])
			transcriptOrder.fifo = transcriptOrder.fifo[1:]
		}
	}
	transcriptOrder.statuses[transcriptID] = status
}

// transcriptOrderStatusOf reports a transcript's cached timestamp order.
func transcriptOrderStatusOf(transcriptID string) (transcriptOrderStatus, bool) {
	transcriptOrder.mu.Lock()
	defer transcriptOrder.mu.Unlock()
	status, ok := transcriptOrder.statuses[transcriptID]
	return status, ok
}
