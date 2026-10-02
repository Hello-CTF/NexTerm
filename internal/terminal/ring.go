package terminal

// ScrollbackBytes is the default raw-byte ring capacity (32 MiB).
const ScrollbackBytes = 32 * 1024 * 1024

const ringMinAlloc = 64 * 1024

// Ring is a byte-oriented scrollback buffer. Bytes are stored exactly as
// received (never decoded), so multibyte characters are never silently
// dropped at chunk boundaries; consumers decode incrementally.
//
// Every byte ever pushed has an absolute sequence number in [0, Total).
// Retained bytes span [Base, Total); older bytes are counted by Dropped.
//
// Ring is not safe for concurrent use; Tab guards it with its own mutex.
type Ring struct {
	buf     []byte // storage, len(buf) <= limit, grows geometrically
	limit   int
	head    int // index of the oldest retained byte
	size    int
	total   uint64
	dropped uint64
}

// NewRing creates a ring retaining at most limit bytes. A non-positive
// limit selects ScrollbackBytes.
func NewRing(limit int) *Ring {
	if limit <= 0 {
		limit = ScrollbackBytes
	}
	return &Ring{limit: limit}
}

// Len returns the number of retained bytes.
func (r *Ring) Len() int { return r.size }

// Cap returns the retention limit in bytes.
func (r *Ring) Cap() int { return r.limit }

// Total returns the total number of bytes ever pushed (the sequence number
// of the next byte).
func (r *Ring) Total() uint64 { return r.total }

// Dropped returns the number of bytes discarded because of the capacity
// limit (including bytes skipped when a single push exceeds the limit).
func (r *Ring) Dropped() uint64 { return r.dropped }

// Base returns the sequence number of the oldest retained byte.
func (r *Ring) Base() uint64 { return r.total - uint64(r.size) }

// Push appends raw bytes, discarding the oldest bytes on overflow.
func (r *Ring) Push(p []byte) {
	if len(p) == 0 {
		return
	}
	r.total += uint64(len(p))
	if len(p) >= r.limit {
		// The whole retained history and the skipped head of p are gone.
		r.dropped += uint64(r.size) + uint64(len(p)-r.limit)
		r.head, r.size = 0, 0
		r.grow(r.limit)
		r.append(p[len(p)-r.limit:])
		return
	}
	if overflow := r.size + len(p) - r.limit; overflow > 0 {
		r.head = (r.head + overflow) % len(r.buf)
		r.size -= overflow
		r.dropped += uint64(overflow)
	}
	r.grow(r.size + len(p))
	r.append(p)
}

// grow ensures len(buf) >= need, linearizing the ring when reallocating.
func (r *Ring) grow(need int) {
	if need <= len(r.buf) {
		return
	}
	n := len(r.buf) * 2
	if n < ringMinAlloc {
		n = ringMinAlloc
	}
	if n < need {
		n = need
	}
	if n > r.limit {
		n = r.limit
	}
	buf := make([]byte, n)
	first := min(r.size, len(r.buf)-r.head)
	copy(buf, r.buf[r.head:r.head+first])
	copy(buf[first:], r.buf[:r.size-first])
	r.buf = buf
	r.head = 0
}

// append writes p at the tail; space must already be available.
func (r *Ring) append(p []byte) {
	idx := (r.head + r.size) % len(r.buf)
	n := copy(r.buf[idx:], p)
	copy(r.buf, p[n:])
	r.size += len(p)
}

// copyAt copies n retained bytes starting at absolute sequence seq into a
// fresh slice. Callers must clamp seq and n to the retained range.
func (r *Ring) copyAt(seq uint64, n int) []byte {
	out := make([]byte, n)
	if n == 0 {
		return out
	}
	off := int(seq - r.Base())
	start := (r.head + off) % len(r.buf)
	copied := copy(out, r.buf[start:min(start+n, len(r.buf))])
	copy(out[copied:], r.buf[:n-copied])
	return out
}

// Dump returns the most recent max bytes (all retained bytes when max <= 0).
func (r *Ring) Dump(max int) []byte {
	n := r.size
	if max > 0 && max < n {
		n = max
	}
	return r.copyAt(r.total-uint64(n), n)
}

// ReplayFrom returns up to max bytes starting at absolute sequence seq
// (all available bytes when max <= 0). start is clamped to [Base, Total]:
// when the requested position has already been evicted, replay resumes at
// the oldest retained byte and reports that position, so reconnecting
// clients can continue without gaps or duplicates in what they receive.
func (r *Ring) ReplayFrom(seq uint64, max int) (data []byte, start uint64) {
	start = seq
	if start < r.Base() {
		start = r.Base()
	}
	if start > r.total {
		start = r.total
	}
	n := int(r.total - start)
	if max > 0 && n > max {
		n = max
	}
	return r.copyAt(start, n), start
}

// Snapshot returns all retained bytes.
func (r *Ring) Snapshot() []byte { return r.Dump(0) }

// Clear empties the ring. Total and Dropped keep counting; Base moves to
// the current Total.
func (r *Ring) Clear() {
	r.buf = nil
	r.head, r.size = 0, 0
}
