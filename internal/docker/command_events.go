package docker

import (
	"context"
	"errors"
	"io"
	"sync"
)

type commandOutputMultiplexer struct {
	ctx      context.Context
	events   chan CommandOutputEvent
	sequence uint64
}

// newCommandOutputMultiplexer provides liveness and backpressure only; its sequence numbers are dequeue order, not transport arrival order. Transports that promise ordering must implement OrderedCommandOutput.
func newCommandOutputMultiplexer(ctx context.Context, stdout io.Reader, stderr io.Reader) *commandOutputMultiplexer {
	multiplexer := &commandOutputMultiplexer{ctx: ctx, events: make(chan CommandOutputEvent, 32)}
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

func (m *commandOutputMultiplexer) NextOutput(ctx context.Context) (CommandOutputEvent, error) {
	select {
	case event, ok := <-m.events:
		if !ok {
			return CommandOutputEvent{}, io.EOF
		}
		m.sequence++
		event.Sequence = m.sequence
		return event, nil
	case <-ctx.Done():
		return CommandOutputEvent{}, ctx.Err()
	case <-m.ctx.Done():
		return CommandOutputEvent{}, m.ctx.Err()
	}
}

func (m *commandOutputMultiplexer) read(readers *sync.WaitGroup, source io.Reader, stderr bool) {
	defer readers.Done()
	buffer := make([]byte, 32*1024)
	for {
		count, err := source.Read(buffer)
		if count > 0 {
			event := CommandOutputEvent{Data: append([]byte(nil), buffer[:count]...), Stderr: stderr}
			if !m.emit(event) {
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				m.emit(CommandOutputEvent{Stderr: stderr, Err: err})
			}
			return
		}
	}
}

func (m *commandOutputMultiplexer) emit(event CommandOutputEvent) bool {
	select {
	case m.events <- event:
		return true
	case <-m.ctx.Done():
		return false
	}
}
