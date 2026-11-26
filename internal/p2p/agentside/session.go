package agentside

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/p2p/clock"
	"github.com/Hello-CTF/NexTerm/internal/p2p/grant"
	"github.com/Hello-CTF/NexTerm/internal/p2p/leg"
	"github.com/Hello-CTF/NexTerm/internal/p2p/signal"
)

// session is one direct session: the leg built for a grant, its current grant token and signal bridge.
type session struct {
	svc       *Service
	grantID   string
	clientPub ed25519.PublicKey
	agentPub  ed25519.PublicKey

	mu      sync.Mutex
	token   string
	claims  *grant.Claims
	sig     *signal.Conn
	leg     leg.Leg
	expiry  clock.Timer
	ended   bool
	endOnce sync.Once
	done    chan struct{}
}

func newSession(svc *Service, claims *grant.Claims, token string, sc *signal.Conn) *session {
	sess := &session{
		svc: svc, grantID: claims.GrantID, clientPub: ed25519.PublicKey(claims.ClientPub), agentPub: ed25519.PublicKey(claims.AgentPub),
		token: token, claims: claims, sig: sc, done: make(chan struct{}),
	}
	sess.scheduleExpiry(claims)
	return sess
}

func (sess *session) sameClient(claims *grant.Claims) bool {
	return bytes.Equal(claims.ClientPub, sess.clientPub) && bytes.Equal(claims.AgentPub, sess.agentPub)
}

// scheduleExpiry ends the session Grace after claims expires unless a renewed grant replaced it.
func (sess *session) scheduleExpiry(claims *grant.Claims) {
	expires := time.UnixMilli(claims.ExpiresAt).Add(Grace)
	wait := expires.Sub(sess.svc.clock.Now())
	timer := sess.svc.clock.AfterFunc(max(wait, 0), func() {
		sess.mu.Lock()
		current := sess.claims
		sess.mu.Unlock()
		if current == claims {
			sess.svc.logger.Info("direct session grant expired", "grant_id", sess.grantID)
			go sess.end(signal.CloseExpired)
		}
	})
	sess.mu.Lock()
	previous := sess.expiry
	sess.expiry = timer
	sess.mu.Unlock()
	if previous != nil {
		previous.Stop()
	}
}

// setGrant installs a renewed grant of this session.
func (sess *session) setGrant(token string, claims *grant.Claims) {
	sess.mu.Lock()
	if sess.ended {
		sess.mu.Unlock()
		return
	}
	sess.token, sess.claims = token, claims
	sess.mu.Unlock()
	sess.scheduleExpiry(claims)
}

func (sess *session) currentGrant() (string, *grant.Claims) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.token, sess.claims
}

func (sess *session) setSignal(sc *signal.Conn) {
	sess.mu.Lock()
	previous := sess.sig
	sess.sig = sc
	ended := sess.ended
	sess.mu.Unlock()
	if previous != nil && previous != sc {
		_ = previous.Close()
	}
	if ended {
		_ = sc.Close()
	}
}

// attachLeg records the built leg and starts accepting its streams; false when the session already ended.
func (sess *session) attachLeg(l leg.Leg) bool {
	sess.mu.Lock()
	if sess.ended {
		sess.mu.Unlock()
		return false
	}
	sess.leg = l
	sess.mu.Unlock()
	svc := sess.svc
	svc.mu.Lock()
	if svc.closed {
		svc.mu.Unlock()
		sess.end("")
		return false
	}
	// Added under the service lock, so a Wait after Close sees every goroutine of a session it did not end.
	svc.wg.Add(2)
	svc.mu.Unlock()
	go func() {
		defer svc.wg.Done()
		select {
		case <-l.Done():
			sess.end("")
		case <-sess.done:
		}
	}()
	go func() {
		defer svc.wg.Done()
		sess.acceptLoop(l)
	}()
	return true
}

// end closes the session: the leg (so every direct carrier fails and its stream waits for a RESUME on the
// relay) and the signal bridge, after telling the client why when reason is set.
func (sess *session) end(reason string) {
	sess.endOnce.Do(func() {
		sess.mu.Lock()
		sess.ended = true
		sc, l, expiry := sess.sig, sess.leg, sess.expiry
		sess.mu.Unlock()
		close(sess.done)
		if expiry != nil {
			expiry.Stop()
		}
		if sc != nil && reason != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = sc.Write(ctx, signal.Frame{Type: signal.TypeClose, Reason: reason})
			cancel()
		}
		if l != nil {
			_ = l.Close()
		}
		if sc != nil {
			_ = sc.Close()
		}
		sess.svc.unregister(sess)
	})
}

// readSignal serves one signal bridge of the session until it drops or the client closes the session.
func (sess *session) readSignal(ctx context.Context, sc *signal.Conn, candidates *signal.Candidates) error {
	if candidates != nil {
		defer candidates.Finish()
	}
	for {
		frame, err := sc.Read()
		if err != nil {
			return nil
		}
		switch frame.Type {
		case signal.TypeCandidate:
			if candidates != nil {
				candidates.Push(frame.Candidate)
			}
		case signal.TypePing:
			_ = sc.Write(ctx, signal.Frame{Type: signal.TypePong, Seq: frame.Seq})
		case signal.TypeGrantRefresh:
			claims, err := sess.svc.verify(ctx, frame.Grant, sess.agentPub, sess.clientPub, "")
			if err != nil || claims.GrantID != sess.grantID {
				sess.svc.logger.Info("grant refresh refused", "grant_id", sess.grantID, "error", err)
				continue
			}
			sess.setGrant(frame.Grant, claims)
		case signal.TypeClose:
			sess.end("")
			return nil
		}
	}
}

func (sess *session) acceptLoop(l leg.Leg) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-sess.done:
		case <-l.Done():
		}
		cancel()
	}()
	for {
		conn, err := l.AcceptStream(ctx)
		if err != nil {
			return
		}
		sess.svc.wg.Add(1)
		go func() {
			defer sess.svc.wg.Done()
			sess.serveStream(conn)
		}()
	}
}

// serveStream checks one direct stream against the grant and hands it to the runtime; it records the stream for
// the audit once its carrier closes.
func (sess *session) serveStream(conn net.Conn) {
	svc := sess.svc
	started := svc.clock.Now()
	_ = conn.SetReadDeadline(time.Now().Add(PurposeTimeout))
	purpose, err := signal.ReadPurpose(conn)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		return
	}
	token, _ := sess.currentGrant()
	ctx := svc.ctx
	record := agent.P2PAuditRecord{GrantID: sess.grantID, Purpose: purpose, StartedAt: started.UnixMilli()}
	if _, err := svc.verify(ctx, token, sess.agentPub, sess.clientPub, purpose); err != nil {
		record.EndedAt, record.Outcome, record.Reason = record.StartedAt, "deny", denyReason(err)
		_ = conn.Close()
		svc.audit.add(token, record)
		return
	}
	if !svc.host.TerminalEnabled() {
		record.EndedAt, record.Outcome, record.Reason = record.StartedAt, "deny", "terminal_disabled"
		_ = conn.Close()
		svc.audit.add(token, record)
		return
	}
	counted := newCountingConn(conn)
	err = svc.host.ServeCarrier(ctx, counted, svc.nextStreamID(sess.grantID), purpose, streamEnv(conn))
	if err != nil {
		svc.logger.Info("direct stream refused", "grant_id", sess.grantID, "purpose", purpose, "error", err)
	}
	select {
	case <-counted.closed:
	case <-sess.done:
		_ = counted.Close()
	}
	record.EndedAt = svc.clock.Now().UnixMilli()
	record.BytesIn, record.BytesOut = counted.in.Load(), counted.out.Load()
	if err != nil {
		record.Outcome, record.Reason = "deny", "refused"
	}
	svc.audit.add(token, record)
}

func denyReason(err error) string {
	switch {
	case errors.Is(err, grant.ErrScope):
		return "out_of_scope"
	case errors.Is(err, grant.ErrExpired):
		return "grant_expired"
	}
	return "grant_invalid"
}

// countingConn counts the bytes of a direct carrier and reports its close.
type countingConn struct {
	net.Conn
	in, out   atomic.Int64
	closed    chan struct{}
	closeOnce sync.Once
}

func newCountingConn(conn net.Conn) *countingConn {
	return &countingConn{Conn: conn, closed: make(chan struct{})}
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.in.Add(int64(n))
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.out.Add(int64(n))
	return n, err
}

func (c *countingConn) Close() error {
	err := c.Conn.Close()
	c.closeOnce.Do(func() { close(c.closed) })
	return err
}
