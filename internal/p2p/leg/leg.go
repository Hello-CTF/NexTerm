// Package leg builds a direct peer-to-peer leg between a NexTerm client and
// an agent: ICE (pion/ice) finds a UDP path, QUIC (quic-go) runs over the
// selected candidate pair with mutual TLS 1.3 pinned to Ed25519 keys, and each
// QUIC stream is exposed as a net.Conn.
//
// Upper layers (rstream, the path selector) depend only on the Leg interface
// so the stack under it can be replaced without touching them.
package leg

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/logging"
	"github.com/pion/stun/v4"
	"github.com/quic-go/quic-go"
)

// Leg is one established direct connection. Every stream is a net.Conn.
type Leg interface {
	// OpenStream opens a new bidirectional stream. The peer's AcceptStream
	// returns it immediately; no application write is needed first.
	OpenStream(ctx context.Context) (net.Conn, error)
	// AcceptStream waits for the next stream the peer opened.
	AcceptStream(ctx context.Context) (net.Conn, error)
	// Stats reports transport statistics of the selected path.
	Stats() Stats
	// Ping measures one application-level round trip over an unreliable
	// QUIC datagram. A lost ping times out; without a context deadline it
	// waits at most DefaultPingTimeout.
	Ping(ctx context.Context) (time.Duration, error)
	// Close tears down the QUIC connection, the transport and the ICE agent.
	Close() error
	// Done is closed once the leg is unusable: closed locally, closed or
	// timed out by the peer, or the ICE path failed.
	Done() <-chan struct{}
}

// Credentials are the ICE short-term credentials of one side.
type Credentials struct {
	Ufrag, Pwd string
}

// Signaler carries trickled ICE candidates to and from the peer.
type Signaler interface {
	SendCandidate(ctx context.Context, c string) error
	// Candidates delivers the peer's candidates. Closing it means no more
	// candidates will arrive; the leg keeps working with what it has.
	Candidates() <-chan string
}

// Config describes one side of a leg.
type Config struct {
	// STUN lists STUN servers as host:port.
	STUN []string
	// Controlling is true on the client side; it also opens the QUIC client.
	Controlling bool
	// LocalKey signs this side's self-signed certificate. PeerKey is the only
	// certificate public key accepted from the peer.
	LocalKey ed25519.PrivateKey
	PeerKey  ed25519.PublicKey
	// HostOnly gathers only host candidates and uses the short check timeout;
	// it is the fallback when NAT probing says hole punching cannot work.
	HostOnly bool
	// Zero values select DefaultGatherTimeout, DefaultCheckTimeout (or
	// DefaultHostOnlyCheckTimeout with HostOnly) and DefaultHandshakeTimeout.
	GatherTimeout, CheckTimeout, HandshakeTimeout time.Duration
	// PacketConnWrapper wraps the packet conn that QUIC reads and writes,
	// i.e. above ICE. It is a test hook for injected loss and latency; ICE
	// connectivity checks are not affected by it.
	PacketConnWrapper func(net.PacketConn) net.PacketConn
	// LoopbackOnly gathers only IPv4 loopback host candidates. It is a test hook for in-process legs on hosts
	// whose only usable interface may be loopback; production never sets it.
	LoopbackOnly bool

	// iceOptions are appended to the ICE agent options (test hook for vnet,
	// loopback candidates and faster checks).
	iceOptions []ice.AgentOption
	// idleTimeout overrides QUICIdleTimeout (test hook).
	idleTimeout time.Duration
	// ignorePings makes this side leave its first N received pings
	// unanswered (test hook for lost datagrams).
	ignorePings int
}

// Timeouts from design 2.4 and keepalive values chosen to stay above the
// application ping detection (3 x 1.5 s) while keeping NAT bindings alive.
const (
	DefaultGatherTimeout        = 3 * time.Second
	DefaultCheckTimeout         = 8 * time.Second
	DefaultHostOnlyCheckTimeout = 3 * time.Second
	DefaultHandshakeTimeout     = 3 * time.Second
	DefaultPingTimeout          = 1500 * time.Millisecond

	// QUICIdleTimeout closes a leg whose peer has been silent this long.
	QUICIdleTimeout = 15 * time.Second
	// QUICKeepAlivePeriod makes QUIC send a PING when nothing else is sent,
	// so the NAT binding never sees an idle gap beyond it.
	QUICKeepAlivePeriod = 5 * time.Second
	// QUICPacketSize is the fixed UDP payload size. Path MTU discovery needs
	// the DF capability that a non-socket packet conn cannot report, so the
	// size is fixed at a value that fits a 1280-byte IPv6 minimum MTU.
	QUICPacketSize = 1232
	// MaxIncomingStreams bounds concurrently open peer streams.
	MaxIncomingStreams = 4096

	iceKeepaliveInterval   = 2 * time.Second
	iceDisconnectedTimeout = 5 * time.Second
	iceFailedTimeout       = 10 * time.Second

	alpn = "nexterm-p2p/1"
)

// Errors returned by Dial, wrapped with detail.
var (
	ErrICE       = errors.New("p2p leg: ice connectivity failed")
	ErrHandshake = errors.New("p2p leg: quic handshake failed")
	ErrPeerKey   = errors.New("p2p leg: peer certificate key mismatch")
	ErrClosed    = errors.New("p2p leg: closed")
)

// NewCredentials returns random ICE credentials (96-bit ufrag, 192-bit pwd).
func NewCredentials() Credentials {
	return Credentials{Ufrag: randomICEString(12), Pwd: randomICEString(24)}
}

func randomICEString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	// The standard base64 alphabet is a subset of ice-char (RFC 8445 15.1).
	return base64.RawStdEncoding.EncodeToString(b)
}

func (c Config) withDefaults() Config {
	if c.GatherTimeout <= 0 {
		c.GatherTimeout = DefaultGatherTimeout
	}
	if c.CheckTimeout <= 0 {
		c.CheckTimeout = DefaultCheckTimeout
		if c.HostOnly {
			c.CheckTimeout = DefaultHostOnlyCheckTimeout
		}
	}
	if c.HandshakeTimeout <= 0 {
		c.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if c.idleTimeout <= 0 {
		c.idleTimeout = QUICIdleTimeout
	}
	return c
}

func (c Config) validate(local, remote Credentials, sig Signaler) error {
	if len(c.LocalKey) != ed25519.PrivateKeySize {
		return errors.New("p2p leg: invalid local key")
	}
	if len(c.PeerKey) != ed25519.PublicKeySize {
		return errors.New("p2p leg: invalid peer key")
	}
	if sig == nil {
		return errors.New("p2p leg: nil signaler")
	}
	if len(local.Ufrag) < 4 || len(local.Pwd) < 22 || len(remote.Ufrag) < 4 || len(remote.Pwd) < 22 {
		return errors.New("p2p leg: ice credentials too short")
	}
	return nil
}

func stunURIs(servers []string) ([]*stun.URI, error) {
	uris := make([]*stun.URI, 0, len(servers))
	for _, s := range servers {
		host, portText, err := net.SplitHostPort(s)
		if err != nil {
			return nil, fmt.Errorf("p2p leg: stun server %q: %w", s, err)
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port <= 0 || port > 65535 {
			return nil, fmt.Errorf("p2p leg: stun server %q: invalid port", s)
		}
		uris = append(uris, &stun.URI{Scheme: stun.SchemeTypeSTUN, Host: host, Port: port, Proto: stun.ProtoTypeUDP})
	}
	return uris, nil
}

func newAgent(cfg Config, local Credentials) (*ice.Agent, error) {
	candidateTypes := []ice.CandidateType{ice.CandidateTypeHost, ice.CandidateTypeServerReflexive}
	var uris []*stun.URI
	if cfg.HostOnly {
		candidateTypes = []ice.CandidateType{ice.CandidateTypeHost}
	} else {
		var err error
		if uris, err = stunURIs(cfg.STUN); err != nil {
			return nil, err
		}
	}
	opts := []ice.AgentOption{
		ice.WithLocalCredentials(local.Ufrag, local.Pwd),
		ice.WithNetworkTypes([]ice.NetworkType{ice.NetworkTypeUDP4, ice.NetworkTypeUDP6}),
		ice.WithCandidateTypes(candidateTypes),
		ice.WithMulticastDNSMode(ice.MulticastDNSModeDisabled),
		ice.WithDisableActiveTCP(),
		ice.WithSTUNGatherTimeout(cfg.GatherTimeout),
		ice.WithKeepaliveInterval(iceKeepaliveInterval),
		ice.WithDisconnectedTimeout(iceDisconnectedTimeout),
		ice.WithFailedTimeout(iceFailedTimeout),
		// Padded checks only validate a pair that carries full-size QUIC
		// packets, so a path with a smaller MTU is never selected.
		ice.WithPaddedCheckSize(QUICPacketSize),
		ice.WithLoggerFactory(&logging.DefaultLoggerFactory{Writer: io.Discard, DefaultLogLevel: logging.LogLevelDisabled}),
	}
	if len(uris) > 0 {
		opts = append(opts, ice.WithUrls(uris))
	}
	if cfg.LoopbackOnly {
		opts = append(opts, ice.WithIncludeLoopback(), ice.WithIPFilter(func(ip net.IP) bool { return ip.IsLoopback() && ip.To4() != nil }))
	}
	opts = append(opts, cfg.iceOptions...)
	return ice.NewAgentWithOptions(opts...)
}

type leg struct {
	cfg     Config
	agent   *ice.Agent
	pconn   *packetConn
	qpc     net.PacketConn // pconn, possibly wrapped
	tr      *quic.Transport
	ln      *quic.Listener
	conn    *quic.Conn
	since   time.Time
	verify  *peerVerifier
	pings   pinger
	sampler lossSampler

	ctx    context.Context
	cancel context.CancelFunc

	iceDead     chan struct{}
	iceDeadOnce sync.Once

	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// Dial establishes a leg: ICE connectivity, then a QUIC handshake over the
// selected pair (the Controlling side is the QUIC client), then one ping round
// trip so both sides know the peer accepted their certificate.
func Dial(ctx context.Context, cfg Config, local, remote Credentials, sig Signaler) (Leg, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(local, remote, sig); err != nil {
		return nil, err
	}
	agent, err := newAgent(cfg, local)
	if err != nil {
		return nil, fmt.Errorf("p2p leg: ice agent: %w", err)
	}
	legCtx, cancel := context.WithCancel(context.Background())
	l := &leg{
		cfg:     cfg,
		agent:   agent,
		ctx:     legCtx,
		cancel:  cancel,
		iceDead: make(chan struct{}),
		done:    make(chan struct{}),
		verify:  &peerVerifier{key: cfg.PeerKey},
	}
	if err := l.establish(ctx, remote, sig); err != nil {
		l.teardown()
		l.wg.Wait()
		return nil, err
	}
	l.wg.Add(1)
	go l.monitor()
	return l, nil
}

func (l *leg) establish(ctx context.Context, remote Credentials, sig Signaler) error {
	if err := l.agent.OnConnectionStateChange(func(s ice.ConnectionState) {
		if s == ice.ConnectionStateFailed || s == ice.ConnectionStateClosed {
			l.iceDeadOnce.Do(func() { close(l.iceDead) })
		}
	}); err != nil {
		return fmt.Errorf("p2p leg: ice agent: %w", err)
	}
	l.startSignaling(sig)
	if err := l.agent.GatherCandidates(); err != nil {
		return fmt.Errorf("p2p leg: gather candidates: %w", err)
	}

	iceCtx, cancelICE := context.WithTimeout(ctx, l.cfg.CheckTimeout)
	defer cancelICE()
	var iceConn *ice.Conn
	var err error
	if l.cfg.Controlling {
		iceConn, err = l.agent.Dial(iceCtx, remote.Ufrag, remote.Pwd)
	} else {
		iceConn, err = l.agent.Accept(iceCtx, remote.Ufrag, remote.Pwd)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrICE, err)
	}

	l.pconn = newPacketConn(iceConn)
	l.qpc = l.pconn
	if l.cfg.PacketConnWrapper != nil {
		l.qpc = l.cfg.PacketConnWrapper(l.pconn)
	}
	l.tr = &quic.Transport{Conn: l.qpc}

	hsCtx, cancelHS := context.WithTimeout(ctx, l.cfg.HandshakeTimeout)
	defer cancelHS()
	qconf := quicConfig(l.cfg.HandshakeTimeout, l.cfg.idleTimeout)
	if l.cfg.Controlling {
		tlsConf, err := clientTLSConfig(l.cfg.LocalKey, l.verify)
		if err != nil {
			return err
		}
		l.conn, err = l.tr.Dial(hsCtx, peerAddr, tlsConf, qconf)
		if err != nil {
			return l.handshakeError(err)
		}
	} else {
		tlsConf, err := serverTLSConfig(l.cfg.LocalKey, l.verify)
		if err != nil {
			return err
		}
		l.ln, err = l.tr.Listen(tlsConf, qconf)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrHandshake, err)
		}
		l.conn, err = l.ln.Accept(hsCtx)
		if err != nil {
			return l.handshakeError(err)
		}
		// One leg carries one connection; closing the listener leaves the
		// accepted connection running and refuses any further handshake.
		_ = l.ln.Close()
	}
	l.since = time.Now()
	l.pings.init()
	l.wg.Add(2)
	go l.datagramLoop()
	go l.sampleLoop()

	// In TLS 1.3 the client completes before the server has checked its
	// certificate; a ping round trip proves the peer accepted us.
	// Each attempt gets a third of the budget so one lost datagram is
	// retried instead of failing the dial.
	pingCtx, cancelPing := context.WithTimeout(hsCtx, l.cfg.HandshakeTimeout)
	defer cancelPing()
	attempt := max(l.cfg.HandshakeTimeout/3, 50*time.Millisecond)
	for {
		ctx, cancel := context.WithTimeout(pingCtx, attempt)
		_, err := l.Ping(ctx)
		cancel()
		if err == nil {
			return nil
		}
		if pingCtx.Err() != nil || l.conn.Context().Err() != nil || l.closed() {
			return l.handshakeError(err)
		}
	}
}

func (l *leg) handshakeError(err error) error {
	if l.verify.mismatched() {
		return fmt.Errorf("%w: %w", ErrPeerKey, err)
	}
	var tlsErr *quic.TransportError
	if errors.As(err, &tlsErr) && tlsErr.ErrorCode.IsCryptoError() {
		return fmt.Errorf("%w: peer rejected the handshake: %w", ErrHandshake, err)
	}
	return fmt.Errorf("%w: %w", ErrHandshake, err)
}

func quicConfig(handshakeTimeout, idleTimeout time.Duration) *quic.Config {
	return &quic.Config{
		Versions:                []quic.Version{quic.Version1},
		HandshakeIdleTimeout:    handshakeTimeout,
		MaxIdleTimeout:          idleTimeout,
		KeepAlivePeriod:         min(QUICKeepAlivePeriod, idleTimeout/3),
		InitialPacketSize:       QUICPacketSize,
		DisablePathMTUDiscovery: true,
		EnableDatagrams:         true,
		MaxIncomingStreams:      MaxIncomingStreams,
		MaxIncomingUniStreams:   -1,
	}
}

// startSignaling sends local candidates and applies remote ones. Pion calls
// OnCandidate on its own goroutine; the queue keeps a slow signaler from
// blocking it.
func (l *leg) startSignaling(sig Signaler) {
	out := newStringQueue()
	_ = l.agent.OnCandidate(func(c ice.Candidate) {
		if c != nil {
			out.push(c.Marshal())
		}
	})
	l.wg.Add(2)
	go func() {
		defer l.wg.Done()
		for {
			c, ok := out.pop(l.ctx)
			if !ok {
				return
			}
			// A failed send only loses one candidate; the peer may still
			// reach us through another one or a peer-reflexive address.
			_ = sig.SendCandidate(l.ctx, c)
		}
	}()
	go func() {
		defer l.wg.Done()
		in := sig.Candidates()
		for {
			select {
			case <-l.ctx.Done():
				return
			case raw, ok := <-in:
				if !ok {
					return
				}
				c, err := ice.UnmarshalCandidate(raw)
				if err != nil {
					continue
				}
				if l.cfg.HostOnly && c.Type() != ice.CandidateTypeHost {
					continue
				}
				_ = l.agent.AddRemoteCandidate(c)
			}
		}
	}()
}

func (l *leg) monitor() {
	defer l.wg.Done()
	select {
	case <-l.ctx.Done():
	case <-l.conn.Context().Done():
	case <-l.iceDead:
	}
	l.teardown()
}

// teardown releases everything; it is safe to call more than once and from
// any goroutine except pion callbacks.
func (l *leg) teardown() {
	l.closeOnce.Do(func() {
		l.cancel()
		if l.conn != nil {
			_ = l.conn.CloseWithError(0, "closed")
		}
		if l.ln != nil {
			_ = l.ln.Close()
		}
		if l.tr != nil {
			_ = l.tr.Close()
		}
		if l.qpc != nil {
			_ = l.qpc.Close()
		}
		_ = l.agent.Close()
		close(l.done)
	})
}

func (l *leg) Close() error {
	l.teardown()
	l.wg.Wait()
	return nil
}

func (l *leg) Done() <-chan struct{} { return l.done }

func (l *leg) OpenStream(ctx context.Context) (net.Conn, error) {
	if l.closed() {
		return nil, ErrClosed
	}
	s, err := l.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, l.streamError(err)
	}
	// The preface byte makes the stream visible to the peer at once.
	if _, err := s.Write([]byte{streamPreface}); err != nil {
		s.CancelRead(0)
		s.CancelWrite(0)
		return nil, l.streamError(err)
	}
	return newStream(s, l.conn), nil
}

func (l *leg) AcceptStream(ctx context.Context) (net.Conn, error) {
	for {
		if l.closed() {
			return nil, ErrClosed
		}
		s, err := l.conn.AcceptStream(ctx)
		if err != nil {
			return nil, l.streamError(err)
		}
		if err := readPreface(ctx, s); err != nil {
			s.CancelRead(0)
			s.CancelWrite(0)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		return newStream(s, l.conn), nil
	}
}

func (l *leg) closed() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

func (l *leg) streamError(err error) error {
	if l.closed() {
		return fmt.Errorf("%w: %w", ErrClosed, err)
	}
	return err
}

// stringQueue is an unbounded FIFO so pion's callback never blocks.
type stringQueue struct {
	mu     sync.Mutex
	items  []string
	notify chan struct{}
}

func newStringQueue() *stringQueue {
	return &stringQueue{notify: make(chan struct{}, 1)}
}

func (q *stringQueue) push(s string) {
	q.mu.Lock()
	q.items = append(q.items, s)
	q.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *stringQueue) pop(ctx context.Context) (string, bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			s := q.items[0]
			q.items = q.items[1:]
			q.mu.Unlock()
			return s, true
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", false
		case <-q.notify:
		}
	}
}
