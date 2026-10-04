package terminal

const ScrollbackBytes = 32 * 1024 * 1024

const ringMinAlloc = 64 * 1024

type Ring struct {
	buf     []byte
	limit   int
	head    int
	size    int
	total   uint64
	dropped uint64
}

func NewRing(limit int) *Ring {
	if limit <= 0 {
		limit = ScrollbackBytes
	}
	return &Ring{limit: limit}
}

func (r *Ring) Len() int { return r.size }

func (r *Ring) Cap() int { return r.limit }

func (r *Ring) Total() uint64 { return r.total }

func (r *Ring) Dropped() uint64 { return r.dropped }

func (r *Ring) Base() uint64 { return r.total - uint64(r.size) }

func (r *Ring) Push(p []byte) {
	if len(p) == 0 {
		return
	}
	r.total += uint64(len(p))
	if len(p) >= r.limit {
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

func (r *Ring) append(p []byte) {
	idx := (r.head + r.size) % len(r.buf)
	n := copy(r.buf[idx:], p)
	copy(r.buf, p[n:])
	r.size += len(p)
}

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

func (r *Ring) Dump(max int) []byte {
	n := r.size
	if max > 0 && max < n {
		n = max
	}
	return r.copyAt(r.total-uint64(n), n)
}

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

func (r *Ring) Clear() {
	r.buf = nil
	r.head, r.size = 0, 0
}
