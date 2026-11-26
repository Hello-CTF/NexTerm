// Package agentside is the device agent's half of direct connections: it answers offers on p2p-signal bridges,
// builds the leg as the non-controlling side with the device key, checks every direct stream against the grant,
// hands accepted streams to the runtime's rstream server, honours p2p_revoke and reports each direct stream to
// the server's audit endpoint.
package agentside

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/p2p/clock"
	"github.com/Hello-CTF/NexTerm/internal/p2p/grant"
	"github.com/Hello-CTF/NexTerm/internal/p2p/leg"
	"github.com/Hello-CTF/NexTerm/internal/p2p/natprobe"
	"github.com/Hello-CTF/NexTerm/internal/p2p/signal"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

const (
	// Grace is how long streams opened under a grant outlive its expiry; new streams need a valid grant.
	Grace = 60 * time.Second
	// ConfigTTL is how long a fetched config is reused.
	ConfigTTL = 10 * time.Minute
	// OfferTimeout bounds the wait for the first frame of a signal bridge.
	OfferTimeout = 15 * time.Second
	// PurposeTimeout bounds the wait for a direct stream's purpose preamble.
	PurposeTimeout = 10 * time.Second
	// DialBudget bounds probing and building a leg: the NAT probe, gathering, checks and the QUIC handshake.
	DialBudget = natprobe.DefaultTimeout + leg.DefaultGatherTimeout + leg.DefaultCheckTimeout + leg.DefaultHandshakeTimeout
)

// Host is what the agent runtime provides; *agent.Runtime implements it.
type Host interface {
	DeviceID() string
	P2PKey() (ed25519.PrivateKey, error)
	FetchP2PConfig(ctx context.Context) (*agent.P2PConfig, agent.P2PEndpointInfo, error)
	DialPurposeBridge(ctx context.Context, bridgeID, purpose string) (net.Conn, error)
	ServeCarrier(ctx context.Context, conn net.Conn, bridgeID, purpose string, env []string) error
	TerminalEnabled() bool
	UploadP2PAudit(ctx context.Context, grants []string, records []agent.P2PAuditRecord) (*agent.P2PAuditResponse, error)
}

// DialLegFunc builds a leg; leg.Dial in production.
type DialLegFunc func(ctx context.Context, cfg leg.Config, local, remote leg.Credentials, sig leg.Signaler) (leg.Leg, error)

// ProbeFunc classifies this side's NAT mapping against servers.
type ProbeFunc func(ctx context.Context, servers []string) natprobe.Mapping

// Options tunes the service; zero values select production behaviour.
type Options struct {
	Clock   clock.Clock
	DialLeg DialLegFunc
	Probe   ProbeFunc
	// LegConfig adjusts every leg config before dialing (test hook).
	LegConfig func(*leg.Config)
	// AuditInterval paces audit uploads; zero means AuditFlushInterval.
	AuditInterval time.Duration
	Logger        *slog.Logger
}

// Service runs the direct sessions of one agent runtime.
type Service struct {
	host   Host
	opts   Options
	clock  clock.Clock
	logger *slog.Logger
	audit  *auditor

	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	sessions map[string]*session
	config   *agent.P2PConfig
	endpoint agent.P2PEndpointInfo
	configAt time.Time
	closed   bool
	streamN  atomic.Uint64
	wg       sync.WaitGroup
}

// New returns a service for host.
func New(host Host, opts Options) *Service {
	if opts.Clock == nil {
		opts.Clock = clock.Real{}
	}
	if opts.DialLeg == nil {
		opts.DialLeg = leg.Dial
	}
	if opts.Probe == nil {
		opts.Probe = defaultProbe
	}
	if opts.AuditInterval <= 0 {
		opts.AuditInterval = AuditFlushInterval
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{host: host, opts: opts, clock: opts.Clock, logger: logger, sessions: make(map[string]*session)}
	s.audit = newAuditor(host, opts.Clock, opts.AuditInterval, logger)
	return s
}

// Install wires the service into an agent runtime: it serves the p2p-signal purpose (which also advertises the
// capability and registers the device key) and receives p2p_revoke frames.
func Install(runtime *agent.Runtime, opts Options) *Service {
	s := New(runtime, opts)
	runtime.RegisterPurposeHandler(agent.PurposeP2PSignal, s.ServeSignal)
	runtime.SetP2PRevokeHandler(s.Revoke)
	runtime.SetP2PRevokeAllHandler(s.RevokeAll)
	return s
}

func defaultProbe(ctx context.Context, servers []string) natprobe.Mapping {
	if len(servers) == 0 {
		return natprobe.Unknown
	}
	result, err := natprobe.Probe(ctx, servers, natprobe.Options{})
	if err != nil {
		// No server resolves: no reflexive address can be learned either.
		return natprobe.Blocked
	}
	return result.Mapping
}

// runContext is the service's lifetime context: the first signal handler's runtime context, so legs outlive the
// signal bridge that built them and still end with the runtime.
func (s *Service) runContext(ctx context.Context) (context.Context, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false
	}
	if s.ctx == nil {
		s.ctx, s.cancel = context.WithCancel(context.WithoutCancel(ctx))
		context.AfterFunc(ctx, s.Close)
		s.audit.start(s.ctx)
	}
	return s.ctx, true
}

// Close ends every direct session and flushes nothing further; streams fall back to the relay.
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	sessions := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	cancel := s.cancel
	s.mu.Unlock()
	for _, sess := range sessions {
		sess.end(signal.CloseShutdown)
	}
	if cancel != nil {
		cancel()
	}
	s.audit.stop()
}

// Wait blocks until every goroutine the service started returned (tests).
func (s *Service) Wait() {
	s.wg.Wait()
	s.audit.wait()
}

// Sessions reports the live direct sessions.
func (s *Service) Sessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// SessionExpiry reports when the grant the live session of grantID currently holds expires.
func (s *Service) SessionExpiry(grantID string) (time.Time, bool) {
	s.mu.Lock()
	sess := s.sessions[grantID]
	s.mu.Unlock()
	if sess == nil {
		return time.Time{}, false
	}
	_, claims := sess.currentGrant()
	return time.UnixMilli(claims.ExpiresAt), true
}

// Revoke closes the session of grantID at once: its streams' carriers fail, and the clients resume them on the
// relay while the stream server retains them.
func (s *Service) Revoke(grantID, reason string) {
	s.mu.Lock()
	sess := s.sessions[grantID]
	s.mu.Unlock()
	if sess == nil {
		s.logger.Debug("p2p_revoke for no live session", "grant_id", grantID, "reason", reason)
		return
	}
	s.logger.Info("direct session revoked", "grant_id", grantID, "reason", reason)
	sess.end(signal.CloseRevoked)
}

// RevokeAll closes every live direct session the way Revoke closes one, for a runtime whose owner became
// unavailable or whose device was revoked: the clients resume their streams on the relay, where the server
// decides again. The service keeps accepting offers, so direct connections return once the server grants them.
func (s *Service) RevokeAll(reason string) {
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	if len(sessions) == 0 {
		return
	}
	s.logger.Info("direct sessions revoked", "sessions", len(sessions), "reason", reason)
	var ended sync.WaitGroup
	for _, sess := range sessions {
		ended.Go(func() { sess.end(signal.CloseRevoked) })
	}
	ended.Wait()
}

func (s *Service) loadConfig(ctx context.Context) (*agent.P2PConfig, agent.P2PEndpointInfo, error) {
	now := s.clock.Now()
	s.mu.Lock()
	if s.config != nil && now.Sub(s.configAt) < ConfigTTL {
		config, endpoint := s.config, s.endpoint
		s.mu.Unlock()
		return config, endpoint, nil
	}
	s.mu.Unlock()
	config, endpoint, err := s.host.FetchP2PConfig(ctx)
	if err != nil {
		return nil, agent.P2PEndpointInfo{}, err
	}
	s.mu.Lock()
	s.config, s.endpoint, s.configAt = config, endpoint, now
	s.mu.Unlock()
	return config, endpoint, nil
}

// verify checks token for this device at now: purpose "" checks the session itself (any scope), otherwise
// purpose must be in scope. It refetches the trusted keys once when the signing key is unknown (a rotation).
func (s *Service) verify(ctx context.Context, token string, agentPub, clientPub ed25519.PublicKey, purpose string) (*grant.Claims, error) {
	config, _, err := s.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	claims, err := s.verifyWith(config.GrantKeys, token, agentPub, clientPub, purpose)
	if errors.Is(err, grant.ErrUnknownKey) {
		s.mu.Lock()
		s.config = nil
		s.mu.Unlock()
		if config, _, err = s.loadConfig(ctx); err != nil {
			return nil, err
		}
		claims, err = s.verifyWith(config.GrantKeys, token, agentPub, clientPub, purpose)
	}
	return claims, err
}

func (s *Service) verifyWith(keys []grant.PublicKey, token string, agentPub, clientPub ed25519.PublicKey, purpose string) (*grant.Claims, error) {
	if purpose == "" {
		parsed, err := grant.Parse(token, keys)
		if err != nil {
			return nil, err
		}
		purpose = parsed.Scope[0]
	}
	return grant.VerifyAgent(token, keys, s.clock.Now(), s.host.DeviceID(), agentPub, clientPub, purpose)
}

// ServeSignal is the p2p-signal purpose handler: one bridge either opens a session (offer) or re-attaches to a
// live one (attach), then carries grant refreshes, relay pings and close frames until it drops. A dropped
// signal bridge does not end the session.
func (s *Service) ServeSignal(ctx context.Context, bridgeID string) error {
	runCtx, ok := s.runContext(ctx)
	if !ok {
		return errors.New("direct connections are shut down")
	}
	conn, err := s.host.DialPurposeBridge(ctx, bridgeID, agent.PurposeP2PSignal)
	if err != nil {
		return err
	}
	sc := signal.NewConn(conn)
	defer func() { _ = sc.Close() }()
	stop := context.AfterFunc(runCtx, func() { _ = sc.Close() })
	defer stop()
	_ = conn.SetReadDeadline(time.Now().Add(OfferTimeout))
	first, err := sc.Read()
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		return fmt.Errorf("read offer: %w", err)
	}
	switch first.Type {
	case signal.TypeOffer:
		return s.serveOffer(runCtx, sc, first)
	case signal.TypeAttach:
		return s.serveAttach(runCtx, sc, first)
	default:
		_ = sc.Write(runCtx, signal.Frame{Type: signal.TypeClose, Reason: signal.CloseRefused})
		return fmt.Errorf("unexpected first signal frame %q", first.Type)
	}
}

func (s *Service) refuse(ctx context.Context, sc *signal.Conn, reason string, err error) error {
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = sc.Write(writeCtx, signal.Frame{Type: signal.TypeClose, Reason: signal.CloseRefused})
	s.logger.Info("direct connection refused", "reason", reason, "error", err)
	return fmt.Errorf("%s: %w", reason, err)
}

func (s *Service) serveOffer(ctx context.Context, sc *signal.Conn, offer signal.Frame) error {
	if !s.host.TerminalEnabled() {
		return s.refuse(ctx, sc, "terminal_disabled", errors.New("terminal access is disabled"))
	}
	config, endpoint, err := s.loadConfig(ctx)
	switch {
	case errors.Is(err, agent.ErrP2PKeyInsecureEndpoint):
		return s.refuse(ctx, sc, "insecure", err)
	case err != nil:
		return s.refuse(ctx, sc, "config", err)
	case !config.Enabled:
		return s.refuse(ctx, sc, "server_disabled", errors.New("the server turned direct connections off"))
	case endpoint.Proxied:
		return s.refuse(ctx, sc, "proxy", errors.New("the endpoint uses a proxy"))
	case endpoint.Insecure:
		return s.refuse(ctx, sc, "insecure", errors.New("the endpoint is plain HTTP or skips TLS verification"))
	}
	key, err := s.host.P2PKey()
	if err != nil {
		return s.refuse(ctx, sc, "device_key", err)
	}
	agentPub := key.Public().(ed25519.PublicKey)
	clientPub, err := grant.ParseKey(offer.ClientPub)
	if err != nil {
		return s.refuse(ctx, sc, "client_key", err)
	}
	claims, err := s.verify(ctx, offer.Grant, agentPub, ed25519.PublicKey(clientPub), "")
	if err != nil {
		return s.refuse(ctx, sc, "grant", err)
	}
	if len(offer.Ufrag) < 4 || len(offer.Pwd) < 22 {
		return s.refuse(ctx, sc, "credentials", errors.New("short ICE credentials"))
	}
	sess := newSession(s, claims, offer.Grant, sc)
	if !s.register(sess) {
		return s.refuse(ctx, sc, "shutdown", errors.New("shutting down"))
	}
	candidates := signal.NewCandidates(sc)
	readerDone := make(chan error, 1)
	dialCtx, cancel := context.WithTimeout(ctx, DialBudget)
	defer cancel()
	go func() {
		err := sess.readSignal(ctx, sc, candidates)
		// A signal bridge that drops before the leg is up ends the attempt.
		cancel()
		readerDone <- err
	}()
	stun := agent.STUNServers(config, endpoint.URL, endpoint.Proxied)
	local := s.opts.Probe(dialCtx, stun)
	creds := leg.NewCredentials()
	answer := signal.Frame{Type: signal.TypeAnswer, Ufrag: creds.Ufrag, Pwd: creds.Pwd, NAT: string(local)}
	answer.AgentPubSig = signal.AnswerSignature(key, claims.GrantID, offer.Ufrag, answer)
	if err := sc.Write(dialCtx, answer); err != nil {
		sess.end("")
		return fmt.Errorf("send answer: %w", err)
	}
	cfg := leg.Config{
		STUN: stun, Controlling: false, LocalKey: key, PeerKey: ed25519.PublicKey(claims.ClientPub),
		HostOnly: natprobe.HostOnly(local, natprobe.Mapping(offer.NAT)),
	}
	if config.Timeouts.GatherMS > 0 {
		cfg.GatherTimeout = time.Duration(config.Timeouts.GatherMS) * time.Millisecond
	}
	if config.Timeouts.CheckMS > 0 && !cfg.HostOnly {
		cfg.CheckTimeout = time.Duration(config.Timeouts.CheckMS) * time.Millisecond
	}
	if s.opts.LegConfig != nil {
		s.opts.LegConfig(&cfg)
	}
	l, err := s.opts.DialLeg(dialCtx, cfg, creds, leg.Credentials{Ufrag: offer.Ufrag, Pwd: offer.Pwd}, candidates)
	if err != nil {
		sess.end("")
		<-readerDone
		return fmt.Errorf("direct leg: %w", err)
	}
	if !sess.attachLeg(l) {
		_ = l.Close()
		<-readerDone
		return errors.New("session ended while the leg was built")
	}
	s.logger.Info("direct session established", "grant_id", claims.GrantID, "host_only", cfg.HostOnly)
	return <-readerDone
}

func (s *Service) serveAttach(ctx context.Context, sc *signal.Conn, attach signal.Frame) error {
	key, err := s.host.P2PKey()
	if err != nil {
		return s.refuse(ctx, sc, "device_key", err)
	}
	clientPub, err := grant.ParseKey(attach.ClientPub)
	if err != nil {
		return s.refuse(ctx, sc, "client_key", err)
	}
	claims, err := s.verify(ctx, attach.Grant, key.Public().(ed25519.PublicKey), ed25519.PublicKey(clientPub), "")
	if err != nil {
		return s.refuse(ctx, sc, "grant", err)
	}
	s.mu.Lock()
	sess := s.sessions[claims.GrantID]
	s.mu.Unlock()
	if sess == nil || !sess.sameClient(claims) {
		return s.refuse(ctx, sc, "unknown_session", errors.New("no live session for the grant"))
	}
	sess.setGrant(attach.Grant, claims)
	sess.setSignal(sc)
	return sess.readSignal(ctx, sc, nil)
}

// register adds sess, ending a previous session of the same grant (the client started over).
func (s *Service) register(sess *session) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	previous := s.sessions[sess.grantID]
	s.sessions[sess.grantID] = sess
	s.mu.Unlock()
	if previous != nil {
		previous.end(signal.CloseRefused)
	}
	return true
}

func (s *Service) unregister(sess *session) {
	s.mu.Lock()
	if s.sessions[sess.grantID] == sess {
		delete(s.sessions, sess.grantID)
	}
	s.mu.Unlock()
}

func (s *Service) nextStreamID(grantID string) string {
	return "p2p-" + grantID + "-" + strconv.FormatUint(s.streamN.Add(1), 10)
}

// streamEnv is the SSH session variables of a direct terminal: the peer and local addresses of the leg.
func streamEnv(conn net.Conn) []string {
	return supervisor.SSHConnectionEnv(conn.RemoteAddr(), conn.LocalAddr())
}
