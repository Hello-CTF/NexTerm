// Package legtest provides an in-memory leg.Leg pair and a fake network that connects the two sides of a
// direct connection by their ICE credentials, for deterministic tests of the layers above the leg.
package legtest

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/p2p/leg"
)

// Leg is one side of an in-memory leg. Streams are net.Pipe pairs.
type Leg struct {
	peer     *Leg
	accept   chan net.Conn
	done     chan struct{}
	once     sync.Once
	mu       sync.Mutex
	streams  []net.Conn
	ping     func() (time.Duration, error)
	loss     float64
	sent     uint64
	controls bool
}

// Pair returns two connected legs.
func Pair() (*Leg, *Leg) {
	a := &Leg{accept: make(chan net.Conn, 64), done: make(chan struct{}), controls: true}
	b := &Leg{accept: make(chan net.Conn, 64), done: make(chan struct{})}
	a.peer, b.peer = b, a
	return a, b
}

func (l *Leg) closed() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

func (l *Leg) OpenStream(ctx context.Context) (net.Conn, error) {
	if l.closed() || l.peer.closed() {
		return nil, leg.ErrClosed
	}
	local, remote := net.Pipe()
	l.track(local)
	l.peer.track(remote)
	select {
	case l.peer.accept <- remote:
		return local, nil
	case <-ctx.Done():
		_ = local.Close()
		_ = remote.Close()
		return nil, ctx.Err()
	case <-l.done:
		return nil, leg.ErrClosed
	}
}

func (l *Leg) track(conn net.Conn) {
	l.mu.Lock()
	l.streams = append(l.streams, conn)
	l.mu.Unlock()
	if l.closed() {
		_ = conn.Close()
	}
}

func (l *Leg) AcceptStream(ctx context.Context) (net.Conn, error) {
	select {
	case conn := <-l.accept:
		return conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.done:
		return nil, leg.ErrClosed
	}
}

// SetPing replaces the ping result; nil answers at once with 1 ms.
func (l *Leg) SetPing(ping func() (time.Duration, error)) {
	l.mu.Lock()
	l.ping = ping
	l.mu.Unlock()
}

// SetLoss sets the reported loss fraction.
func (l *Leg) SetLoss(loss float64) {
	l.mu.Lock()
	l.loss, l.sent = loss, 1000
	l.mu.Unlock()
}

// ErrPingLost is the result of a ping SetPing marks lost.
var ErrPingLost = errors.New("legtest: ping lost")

func (l *Leg) Ping(ctx context.Context) (time.Duration, error) {
	if l.closed() {
		return 0, leg.ErrClosed
	}
	l.mu.Lock()
	ping := l.ping
	l.mu.Unlock()
	if ping == nil {
		return time.Millisecond, nil
	}
	return ping()
}

func (l *Leg) Stats() leg.Stats {
	l.mu.Lock()
	defer l.mu.Unlock()
	stats := leg.Stats{Loss: l.loss, PacketsSent: l.sent, PacketsLost: uint64(float64(l.sent) * l.loss)}
	stats.Local = leg.Candidate{Type: "host", Network: "udp4", Address: "127.0.0.1", Port: 40000}
	stats.Remote = leg.Candidate{Type: "host", Network: "udp4", Address: "127.0.0.1", Port: 40001}
	return stats
}

// Close tears both sides down, as a dead path would.
func (l *Leg) Close() error {
	l.kill()
	l.peer.kill()
	return nil
}

func (l *Leg) kill() {
	l.once.Do(func() {
		close(l.done)
		l.mu.Lock()
		streams := l.streams
		l.mu.Unlock()
		for _, conn := range streams {
			_ = conn.Close()
		}
	})
}

func (l *Leg) Done() <-chan struct{} { return l.done }

var _ leg.Leg = (*Leg)(nil)

// Net pairs the leg dials of a client and an agent by their credentials.
type Net struct {
	mu      sync.Mutex
	waiting map[string]chan *Leg
	legs    []*Leg
	// Fail makes every dial fail with this error when set.
	fail  error
	dials int
	// configs records the configs of the controlling dials.
	configs []leg.Config
}

// NewNet returns an empty fake network.
func NewNet() *Net { return &Net{waiting: make(map[string]chan *Leg)} }

// SetFail makes later dials fail with err (nil restores them).
func (n *Net) SetFail(err error) {
	n.mu.Lock()
	n.fail = err
	n.mu.Unlock()
}

// Dial implements the DialLeg hooks: the controlling side creates the pair, the other side picks it up.
func (n *Net) Dial(ctx context.Context, cfg leg.Config, local, remote leg.Credentials, _ leg.Signaler) (leg.Leg, error) {
	n.mu.Lock()
	if cfg.Controlling {
		n.dials++
		n.configs = append(n.configs, cfg)
	}
	fail := n.fail
	var key string
	if cfg.Controlling {
		key = local.Ufrag + "/" + remote.Ufrag
	} else {
		key = remote.Ufrag + "/" + local.Ufrag
	}
	ch := n.waiting[key]
	if ch == nil {
		ch = make(chan *Leg, 1)
		n.waiting[key] = ch
	}
	n.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	if cfg.Controlling {
		client, agentSide := Pair()
		n.mu.Lock()
		n.legs = append(n.legs, client)
		n.mu.Unlock()
		ch <- agentSide
		return client, nil
	}
	select {
	case agentSide := <-ch:
		return agentSide, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Legs returns the client sides of the legs built so far.
func (n *Net) Legs() []*Leg {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]*Leg(nil), n.legs...)
}

// Dials reports how many controlling (client) dials ran and the configs they used.
func (n *Net) Dials() (int, []leg.Config) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.dials, append([]leg.Config(nil), n.configs...)
}
