package tools

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type hookTerminal struct {
	*terminal.Tab
	mu           sync.Mutex
	snapshotHook func()
}

func (h *hookTerminal) Snapshot() terminal.Snapshot {
	h.mu.Lock()
	hook := h.snapshotHook
	h.snapshotHook = nil
	h.mu.Unlock()
	if hook != nil {
		hook()
	}
	return h.Tab.Snapshot()
}

type atomicPTYChannel struct {
	id        string
	reads     chan []byte
	closed    chan struct{}
	closeOnce sync.Once

	mu       sync.Mutex
	pending  []byte
	writes   [][]byte
	attempts int
	failAt   int
	hook     func([]byte)
}

func newAtomicPTYChannel() *atomicPTYChannel {
	return &atomicPTYChannel{id: "atomic-channel", reads: make(chan []byte, 256), closed: make(chan struct{})}
}

func (c *atomicPTYChannel) Read(buffer []byte) (int, error) {
	c.mu.Lock()
	if len(c.pending) > 0 {
		n := copy(buffer, c.pending)
		c.pending = c.pending[n:]
		c.mu.Unlock()
		return n, nil
	}
	c.mu.Unlock()
	select {
	case data := <-c.reads:
		c.mu.Lock()
		n := copy(buffer, data)
		if n < len(data) {
			c.pending = append(c.pending, data[n:]...)
		}
		c.mu.Unlock()
		return n, nil
	case <-c.closed:
		return 0, io.EOF
	}
}

func (c *atomicPTYChannel) Write(data []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, base.ErrClosed
	default:
	}
	c.mu.Lock()
	c.attempts++
	attempt := c.attempts
	failAt := c.failAt
	hook := c.hook
	c.mu.Unlock()
	if hook != nil {
		hook(append([]byte(nil), data...))
	}
	if failAt == attempt {
		return 0, errors.New("injected write failure")
	}
	c.mu.Lock()
	c.writes = append(c.writes, append([]byte(nil), data...))
	c.mu.Unlock()
	return len(data), nil
}

func (c *atomicPTYChannel) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}
func (c *atomicPTYChannel) Stderr() io.Reader                            { return nil }
func (c *atomicPTYChannel) Resize(context.Context, uint32, uint32) error { return nil }
func (c *atomicPTYChannel) Wait(context.Context) error                   { return nil }
func (c *atomicPTYChannel) CloseWrite() error                            { return nil }
func (c *atomicPTYChannel) ID() string                                   { return c.id }
func (c *atomicPTYChannel) Generation() uint64                           { return 1 }

func (c *atomicPTYChannel) emit(data []byte) error {
	select {
	case c.reads <- append([]byte(nil), data...):
		return nil
	case <-c.closed:
		return base.ErrClosed
	case <-time.After(time.Second):
		return errors.New("channel emit timed out")
	}
}

func (c *atomicPTYChannel) successfulWrites() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([][]byte, len(c.writes))
	for index := range c.writes {
		result[index] = append([]byte(nil), c.writes[index]...)
	}
	return result
}

func (c *atomicPTYChannel) attemptCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempts
}

func (c *atomicPTYChannel) setHook(hook func([]byte)) {
	c.mu.Lock()
	c.hook = hook
	c.mu.Unlock()
}

type atomicPTYTransport struct {
	channel *atomicPTYChannel
	alive   atomic.Bool

	channelsMu sync.Mutex
	channels   []*atomicPTYChannel
	openCalls  int
}

func newAtomicPTYTransport() *atomicPTYTransport {
	channel := newAtomicPTYChannel()
	transport := &atomicPTYTransport{channel: channel, channels: []*atomicPTYChannel{channel}}
	transport.alive.Store(true)
	return transport
}

func (t *atomicPTYTransport) Kind() string       { return "ssh" }
func (t *atomicPTYTransport) Generation() uint64 { return 1 }
func (t *atomicPTYTransport) IsAlive() bool      { return t.alive.Load() }
func (t *atomicPTYTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}
func (t *atomicPTYTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	code := 0
	return base.ExecResult{ExitCode: &code}, nil
}
func (t *atomicPTYTransport) Close() error {
	t.alive.Store(false)
	t.channelsMu.Lock()
	defer t.channelsMu.Unlock()
	for _, channel := range t.channels {
		_ = channel.Close()
	}
	return nil
}
func (t *atomicPTYTransport) OpenPTY(context.Context, base.PTYOptions) (base.Channel, error) {
	t.channelsMu.Lock()
	defer t.channelsMu.Unlock()
	t.openCalls++
	if t.openCalls == 1 {
		return t.channel, nil
	}
	channel := newAtomicPTYChannel()
	t.channels = append(t.channels, channel)
	return channel, nil
}

func (t *atomicPTYTransport) channelAt(index int) *atomicPTYChannel {
	t.channelsMu.Lock()
	defer t.channelsMu.Unlock()
	return t.channels[index]
}

// trustedContext simulates the trusted-internal caller shape these atomicity
// tests exercise: an identified owner carrying the app's open-access vouch,
// the only shape a non-open server honors for full access.
func trustedContext() context.Context {
	return owner.WithOpenAccess(ipc.WithUserID(context.Background(), "owner-a"))
}

type atomicSessionFixture struct {
	manager   *session.Manager
	tabID     string
	sessionID string
	transport *atomicPTYTransport
	channel   *atomicPTYChannel
	clock     atomic.Int64

	terminalsMu sync.Mutex
	terminals   map[string]*hookTerminal
}

func newAtomicSessionFixture(t *testing.T, manualClock bool) *atomicSessionFixture {
	t.Helper()
	fixture := &atomicSessionFixture{transport: newAtomicPTYTransport(), channel: nil, terminals: make(map[string]*hookTerminal)}
	fixture.channel = fixture.transport.channel
	fixture.clock.Store(1_000_000)
	fixture.manager = session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return fixture.transport, nil
		}),
		Terminals: session.TerminalFactoryFunc(func(config session.TerminalConfig) (session.TerminalState, error) {
			options := []terminal.TabOption{}
			if manualClock {
				options = append(options, terminal.WithClock(func() time.Time {
					return time.UnixMilli(fixture.clock.Load())
				}))
			}
			core := &hookTerminal{Tab: terminal.NewTab(config.TabID, config.SessionID, int(config.Cols), int(config.Rows), terminal.UTF8, options...)}
			fixture.terminalsMu.Lock()
			fixture.terminals[config.TabID] = core
			fixture.terminalsMu.Unlock()
			return core, nil
		}),
	})
	connected, err := fixture.manager.Connect(ipc.WithUserID(context.Background(), "owner-a"), session.Asset{ID: "asset-atomic", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := fixture.manager.OpenTab(context.Background(), session.OpenTabOptions{
		SessionID: connected.ID, ClientID: "client", ChannelID: "channel", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.tabID = opened.ID
	fixture.sessionID = connected.ID
	t.Cleanup(func() {
		_ = fixture.manager.Close()
		_ = fixture.transport.Close()
	})
	return fixture
}

func (f *atomicSessionFixture) registry() *Registry {
	return NewRegistry(WithSession(Dependencies{PollInterval: time.Millisecond}, f.manager))
}

func (f *atomicSessionFixture) scope() Scope {
	return Scope{SessionID: f.sessionID, TabID: f.tabID, AssetID: "asset-atomic"}
}

func (f *atomicSessionFixture) terminal() *hookTerminal {
	f.terminalsMu.Lock()
	defer f.terminalsMu.Unlock()
	return f.terminals[f.tabID]
}

func (f *atomicSessionFixture) setSnapshotHook(hook func()) {
	core := f.terminal()
	core.mu.Lock()
	core.snapshotHook = hook
	core.mu.Unlock()
}

func (f *atomicSessionFixture) setWriteHook(hook func([]byte)) {
	f.channel.setHook(hook)
}

func (f *atomicSessionFixture) waitForMode(t *testing.T, bracketed bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, err := f.manager.TerminalModeState(f.tabID)
		if err == nil && state.BracketedPaste == bracketed {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("terminal bracketed mode = %v did not reach %v", !bracketed, bracketed)
}
