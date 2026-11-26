package rstream

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sort"
	"sync"
	"testing"
	"time"
)

var errCut = errors.New("fake carrier cut")

// fakeLink is a scriptable in-memory carrier: both ends buffer without bound, and the test can cut the link,
// delay or drop one end's writes, duplicate its DATA writes, or half-close it.
type fakeLink struct {
	mu     sync.Mutex
	broken bool
	ends   [2]*fakeConn
}

type fakeConn struct {
	link        *fakeLink
	side        int
	wake        chan struct{}
	inbox       []byte
	closed      bool
	writeClosed bool
	wrote       bool

	delay     time.Duration
	duplicate bool
	blackhole bool
}

type fakeAddr string

func (a fakeAddr) Network() string { return "fake" }
func (a fakeAddr) String() string  { return string(a) }

func newFakeLink() *fakeLink {
	link := &fakeLink{}
	for side := range link.ends {
		link.ends[side] = &fakeConn{link: link, side: side, wake: make(chan struct{})}
	}
	return link
}

func (l *fakeLink) client() *fakeConn { return l.ends[0] }
func (l *fakeLink) agent() *fakeConn  { return l.ends[1] }

func (c *fakeConn) peer() *fakeConn { return c.link.ends[1-c.side] }

func (c *fakeConn) broadcastLocked() {
	close(c.wake)
	c.wake = make(chan struct{})
}

// Cut breaks the link: both ends fail every read and write, and bytes in flight are lost.
func (l *fakeLink) Cut() {
	l.mu.Lock()
	l.broken = true
	for _, end := range l.ends {
		end.inbox = nil
		end.broadcastLocked()
	}
	l.mu.Unlock()
}

// damaged reports whether the test cut or half-closed the link, or the client closed it unused (a redial that
// lost the race to a concurrent migration).
func (l *fakeLink) damaged() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.broken || l.ends[0].writeClosed || l.ends[1].writeClosed || (l.ends[0].closed && !l.ends[0].wrote)
}

// configure changes how c's writes behave.
func (c *fakeConn) configure(apply func(c *fakeConn)) {
	c.link.mu.Lock()
	apply(c)
	c.link.mu.Unlock()
}

// HalfClose stops c's writes; the peer reads EOF once it drained them, and c still reads.
func (c *fakeConn) HalfClose() {
	c.link.mu.Lock()
	c.writeClosed = true
	c.peer().broadcastLocked()
	c.link.mu.Unlock()
}

func (c *fakeConn) Write(p []byte) (int, error) {
	c.link.mu.Lock()
	delay := c.delay
	c.link.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	c.link.mu.Lock()
	defer c.link.mu.Unlock()
	switch {
	case c.closed:
		return 0, net.ErrClosed
	case c.link.broken:
		return 0, errCut
	case c.writeClosed:
		return 0, io.ErrClosedPipe
	}
	c.wrote = true
	if c.blackhole {
		return len(p), nil
	}
	peer := c.peer()
	if peer.closed {
		return 0, io.ErrClosedPipe
	}
	peer.inbox = append(peer.inbox, p...)
	if c.duplicate && onlyDataOrAck(p) {
		peer.inbox = append(peer.inbox, p...)
	}
	peer.broadcastLocked()
	return len(p), nil
}

func (c *fakeConn) Read(p []byte) (int, error) {
	c.link.mu.Lock()
	defer c.link.mu.Unlock()
	for {
		switch {
		case c.closed:
			return 0, net.ErrClosed
		case c.link.broken:
			return 0, errCut
		case len(c.inbox) > 0:
			n := copy(p, c.inbox)
			c.inbox = c.inbox[n:]
			return n, nil
		case c.peer().writeClosed || c.peer().closed:
			return 0, io.EOF
		}
		wake := c.wake
		c.link.mu.Unlock()
		<-wake
		c.link.mu.Lock()
	}
}

func (c *fakeConn) Close() error {
	c.link.mu.Lock()
	c.closed = true
	c.broadcastLocked()
	c.peer().broadcastLocked()
	c.link.mu.Unlock()
	return nil
}

func (c *fakeConn) LocalAddr() net.Addr              { return fakeAddr("fake") }
func (c *fakeConn) RemoteAddr() net.Addr             { return fakeAddr("fake") }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

// onlyDataOrAck reports whether a carrier write holds only DATA and ACK frames, the frames a peer must take
// twice without harm.
func onlyDataOrAck(p []byte) bool {
	reader := bufio.NewReader(bytes.NewReader(p))
	for {
		kind, _, err := readFrame(reader)
		if errors.Is(err, io.EOF) {
			return true
		}
		if err != nil || (kind != frameData && kind != frameAck) {
			return false
		}
	}
}

// fakeClock fires its timers only when the test advances it.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Duration
	timers []*fakeTimer
}

type fakeTimer struct {
	clock *fakeClock
	at    time.Duration
	f     func()
	done  bool
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{clock: c, at: c.now + d, f: f}
	c.timers = append(c.timers, timer)
	return timer
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	stopped := !t.done
	t.done = true
	return stopped
}

// Advance moves the clock and runs the timers that came due, in order.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now += d
	var due []*fakeTimer
	for _, timer := range c.timers {
		if !timer.done && timer.at <= c.now {
			timer.done = true
			due = append(due, timer)
		}
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].at < due[j].at })
	c.mu.Unlock()
	for _, timer := range due {
		timer.f()
	}
}

// pending reports how many timers are armed.
func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, timer := range c.timers {
		if !timer.done {
			count++
		}
	}
	return count
}

// accepted is the outcome of one Server.Accept.
type accepted struct {
	link    *fakeLink
	conn    net.Conn
	resumed bool
	err     error
}

// harness pairs client streams with a Server over fake links.
type harness struct {
	t      *testing.T
	server *Server
	// purpose is the purpose each new carrier is authorized for.
	purpose  string
	accepted chan accepted
	mu       sync.Mutex
	links    []*fakeLink
}

func newHarness(t *testing.T, opts ServerOptions, purpose string) *harness {
	t.Helper()
	h := &harness{t: t, server: NewServer(opts), purpose: purpose, accepted: make(chan accepted, 16)}
	t.Cleanup(func() { _ = h.server.Close() })
	return h
}

// carrier returns the client end of a new link whose agent end is being accepted.
func (h *harness) carrier() *fakeLink {
	return h.carrierFor(h.purpose)
}

func (h *harness) carrierFor(purpose string) *fakeLink {
	link := newFakeLink()
	h.mu.Lock()
	h.links = append(h.links, link)
	h.mu.Unlock()
	go func() {
		conn, resumed, err := h.server.Accept(context.Background(), link.agent(), purpose)
		h.accepted <- accepted{link: link, conn: conn, resumed: resumed, err: err}
	}()
	return link
}

func (h *harness) next() accepted {
	h.t.Helper()
	select {
	case result := <-h.accepted:
		return result
	case <-time.After(10 * time.Second):
		h.t.Fatal("no Accept result")
		return accepted{}
	}
}

// open opens a client stream and returns it with the agent's side.
func (h *harness) open(opts Options) (*Stream, *fakeLink, net.Conn) {
	h.t.Helper()
	link := h.carrier()
	client, err := Open(context.Background(), link.client(), h.purpose, opts)
	if err != nil {
		h.t.Fatal(err)
	}
	result := h.next()
	if result.err != nil || result.resumed {
		h.t.Fatalf("accept = %+v", result)
	}
	return client, link, result.conn
}

// waitFor polls cond until it holds or the test times out.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
