package rstream

// ring holds the unacknowledged send bytes [acked, written) of a stream. It grows on demand up to the stream
// window and drops its storage once drained, so an idle stream keeps no buffer.
type ring struct {
	buf  []byte
	head int
	size int
}

const ringMinCapacity = 16 * 1024

func (r *ring) write(p []byte, limit int) {
	if need := r.size + len(p); need > len(r.buf) {
		capacity := max(2*len(r.buf), ringMinCapacity, need)
		capacity = min(capacity, max(limit, need))
		grown := make([]byte, capacity)
		r.copyAt(grown[:r.size], 0)
		r.buf, r.head = grown, 0
	}
	tail := (r.head + r.size) % len(r.buf)
	n := copy(r.buf[tail:], p)
	copy(r.buf, p[n:])
	r.size += len(p)
}

// copyAt copies len(dst) bytes starting at logical index at.
func (r *ring) copyAt(dst []byte, at int) {
	if len(dst) == 0 {
		return
	}
	start := (r.head + at) % len(r.buf)
	n := copy(dst, r.buf[start:])
	copy(dst[n:], r.buf)
}

func (r *ring) discard(n int) {
	r.size -= n
	if r.size == 0 {
		r.buf, r.head = nil, 0
		return
	}
	r.head = (r.head + n) % len(r.buf)
}
