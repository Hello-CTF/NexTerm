package metrics

import (
	"math"
	"sort"
	"sync/atomic"
)

// DefaultDurationBuckets are request duration upper bounds in seconds.
var DefaultDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Histogram is a lock-free fixed-bucket histogram.
type Histogram struct {
	bounds  []float64
	buckets []atomic.Uint64
	count   atomic.Uint64
	sumBits atomic.Uint64
}

// HistogramSnapshot holds cumulative bucket counts aligned with Bounds.
type HistogramSnapshot struct {
	Bounds     []float64
	Cumulative []uint64
	Count      uint64
	Sum        float64
}

func NewHistogram(bounds []float64) *Histogram {
	sorted := append([]float64(nil), bounds...)
	sort.Float64s(sorted)
	return &Histogram{bounds: sorted, buckets: make([]atomic.Uint64, len(sorted))}
}

func (h *Histogram) Observe(value float64) {
	if index := sort.SearchFloat64s(h.bounds, value); index < len(h.bounds) {
		h.buckets[index].Add(1)
	}
	for {
		old := h.sumBits.Load()
		if h.sumBits.CompareAndSwap(old, math.Float64bits(math.Float64frombits(old)+value)) {
			break
		}
	}
	h.count.Add(1)
}

// Snapshot reads the histogram. Concurrent observations may make the
// snapshot lag by a few samples, which the format tolerates.
func (h *Histogram) Snapshot() HistogramSnapshot {
	snapshot := HistogramSnapshot{Bounds: h.bounds, Cumulative: make([]uint64, len(h.bounds))}
	var running uint64
	for index := range h.buckets {
		running += h.buckets[index].Load()
		snapshot.Cumulative[index] = running
	}
	snapshot.Count = max(h.count.Load(), running)
	snapshot.Sum = math.Float64frombits(h.sumBits.Load())
	return snapshot
}
