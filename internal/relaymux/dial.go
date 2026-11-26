package relaymux

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

// DialConfig describes how to reach one server.
type DialConfig struct {
	// QUICAddrs are the server's QUIC relay endpoints (host:port), tried in order. Empty disables QUIC.
	QUICAddrs []string
	// Pin is the server key pin (Config.Pin). Without it the QUIC certificate must verify against RootCAs
	// (nil: system roots) for ServerName.
	Pin        string
	ServerName string
	RootCAs    *x509.CertPool

	// WSURL is the websocket carrier URL (ws:// or wss://, ending in WSPath); HTTPClient carries the TLS and
	// proxy settings of the HTTPS API and Header extra upgrade headers. Empty WSURL disables the fallback.
	WSURL      string
	HTTPClient *http.Client
	Header     http.Header

	// Hello authenticates the session.
	Hello Hello

	// HeadStart is QUIC's lead before the websocket carrier starts (DefaultHeadStart); QUICTimeout bounds the
	// QUIC attempt (DefaultQUICTimeout).
	HeadStart   time.Duration
	QUICTimeout time.Duration
	// Backoff remembers QUIC failures per server (nil: DefaultBackoff); BackoffKey names the server in it
	// (empty: WSURL).
	Backoff    *QUICBackoff
	BackoffKey string

	// ListenPacket opens the client UDP socket of a QUIC attempt (nil: an unbound UDP socket). Tests inject a
	// socket that drops everything to stand for a network that blocks UDP.
	ListenPacket func() (net.PacketConn, error)
	// after is the head start timer (test hook).
	after func(time.Duration) <-chan time.Time
}

// QUICBackoff remembers per server that QUIC failed, so the next dials go straight to the websocket carrier
// until the period ends.
type QUICBackoff struct {
	period time.Duration
	now    func() time.Time

	mu    sync.Mutex
	until map[string]time.Time
}

// NewQUICBackoff returns a backoff of period (DefaultQUICBackoff when not positive); now nil uses time.Now.
func NewQUICBackoff(period time.Duration, now func() time.Time) *QUICBackoff {
	if period <= 0 {
		period = DefaultQUICBackoff
	}
	if now == nil {
		now = time.Now
	}
	return &QUICBackoff{period: period, now: now, until: make(map[string]time.Time)}
}

// DefaultBackoff is the process-wide QUIC failure memory.
var DefaultBackoff = NewQUICBackoff(DefaultQUICBackoff, nil)

// Blocked reports whether key's QUIC attempts are suspended.
func (b *QUICBackoff) Blocked(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.until[key]
	if !ok {
		return false
	}
	if !b.now().Before(until) {
		delete(b.until, key)
		return false
	}
	return true
}

// Fail suspends key's QUIC attempts for the period.
func (b *QUICBackoff) Fail(key string) {
	b.mu.Lock()
	b.until[key] = b.now().Add(b.period)
	b.mu.Unlock()
}

// Clear forgets a failure after QUIC worked.
func (b *QUICBackoff) Clear(key string) {
	b.mu.Lock()
	delete(b.until, key)
	b.mu.Unlock()
}

type attempt struct {
	kind string
	link *Link
	err  error
}

// Dial establishes an authenticated session to the server, racing QUIC against the websocket carrier: QUIC
// starts first, the websocket after HeadStart or as soon as QUIC fails, and the first carrier whose handshake
// the server accepts wins; the loser is closed. A refusal of the Hello is final and returned as a
// *RefusedError. QUIC failing, or losing the race, suspends QUIC for the server for the backoff period, during
// which the websocket starts at once, so a network that drops UDP costs one head start per backoff period.
func Dial(ctx context.Context, config DialConfig) (*Link, error) {
	if len(config.QUICAddrs) == 0 && config.WSURL == "" {
		return nil, errors.New("relaymux: no carrier configured")
	}
	headStart := config.HeadStart
	if headStart <= 0 {
		headStart = DefaultHeadStart
	}
	backoff := config.Backoff
	if backoff == nil {
		backoff = DefaultBackoff
	}
	key := config.BackoffKey
	if key == "" {
		key = config.WSURL
	}
	after := config.after
	if after == nil {
		after = time.After
	}

	raceCtx, cancel := context.WithCancel(ctx)
	results := make(chan attempt, 2)
	pending := 0
	start := func(kind string, run func(context.Context) (*Link, error)) {
		pending++
		go func() {
			link, err := run(raceCtx)
			results <- attempt{kind: kind, link: link, err: err}
		}()
	}
	// finish closes every link that arrives after the decision, once all attempts returned.
	finish := func(remaining int) {
		cancel()
		if remaining == 0 {
			return
		}
		go func() {
			for range remaining {
				if late := <-results; late.link != nil {
					_ = late.link.Close()
				}
			}
		}()
	}

	quicPending := false
	if len(config.QUICAddrs) > 0 && !backoff.Blocked(key) {
		quicPending = true
		start(KindQUIC, func(ctx context.Context) (*Link, error) { return dialQUIC(ctx, config) })
	}
	wsStarted := false
	startWS := func() {
		if !wsStarted && config.WSURL != "" {
			wsStarted = true
			start(KindWS, func(ctx context.Context) (*Link, error) { return dialWSLink(ctx, config) })
		}
	}
	var headStartTimer <-chan time.Time
	if quicPending {
		headStartTimer = after(headStart)
	} else {
		startWS()
	}

	var errs []error
	for {
		select {
		case <-headStartTimer:
			headStartTimer = nil
			startWS()
			continue
		case result := <-results:
			pending--
			if result.kind == KindQUIC {
				quicPending = false
			}
			if result.err == nil {
				if result.kind == KindQUIC {
					backoff.Clear(key)
				} else if quicPending {
					// QUIC is still handshaking after the head start and a whole websocket handshake: treat
					// the path as unusable rather than keep paying the head start on every dial.
					backoff.Fail(key)
				}
				finish(pending)
				return result.link, nil
			}
			if _, refused := Refused(result.err); refused {
				finish(pending)
				return nil, result.err
			}
			errs = append(errs, fmt.Errorf("%s: %w", result.kind, result.err))
			if result.kind == KindQUIC {
				if ctx.Err() == nil {
					backoff.Fail(key)
				}
				headStartTimer = nil
				startWS()
			}
			if pending == 0 {
				finish(0)
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, fmt.Errorf("relaymux: dial: %w", errors.Join(errs...))
			}
		case <-ctx.Done():
			finish(pending)
			return nil, ctx.Err()
		}
	}
}

func dialWSLink(ctx context.Context, config DialConfig) (*Link, error) {
	session, err := DialWS(ctx, config.WSURL, config.HTTPClient, config.Header)
	if err != nil {
		return nil, err
	}
	return Handshake(ctx, session, config.Hello)
}

func dialQUIC(ctx context.Context, config DialConfig) (*Link, error) {
	timeout := config.QUICTimeout
	if timeout <= 0 {
		timeout = DefaultQUICTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var errs []error
	for _, address := range config.QUICAddrs {
		session, err := DialQUIC(ctx, address, config)
		if err != nil {
			errs = append(errs, err)
			if ctx.Err() != nil {
				break
			}
			continue
		}
		link, err := Handshake(ctx, session, config.Hello)
		if err != nil {
			return nil, err
		}
		return link, nil
	}
	return nil, errors.Join(errs...)
}

// DialQUIC opens a QUIC carrier session to address, authenticating the server by config.Pin or, without a
// pin, by its certificate chain for config.ServerName (default: the address host).
func DialQUIC(ctx context.Context, address string, config DialConfig) (Session, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	serverName := config.ServerName
	if serverName == "" {
		serverName = host
	}
	tlsConfig, err := clientTLSConfig(config.Pin, serverName, config.RootCAs)
	if err != nil {
		return nil, err
	}
	remote, err := resolveUDP(ctx, host, port)
	if err != nil {
		return nil, err
	}
	listen := config.ListenPacket
	if listen == nil {
		listen = func() (net.PacketConn, error) { return net.ListenUDP("udp", nil) }
	}
	packetConn, err := listen()
	if err != nil {
		return nil, err
	}
	transport := &quic.Transport{Conn: packetConn}
	release := func() {
		_ = transport.Close()
		_ = packetConn.Close()
	}
	conn, err := transport.Dial(ctx, remote, tlsConfig, quicConfig())
	if err != nil {
		release()
		return nil, fmt.Errorf("relaymux: quic %s: %w", address, err)
	}
	return newQUICSession(conn, release), nil
}

func resolveUDP(ctx context.Context, host, port string) (*net.UDPAddr, error) {
	portNumber, err := net.LookupPort("udp", port)
	if err != nil {
		return nil, err
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return net.UDPAddrFromAddrPort(netip.AddrPortFrom(address.Unmap(), uint16(portNumber))), nil
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("relaymux: %s has no address", host)
	}
	return net.UDPAddrFromAddrPort(netip.AddrPortFrom(addresses[0].Unmap(), uint16(portNumber))), nil
}
