package client

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/p2p/grant"
	"github.com/Hello-CTF/NexTerm/internal/p2p/leg"
	"github.com/Hello-CTF/NexTerm/internal/p2p/natprobe"
	"github.com/Hello-CTF/NexTerm/internal/p2p/signal"
)

// directSession is an established leg with its grant and signal bridge.
type directSession struct {
	leg       leg.Leg
	agentPub  ed25519.PublicKey
	localNAT  natprobe.Mapping
	remoteNAT natprobe.Mapping
	stunOK    bool

	mu          sync.Mutex
	grant       *Grant
	sig         *signal.Conn
	closeReason string
	closed      chan struct{}
	closeOnce   sync.Once
}

func (s *directSession) currentGrant() *Grant {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.grant
}

func (s *directSession) setGrant(g *Grant) {
	s.mu.Lock()
	s.grant = g
	s.mu.Unlock()
}

func (s *directSession) signal() *signal.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sig
}

func (s *directSession) setSignal(sc *signal.Conn) {
	s.mu.Lock()
	previous := s.sig
	s.sig = sc
	s.mu.Unlock()
	if previous != nil && previous != sc {
		_ = previous.Close()
	}
}

func (s *directSession) setCloseReason(reason string) {
	s.mu.Lock()
	s.closeReason = reason
	s.mu.Unlock()
}

func (s *directSession) closeReasonText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeReason
}

func (s *directSession) dead() bool {
	select {
	case <-s.leg.Done():
		return true
	case <-s.closed:
		return true
	default:
		return false
	}
}

// close tells the agent and closes the leg and the signal bridge.
func (s *directSession) close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		if sc := s.signal(); sc != nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = sc.Write(ctx, signal.Frame{Type: signal.TypeClose})
			cancel()
			_ = sc.Close()
		}
		_ = s.leg.Close()
	})
}

// allows reports whether the grant covers purpose; other streams stay on the relay.
func (s *directSession) allows(purpose string) bool {
	return slices.Contains(s.currentGrant().Scope, purpose)
}

// openStream opens a direct stream for purpose, preamble written.
func (s *directSession) openStream(ctx context.Context, purpose string) (net.Conn, error) {
	if !s.allows(purpose) {
		return nil, errors.New("purpose outside the grant scope")
	}
	conn, err := s.leg.OpenStream(ctx)
	if err != nil {
		return nil, err
	}
	if err := signal.WritePurpose(conn, purpose); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func clientPubText(key ed25519.PrivateKey) string {
	return grant.Key(key.Public().(ed25519.PublicKey)).String()
}

// connect runs one attempt: config, device key state, grant, signal bridge, NAT probe, offer/answer and the leg.
// It returns the session, or nil and the status reason of the failure.
func (d *device) connect() (*directSession, string) {
	ctx, cancel := context.WithTimeout(d.ctx, DialBudget)
	defer cancel()
	m := d.m
	config, err := m.loadConfig(ctx)
	if err != nil {
		return nil, grantReason(err)
	}
	d.mu.Lock()
	d.serverOn = config.Enabled
	d.mu.Unlock()
	if !config.Enabled {
		return nil, ReasonServerDisabled
	}
	state, err := m.backend.DeviceKeyState(ctx, d.id)
	if err != nil {
		return nil, grantReason(err)
	}
	d.mu.Lock()
	d.deviceOn = state == agent.P2PKeyStateBound
	d.mu.Unlock()
	switch state {
	case agent.P2PKeyStateBound:
	case agent.P2PKeyStatePending:
		return nil, ReasonKeyPending
	default:
		return nil, ReasonDeviceUnsupported
	}
	endpoint, err := m.backend.Endpoint(ctx)
	if err != nil {
		return nil, grantReason(err)
	}
	if reason := endpointReason(endpoint); reason != "" {
		return nil, reason
	}
	g, err := m.backend.Grant(ctx, d.id, m.key.Public().(ed25519.PublicKey))
	if err != nil {
		return nil, grantReason(err)
	}
	agentPub := ed25519.PublicKey(g.AgentPub)
	if len(agentPub) != ed25519.PublicKeySize {
		return nil, ReasonConnectFailed
	}
	stun := agent.STUNServers(config, endpoint.URL, false)
	local := m.opts.Probe(ctx, stun)
	stunOK := len(stun) > 0 && local != natprobe.Blocked
	d.mu.Lock()
	d.localNAT, d.stunOK = string(local), stunOK
	d.mu.Unlock()

	conn, err := m.backend.DialSignal(ctx, d.id)
	if err != nil {
		return nil, grantReason(err)
	}
	sc := signal.NewConn(conn)
	sess := &directSession{agentPub: agentPub, localNAT: local, stunOK: stunOK, grant: g, sig: sc, closed: make(chan struct{})}
	creds := leg.NewCredentials()
	if err := sc.Write(ctx, signal.Frame{Type: signal.TypeOffer, Grant: g.Grant, ClientPub: clientPubText(m.key), Ufrag: creds.Ufrag, Pwd: creds.Pwd, NAT: string(local)}); err != nil {
		_ = sc.Close()
		return nil, ReasonConnectFailed
	}
	answers := make(chan signal.Frame, 1)
	candidates := signal.NewCandidates(sc)
	go d.readSignal(sess, sc, answers, candidates)
	var answer signal.Frame
	select {
	case answer = <-answers:
	case <-ctx.Done():
		_ = sc.Close()
		return nil, ReasonConnectFailed
	}
	if answer.Type == signal.TypeClose {
		_ = sc.Close()
		return nil, ReasonServerDisabled
	}
	if !signal.VerifyAnswer(agentPub, g.GrantID, creds.Ufrag, answer) {
		_ = sc.Close()
		return nil, ReasonConnectFailed
	}
	remote := natprobe.Mapping(answer.NAT)
	sess.remoteNAT = remote
	d.mu.Lock()
	d.remoteNAT = string(remote)
	d.mu.Unlock()
	cfg := leg.Config{STUN: stun, Controlling: true, LocalKey: m.key, PeerKey: agentPub, HostOnly: natprobe.HostOnly(local, remote)}
	if config.Timeouts.GatherMS > 0 {
		cfg.GatherTimeout = time.Duration(config.Timeouts.GatherMS) * time.Millisecond
	}
	if config.Timeouts.CheckMS > 0 && !cfg.HostOnly {
		cfg.CheckTimeout = time.Duration(config.Timeouts.CheckMS) * time.Millisecond
	}
	if m.opts.LegConfig != nil {
		m.opts.LegConfig(&cfg)
	}
	l, err := m.opts.DialLeg(ctx, cfg, creds, leg.Credentials{Ufrag: answer.Ufrag, Pwd: answer.Pwd}, candidates)
	if err != nil {
		_ = sc.Close()
		if cfg.HostOnly && errors.Is(err, leg.ErrICE) {
			return nil, ReasonNATBlocked
		}
		m.logger.Info("direct leg failed", "device_id", d.id, "host_only", cfg.HostOnly, "error", err)
		return nil, ReasonConnectFailed
	}
	sess.leg = l
	return sess, ""
}

// readSignal reads one signal bridge of sess: the answer while connecting, then candidates, pongs and close.
func (d *device) readSignal(sess *directSession, sc *signal.Conn, answers chan<- signal.Frame, candidates *signal.Candidates) {
	if candidates != nil {
		defer candidates.Finish()
	}
	for {
		frame, err := sc.Read()
		if err != nil {
			if answers != nil {
				select {
				case answers <- signal.Frame{Type: signal.TypeClose}:
				default:
				}
			}
			d.post(event{kind: evSignalDown, sess: sess, sig: sc})
			return
		}
		switch frame.Type {
		case signal.TypeAnswer, signal.TypeClose:
			if answers != nil {
				select {
				case answers <- frame:
					answers = nil
					if frame.Type == signal.TypeClose {
						return
					}
					continue
				default:
				}
			}
			if frame.Type == signal.TypeClose {
				d.post(event{kind: evSignalClose, sess: sess, reason: frame.Reason})
				return
			}
		case signal.TypeCandidate:
			if candidates != nil {
				candidates.Push(frame.Candidate)
			}
		case signal.TypePong:
			d.post(event{kind: evPong, seq: frame.Seq})
		}
	}
}
