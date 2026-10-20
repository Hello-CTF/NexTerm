package session

import "sync"

// recoveryRingBytes bounds the retained output per ephemeral tab. It must
// comfortably exceed a saturated client's hard cap so a refused suffix can be
// redelivered whole.
const recoveryRingBytes = 32 << 20

// recoveryRing is a bounded buffer of a tab's recent output frames with
// cumulative offsets. It lets the server redeliver a refused suffix to a
// saturated channel without dropping bytes: the client reports how many bytes
// it accepted, and the ring replays everything produced after that point.
type recoveryRing struct {
	mu     sync.Mutex
	chunks []recoveryChunk
	size   int
	limit  int
	offset int64
}

type recoveryChunk struct {
	offset int64
	data   []byte
}

func newRecoveryRing(limit int) *recoveryRing {
	return &recoveryRing{limit: limit}
}

// add appends an output chunk, evicting the oldest chunks past the limit.
func (r *recoveryRing) add(data []byte) {
	if len(data) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chunks = append(r.chunks, recoveryChunk{offset: r.offset, data: data})
	r.offset += int64(len(data))
	r.size += len(data)
	for r.size > r.limit && len(r.chunks) > 0 {
		first := len(r.chunks[0].data)
		r.chunks = r.chunks[1:]
		r.size -= first
	}
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
