package terminal

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock for idle/last-output tests.
type fakeClock struct{ ms atomic.Int64 }

func newFakeClock() *fakeClock {
	c := &fakeClock{}
	c.ms.Store(1_000_000)
	return c
}

func (c *fakeClock) now() time.Time { return time.UnixMilli(c.ms.Load()) }

func (c *fakeClock) advance(d time.Duration) { c.ms.Add(d.Milliseconds()) }

// memSink records frames in order.
type memSink struct {
	mu     sync.Mutex
	frames [][]byte
	err    error
}

func (m *memSink) SendBinary(_ context.Context, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.frames = append(m.frames, bytes.Clone(data))
	return nil
}

func (m *memSink) Bytes() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []byte
	for _, f := range m.frames {
		out = append(out, f...)
	}
	return out
}

func (m *memSink) Frames() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]byte, len(m.frames))
	copy(out, m.frames)
	return out
}

func (m *memSink) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, f := range m.frames {
		n += len(f)
	}
	return n
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func newTestTab(t *testing.T, opts ...TabOption) *Tab {
	t.Helper()
	tab := NewTab("t1", "s1", 80, 24, UTF8, opts...)
	t.Cleanup(tab.Close)
	return tab
}

func newTestOutput(t *testing.T, tab *Tab, opts ...OutputOption) *Output {
	t.Helper()
	o := NewOutput(tab, opts...)
	t.Cleanup(o.Close)
	return o
}
