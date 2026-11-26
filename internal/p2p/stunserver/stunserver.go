// Package stunserver answers STUN Binding requests (RFC 8489) with the sender's reflexive transport address in
// XOR-MAPPED-ADDRESS, so clients and agents can learn their server-reflexive ICE candidates. It implements no
// other method, no authentication and no TURN relaying. Every source IP is rate limited; a response carries only
// the mapped address and a fingerprint (at most 52 bytes), so it cannot amplify much traffic.
package stunserver

import (
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/pion/stun/v4"
)

const (
	// DefaultRate and DefaultBurst bound the Binding requests answered per source IP: a gatherer sends a few
	// requests per server per session, so this leaves room for retransmissions and NAT probing.
	DefaultRate  = 5.0
	DefaultBurst = 20
	// DefaultMaxSources caps the tracked source IPs; requests from new sources are dropped while it is full.
	DefaultMaxSources = 65536

	maxPacket = 1500
)

// Options tunes a Server; zero values select the defaults.
type Options struct {
	// Rate is the sustained Binding requests per second answered per source IP; Burst is the bucket size.
	Rate  float64
	Burst int
	// MaxSources caps the number of source IPs tracked at once.
	MaxSources int
	Logger     *slog.Logger
	// Now is the limiter clock; tests inject one.
	Now func() time.Time
}

// Server owns the UDP sockets it listens on.
type Server struct {
	limiter *Limiter
	logger  *slog.Logger

	mu     sync.Mutex
	conns  []net.PacketConn
	closed bool
	wg     sync.WaitGroup
}

func New(options Options) *Server {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{limiter: NewLimiter(options.Rate, options.Burst, options.MaxSources, options.Now), logger: logger}
}

// Listen binds address ("host:port", host optional) over UDP and starts answering on it.
func (s *Server) Listen(address string) (net.Addr, error) {
	conn, err := net.ListenPacket("udp", address)
	if err != nil {
		return nil, err
	}
	if err := s.Serve(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn.LocalAddr(), nil
}

// Serve answers Binding requests on conn until the server closes; the server takes ownership of conn.
func (s *Server) Serve(conn net.PacketConn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("stunserver: closed")
	}
	s.conns = append(s.conns, conn)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.serve(conn)
	}()
	return nil
}

// Ports returns the local UDP ports being served, in listen order.
func (s *Server) Ports() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	ports := make([]int, 0, len(s.conns))
	for _, conn := range s.conns {
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			ports = append(ports, addr.Port)
		}
	}
	return ports
}

// Close stops every listener and waits for the serve loops to end.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	var first error
	for _, conn := range conns {
		if err := conn.Close(); err != nil && first == nil {
			first = err
		}
	}
	s.wg.Wait()
	return first
}

func (s *Server) serve(conn net.PacketConn) {
	buffer := make([]byte, maxPacket)
	for {
		n, source, err := conn.ReadFrom(buffer)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			s.logger.Warn("stun read failed", "error", err)
			return
		}
		response, ok := s.respond(buffer[:n], source)
		if !ok {
			continue
		}
		if _, err := conn.WriteTo(response, source); err != nil && !errors.Is(err, net.ErrClosed) {
			s.logger.Debug("stun write failed", "error", err)
		}
	}
}

// respond returns the Binding success response for a valid request from source, or false to drop the packet.
func (s *Server) respond(packet []byte, source net.Addr) ([]byte, bool) {
	udp, ok := source.(*net.UDPAddr)
	if !ok || udp.Port == 0 {
		return nil, false
	}
	address, ok := netip.AddrFromSlice(udp.IP)
	if !ok {
		return nil, false
	}
	address = address.Unmap()
	if !stun.IsMessage(packet) {
		return nil, false
	}
	request := &stun.Message{Raw: append([]byte(nil), packet...)}
	if err := request.Decode(); err != nil || request.Type != stun.BindingRequest {
		return nil, false
	}
	if !s.limiter.Allow(address) {
		return nil, false
	}
	response, err := stun.Build(
		stun.NewTransactionIDSetter(request.TransactionID),
		stun.BindingSuccess,
		&stun.XORMappedAddress{IP: address.AsSlice(), Port: udp.Port},
		stun.Fingerprint,
	)
	if err != nil {
		return nil, false
	}
	return response.Raw, true
}

// Limiter is a per-source-IP token bucket.
type Limiter struct {
	rate       float64
	burst      float64
	maxSources int
	now        func() time.Time

	mu      sync.Mutex
	buckets map[netip.Addr]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func NewLimiter(rate float64, burst, maxSources int, now func() time.Time) *Limiter {
	if rate <= 0 {
		rate = DefaultRate
	}
	if burst <= 0 {
		burst = DefaultBurst
	}
	if maxSources <= 0 {
		maxSources = DefaultMaxSources
	}
	if now == nil {
		now = time.Now
	}
	return &Limiter{rate: rate, burst: float64(burst), maxSources: maxSources, now: now, buckets: make(map[netip.Addr]*bucket)}
}

// Allow takes one token from address's bucket.
func (l *Limiter) Allow(address netip.Addr) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.buckets[address]
	if entry == nil {
		if len(l.buckets) >= l.maxSources {
			l.evictFull(now)
			if len(l.buckets) >= l.maxSources {
				return false
			}
		}
		entry = &bucket{tokens: l.burst, last: now}
		l.buckets[address] = entry
	}
	if elapsed := now.Sub(entry.last).Seconds(); elapsed > 0 {
		entry.tokens = min(l.burst, entry.tokens+elapsed*l.rate)
		entry.last = now
	}
	if entry.tokens < 1 {
		return false
	}
	entry.tokens--
	return true
}

// evictFull drops the buckets that have refilled completely: forgetting them changes no decision.
func (l *Limiter) evictFull(now time.Time) {
	for address, entry := range l.buckets {
		if entry.tokens+now.Sub(entry.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, address)
		}
	}
}
