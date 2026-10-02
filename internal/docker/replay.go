package docker

type boundedReplay struct {
	chunks [][]byte
	size   int
	limit  int
}

func (b *boundedReplay) add(frame []byte) {
	if b.limit <= 0 || len(frame) == 0 {
		return
	}
	if len(frame) >= b.limit {
		b.chunks = [][]byte{append([]byte(nil), frame[len(frame)-b.limit:]...)}
		b.size = b.limit
		return
	}
	b.chunks = append(b.chunks, frame)
	b.size += len(frame)
	for b.size > b.limit && len(b.chunks) > 0 {
		excess := b.size - b.limit
		first := len(b.chunks[0])
		if excess >= first {
			b.chunks = b.chunks[1:]
			b.size -= first
			continue
		}
		b.chunks[0] = b.chunks[0][excess:]
		b.size -= excess
	}
}

func (b *boundedReplay) drain() [][]byte {
	chunks := b.chunks
	b.chunks = nil
	b.size = 0
	return chunks
}

func (b *boundedReplay) restore(chunks [][]byte) {
	b.chunks = chunks
	b.size = 0
	for _, chunk := range chunks {
		b.size += len(chunk)
	}
}
