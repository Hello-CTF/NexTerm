package docker

import "errors"

// errReplayFull signals that the detached replay buffer is full: the read
// loop stalls until a resume drains it instead of dropping exec output.
var errReplayFull = errors.New("docker exec replay buffer full")

// boundedReplay accumulates frames produced while an exec stream has no sink.
// It never drops bytes: when the buffer is full, producers block until a
// resume drains it, which applies backpressure to the exec process instead of
// truncating its output.
type boundedReplay struct {
	chunks    [][]byte
	size      int
	limit     int
	roomAvail chan struct{}
}

func newBoundedReplay(limit int) *boundedReplay {
	return &boundedReplay{limit: limit, roomAvail: make(chan struct{})}
}

func (b *boundedReplay) full() bool {
	return b.limit > 0 && b.size >= b.limit
}

// addForced retains a frame regardless of the limit: a frame that failed to
// send must be redelivered, so it is kept even when the buffer is full. The
// dispatch loop only calls it when the buffer has room, or for a failed send.
func (b *boundedReplay) addForced(frame []byte) {
	if b.limit <= 0 || len(frame) == 0 {
		return
	}
	b.chunks = append(b.chunks, frame)
	b.size += len(frame)
}

func (b *boundedReplay) drain() [][]byte {
	chunks := b.chunks
	b.chunks = nil
	b.size = 0
	close(b.roomAvail)
	b.roomAvail = make(chan struct{})
	return chunks
}

func (b *boundedReplay) restore(chunks [][]byte) {
	b.chunks = chunks
	b.size = 0
	for _, chunk := range chunks {
		b.size += len(chunk)
	}
}
