package terminal

import (
	"bytes"
	"context"
	"sync"
)

type Sink interface {
	SendBinary(ctx context.Context, data []byte) error
}

type SinkFunc func(ctx context.Context, data []byte) error

func (f SinkFunc) SendBinary(ctx context.Context, data []byte) error { return f(ctx, data) }

type fanoutEntry struct {
	sink Sink
	gen  uint64
}

type Fanout struct {
	mu     sync.Mutex
	sendMu sync.Mutex
	next   uint64
	sinks  map[string]fanoutEntry
}

func NewFanout() *Fanout {
	return &Fanout{sinks: make(map[string]fanoutEntry)}
}

func (f *Fanout) Attach(id string, sink Sink) (replaced bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	_, replaced = f.sinks[id]
	f.sinks[id] = fanoutEntry{sink: sink, gen: f.next}
	return replaced
}

func (f *Fanout) Detach(id string) (removed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sinks[id]; ok {
		delete(f.sinks, id)
		return true
	}
	return false
}

func (f *Fanout) DetachAll() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.sinks)
	f.sinks = make(map[string]fanoutEntry)
	return n
}

func (f *Fanout) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sinks)
}

func (f *Fanout) detachIf(id string, gen uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if entry, ok := f.sinks[id]; ok && entry.gen == gen {
		delete(f.sinks, id)
	}
}

func (f *Fanout) Send(ctx context.Context, data []byte) bool {
	f.sendMu.Lock()
	defer f.sendMu.Unlock()

	f.mu.Lock()
	if len(f.sinks) == 0 {
		f.mu.Unlock()
		return false
	}
	type target struct {
		id   string
		gen  uint64
		sink Sink
	}
	targets := make([]target, 0, len(f.sinks))
	for id, entry := range f.sinks {
		targets = append(targets, target{id: id, gen: entry.gen, sink: entry.sink})
	}
	f.mu.Unlock()

	delivered := false
	for _, tgt := range targets {
		if ctx.Err() != nil {
			f.detachIf(tgt.id, tgt.gen)
			continue
		}
		payload := data
		if delivered {
			payload = bytes.Clone(data)
		}
		if err := tgt.sink.SendBinary(ctx, payload); err != nil {
			f.detachIf(tgt.id, tgt.gen)
			continue
		}
		delivered = true
	}
	return delivered
}
