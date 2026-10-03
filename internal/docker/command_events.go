package docker

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type commandOutputMultiplexer struct {
	ctx      context.Context
	events   chan base.OutputEvent
	sequence uint64
}

// newCommandOutputMultiplexer provides liveness and backpressure only; its
// sequence numbers are dequeue order, not transport arrival order. Transports
// that promise ordering must implement base.OrderedOutput.
func newCommandOutputMultiplexer(ctx context.Context, stdout io.Reader, stderr io.Reader) *commandOutputMultiplexer {
	multiplexer := &commandOutputMultiplexer{ctx: ctx, events: make(chan base.OutputEvent, 32)}
	var readers sync.WaitGroup
	readers.Add(2)
	go multiplexer.read(&readers, stdout, false)
	go multiplexer.read(&readers, stderr, true)
	go func() {
		readers.Wait()
		close(multiplexer.events)
	}()
	return multiplexer
}

func (m *commandOutputMultiplexer) NextOutput(ctx context.Context) (base.OutputEvent, error) {
	select {
	case event, ok := <-m.events:
		if !ok {
			return base.OutputEvent{}, io.EOF
		}
		m.sequence++
		event.Sequence = m.sequence
		return event, nil
	case <-ctx.Done():
		return base.OutputEvent{}, ctx.Err()
	case <-m.ctx.Done():
		return base.OutputEvent{}, m.ctx.Err()
	}
}

func (m *commandOutputMultiplexer) read(readers *sync.WaitGroup, source io.Reader, stderr bool) {
	defer readers.Done()
	buffer := make([]byte, 32*1024)
	for {
		count, err := source.Read(buffer)
		if count > 0 {
			event := base.OutputEvent{Data: append([]byte(nil), buffer[:count]...), Stderr: stderr}
			if !m.emit(event) {
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				m.emit(base.OutputEvent{Stderr: stderr, Err: err})
			}
			return
		}
	}
}

func (m *commandOutputMultiplexer) emit(event base.OutputEvent) bool {
	select {
	case m.events <- event:
		return true
	case <-m.ctx.Done():
		return false
	}
}
