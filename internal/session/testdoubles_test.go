package session

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type fakeConnector struct {
	mu         sync.Mutex
	calls      int
	transports []*fakeTransport
	connect    func(context.Context, Asset, uint64, int) (base.Transport, error)
}

func newFakeConnector() *fakeConnector {
	return &fakeConnector{}
}

func (c *fakeConnector) Connect(ctx context.Context, asset Asset, generation uint64) (base.Transport, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	hook := c.connect
	c.mu.Unlock()
	var transport base.Transport
	var err error
	if hook != nil {
		transport, err = hook(ctx, asset, generation, call)
	} else {
		transport = newFakeTransport(asset.Kind, uint64(call)+1000)
	}
	if transport != nil {
		c.mu.Lock()
		c.transports = append(c.transports, transport.(*fakeTransport))
		c.mu.Unlock()
	}
	return transport, err
}

func (c *fakeConnector) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *fakeConnector) transport(index int) *fakeTransport {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transports[index]
}

type fakeTransport struct {
	kind       string
	generation uint64
	alive      atomic.Bool
	closeCount atomic.Int32

	mu       sync.Mutex
	channels []*fakeChannel
	options  []base.PTYOptions
	execs    []string
}

func newFakeTransport(kind string, generation uint64) *fakeTransport {
	transport := &fakeTransport{kind: kind, generation: generation}
	transport.alive.Store(true)
	return transport
}

func (t *fakeTransport) Kind() string       { return t.kind }
func (t *fakeTransport) Generation() uint64 { return t.generation }
func (t *fakeTransport) IsAlive() bool      { return t.alive.Load() }
func (t *fakeTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}

func (t *fakeTransport) Close() error {
	if !t.alive.Swap(false) {
		return nil
	}
	t.closeCount.Add(1)
	t.mu.Lock()
	channels := append([]*fakeChannel(nil), t.channels...)
	t.mu.Unlock()
	for _, channel := range channels {
		_ = channel.Close()
	}
	return nil
}

func (t *fakeTransport) OpenPTY(_ context.Context, options base.PTYOptions) (base.Channel, error) {
	if !t.alive.Load() {
		return nil, base.ErrClosed
	}
	if options.ExpectedGeneration != 0 && options.ExpectedGeneration != t.generation {
		return nil, base.ErrStaleGeneration
	}
	channel := newFakeChannel(t.generation)
	t.mu.Lock()
	t.channels = append(t.channels, channel)
	t.options = append(t.options, options)
	t.mu.Unlock()
	return channel, nil
}

func (t *fakeTransport) Exec(_ context.Context, command string, options base.ExecOptions) (base.ExecResult, error) {
	if !t.alive.Load() {
		return base.ExecResult{}, base.ErrClosed
	}
	if options.ExpectedGeneration != 0 && options.ExpectedGeneration != t.generation {
		return base.ExecResult{}, base.ErrStaleGeneration
	}
	t.mu.Lock()
	t.execs = append(t.execs, command)
	t.mu.Unlock()
	return base.ExecResult{Stdout: "ran:" + command, ExitCode: new(int)}, nil
}

func (t *fakeTransport) channel(index int) *fakeChannel {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.channels[index]
}

func (t *fakeTransport) channelCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.channels)
}

type fakeChannel struct {
	generation uint64
	id         string
	reads      chan []byte
	closed     chan struct{}
	closeOnce  sync.Once
	closeCount atomic.Int32

	mu         sync.Mutex
	pending    []byte
	input      []byte
	resizes    int
	cols       uint32
	rows       uint32
	waitErr    error
	closeWrite int
	resizeHook func(context.Context, uint32, uint32) error
}

func newFakeChannel(generation uint64) *fakeChannel {
	return &fakeChannel{generation: generation, id: newID(), reads: make(chan []byte, 256), closed: make(chan struct{})}
}

func (c *fakeChannel) Read(buffer []byte) (int, error) {
	c.mu.Lock()
	if len(c.pending) > 0 {
		count := copy(buffer, c.pending)
		c.pending = c.pending[count:]
		c.mu.Unlock()
		return count, nil
	}
	c.mu.Unlock()
	select {
	case data := <-c.reads:
		count := copy(buffer, data)
		if count < len(data) {
			c.mu.Lock()
			c.pending = append(c.pending, data[count:]...)
			c.mu.Unlock()
		}
		return count, nil
	case <-c.closed:
		return 0, io.EOF
	}
}

func (c *fakeChannel) Write(data []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, base.ErrClosed
	default:
	}
	c.mu.Lock()
	c.input = append(c.input, data...)
	c.mu.Unlock()
	return len(data), nil
}

func (c *fakeChannel) Close() error {
	c.closeOnce.Do(func() {
		c.closeCount.Add(1)
		close(c.closed)
	})
	return nil
}

func (c *fakeChannel) Stderr() io.Reader { return nil }

func (c *fakeChannel) Resize(ctx context.Context, cols, rows uint32) error {
	if c.resizeHook != nil {
		if err := c.resizeHook(ctx, cols, rows); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.resizes++
	c.cols, c.rows = cols, rows
	c.mu.Unlock()
	return nil
}

func (c *fakeChannel) Wait(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waitErr
}

func (c *fakeChannel) CloseWrite() error {
	c.mu.Lock()
	c.closeWrite++
	c.mu.Unlock()
	return nil
}

func (c *fakeChannel) ID() string         { return c.id }
func (c *fakeChannel) Generation() uint64 { return c.generation }

func (c *fakeChannel) emit(data []byte) error {
	chunk := append([]byte(nil), data...)
	select {
	case c.reads <- chunk:
		return nil
	case <-c.closed:
		return base.ErrClosed
	case <-time.After(time.Second):
		return errors.New("fake channel emit timed out")
	}
}

func (c *fakeChannel) written() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.input...)
}

type fakeTerminalFactory struct {
	mu        sync.Mutex
	terminals map[string]*fakeTerminal
}

func newFakeTerminalFactory() *fakeTerminalFactory {
	return &fakeTerminalFactory{terminals: make(map[string]*fakeTerminal)}
}

func (f *fakeTerminalFactory) NewTerminal(config TerminalConfig) (TerminalState, error) {
	terminal := &fakeTerminal{cols: int(config.Cols), rows: int(config.Rows)}
	f.mu.Lock()
	f.terminals[config.TabID] = terminal
	f.mu.Unlock()
	return terminal, nil
}

func (f *fakeTerminalFactory) terminal(id string) *fakeTerminal {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.terminals[id]
}

type fakeTerminal struct {
	mu             sync.Mutex
	raw            []byte
	cols           int
	rows           int
	closeCount     int
	response       func([]byte)
	hidden         bool
	visibilityHook func(bool)
}

func (t *fakeTerminal) Feed(data []byte) {
	t.mu.Lock()
	t.raw = append(t.raw, data...)
	t.mu.Unlock()
}

func (t *fakeTerminal) Dump(maxBytes int) []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	if maxBytes <= 0 {
		maxBytes = len(t.raw)
	}
	start := max(0, len(t.raw)-maxBytes)
	return append([]byte(nil), t.raw[start:]...)
}

func (t *fakeTerminal) Resize(cols, rows int) error {
	t.mu.Lock()
	t.cols, t.rows = cols, rows
	t.mu.Unlock()
	return nil
}

func (t *fakeTerminal) SetResponseHandler(handler func([]byte)) {
	t.mu.Lock()
	t.response = handler
	t.mu.Unlock()
}

func (t *fakeTerminal) SetVisible(visible bool) {
	if t.visibilityHook != nil {
		t.visibilityHook(visible)
	}
	t.mu.Lock()
	t.hidden = !visible
	t.mu.Unlock()
}

func (t *fakeTerminal) isHidden() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.hidden
}

func (t *fakeTerminal) Close() {
	t.mu.Lock()
	t.closeCount++
	t.mu.Unlock()
}

func (t *fakeTerminal) bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.raw...)
}

func (t *fakeTerminal) closes() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closeCount
}
