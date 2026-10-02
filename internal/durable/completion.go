package durable

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
)

type completionFilter struct {
	marker   []byte
	pending  []byte
	ready    []byte
	complete bool
}

func newCompletionFilter(id string) *completionFilter {
	return &completionFilter{marker: completionMarker(id)}
}

func completionMarker(id string) []byte {
	return []byte("\x1b]73733;" + completionToken(id) + "\x07")
}

func completionPrintFormat(id string) string {
	return "\\033]73733;" + completionToken(id) + "\\007"
}

func completionToken(id string) string {
	digest := sha256.Sum256([]byte("nexterm-durable-completion-v1\x00" + id))
	return hex.EncodeToString(digest[:])
}

func (f *completionFilter) append(raw []byte) {
	if f.complete || len(raw) == 0 {
		return
	}
	f.pending = append(f.pending, raw...)
	if index := bytes.Index(f.pending, f.marker); index >= 0 {
		f.ready = append(f.ready, f.pending[:index]...)
		f.pending = nil
		f.complete = true
		return
	}
	held := markerSuffix(f.pending, f.marker)
	emit := len(f.pending) - held
	f.ready = append(f.ready, f.pending[:emit]...)
	f.pending = append(f.pending[:0], f.pending[emit:]...)
}

func (f *completionFilter) finish() {
	if f.complete {
		return
	}
	f.ready = append(f.ready, f.pending...)
	f.pending = nil
	f.complete = true
}

func (f *completionFilter) read(buffer []byte) (int, bool) {
	if len(f.ready) == 0 {
		return 0, f.complete
	}
	count := copy(buffer, f.ready)
	f.ready = f.ready[count:]
	return count, false
}

func markerSuffix(data, marker []byte) int {
	limit := len(data)
	if len(marker)-1 < limit {
		limit = len(marker) - 1
	}
	for length := limit; length > 0; length-- {
		if bytes.Equal(data[len(data)-length:], marker[:length]) {
			return length
		}
	}
	return 0
}
