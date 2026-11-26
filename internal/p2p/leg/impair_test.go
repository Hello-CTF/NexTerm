package leg

import (
	"math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// impairedConn delays and drops outgoing packets. Delay is a FIFO queue
// drained by one goroutine, so the link keeps its bandwidth while adding a
// constant one-way latency. Loss uses a seeded generator.
type impairedConn struct {
	net.PacketConn
	delay time.Duration

	mu       sync.Mutex
	rng      *rand.Rand
	loss     float64
	blackout bool

	queue   chan delayed
	stop    chan struct{}
	stopped sync.WaitGroup
	once    sync.Once
	dropped atomic.Uint64
}

type delayed struct {
	due  time.Time
	data []byte
	addr net.Addr
}

func newImpaired(pc net.PacketConn, seed uint64, loss float64, delay time.Duration) *impairedConn {
	c := &impairedConn{
		PacketConn: pc,
		delay:      delay,
		rng:        rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
		loss:       loss,
		queue:      make(chan delayed, 8192),
		stop:       make(chan struct{}),
	}
	c.stopped.Add(1)
	go c.drain()
	return c
}

func (c *impairedConn) SetReadBuffer(int) error  { return nil }
func (c *impairedConn) SetWriteBuffer(int) error { return nil }

func (c *impairedConn) setBlackout(v bool) {
	c.mu.Lock()
	c.blackout = v
	c.mu.Unlock()
}

func (c *impairedConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	c.mu.Lock()
	drop := c.blackout || (c.loss > 0 && c.rng.Float64() < c.loss)
	c.mu.Unlock()
	if drop {
		c.dropped.Add(1)
		return len(p), nil
	}
	if c.delay <= 0 {
		return c.PacketConn.WriteTo(p, addr)
	}
	d := delayed{due: time.Now().Add(c.delay), data: append([]byte(nil), p...), addr: addr}
	select {
	case c.queue <- d:
	default:
		c.dropped.Add(1) // queue overflow behaves like a full router buffer
	}
	return len(p), nil
}

func (c *impairedConn) drain() {
	defer c.stopped.Done()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		select {
		case <-c.stop:
			return
		case d := <-c.queue:
			if wait := time.Until(d.due); wait > 0 {
				timer.Reset(wait)
				select {
				case <-c.stop:
					return
				case <-timer.C:
				}
			}
			_, _ = c.PacketConn.WriteTo(d.data, d.addr)
		}
	}
}

func (c *impairedConn) Close() error {
	c.once.Do(func() { close(c.stop) })
	c.stopped.Wait()
	return c.PacketConn.Close()
}

// impairment returns a PacketConnWrapper and a handle to the wrapped conn.
func impairment(seed uint64, loss float64, delay time.Duration) (func(net.PacketConn) net.PacketConn, func() *impairedConn) {
	var mu sync.Mutex
	var made *impairedConn
	wrap := func(pc net.PacketConn) net.PacketConn {
		c := newImpaired(pc, seed, loss, delay)
		mu.Lock()
		made = c
		mu.Unlock()
		return c
	}
	get := func() *impairedConn {
		mu.Lock()
		defer mu.Unlock()
		return made
	}
	return wrap, get
}
