package session

import "sync"

// recoveryRingBytes bounds the retained output per ephemeral tab. It must
// comfortably exceed a saturated client's hard cap plus the in-flight
// pipeline (hub queue 16 MiB + fanout queue 4 MiB + coalescer 64 KiB), so a
// refused suffix can be redelivered whole even when the pause is delayed.
const recoveryRingBytes = 64 << 20

// recoveryRing is a bounded buffer of a tab's recent output frames with
// cumulative offsets. It lets the server redeliver a refused suffix to a
// saturated channel without dropping bytes: the client reports how many bytes
// it accepted, and the ring replays everything produced after that point.
//
// The floor is the oldest offset that must be retained: while a subscriber
// has an unresolved refusal episode, bytes at or after its accepted offset
// must not be evicted — eviction stops at the floor and the pump stalls
// instead, applying real backpressure rather than losing the suffix.
type recoveryRing struct {
	mu     sync.Mutex
	chunks []recoveryChunk
	size   int
	limit  int
	offset int64
	floor  int64
}

type recoveryChunk struct {
	offset int64
	data   []byte
}

func newRecoveryRing(limit int) *recoveryRing {
	return &recoveryRing{limit: limit, floor: -1}
}

// setFloor sets the oldest offset that must be retained, or -1 for no floor.
func (r *recoveryRing) setFloor(offset int64) {
	r.mu.Lock()
	r.floor = offset
	r.mu.Unlock()
}

// add appends an output chunk, evicting the oldest chunks past the limit but
// never at or above the floor.
func (r *recoveryRing) add(data []byte) {
	if len(data) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chunks = append(r.chunks, recoveryChunk{offset: r.offset, data: data})
	r.offset += int64(len(data))
	r.size += len(data)
	for r.size > r.limit && len(r.chunks) > 0 && r.floorEvictableLocked() {
		first := len(r.chunks[0].data)
		r.chunks = r.chunks[1:]
		r.size -= first
	}
}

// floorEvictableLocked reports whether the oldest chunk may be evicted: whole
// chunks ending at or below the floor are safe to drop.
func (r *recoveryRing) floorEvictableLocked() bool {
	if r.floor < 0 {
		return true
	}
	return len(r.chunks) > 0 && r.chunks[0].offset+int64(len(r.chunks[0].data)) <= r.floor
}

// needsStall reports whether the ring is at capacity with the floor blocking
// eviction: the producer must stall instead of dropping retained bytes.
func (r *recoveryRing) needsStall() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size < r.limit || r.floor < 0 {
		return false
	}
	return !r.floorEvictableLocked()
}

// currentOffset returns the offset that the next chunk will carry.
func (r *recoveryRing) currentOffset() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.offset
}

// oldestOffset returns the offset of the oldest retained byte, or -1 when the
// ring is empty.
func (r *recoveryRing) oldestOffset() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.chunks) == 0 {
		return -1
	}
	return r.chunks[0].offset
}

// replayRange returns the retained bytes in [offset, end). A start earlier
// than the retention window reads only from the oldest retained byte; callers
// compare against oldestOffset first when a gap must be reported instead.
func (r *recoveryRing) replayRange(offset, end int64) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []byte
	for _, chunk := range r.chunks {
		chunkEnd := chunk.offset + int64(len(chunk.data))
		if chunkEnd <= offset {
			continue
		}
		if chunk.offset >= end {
			break
		}
		start := int64(0)
		if chunk.offset < offset {
			start = offset - chunk.offset
		}
		stop := int64(len(chunk.data))
		if chunkEnd > end {
			stop = end - chunk.offset
		}
		out = append(out, chunk.data[start:stop]...)
	}
	return out
}
