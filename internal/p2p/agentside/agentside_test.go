package agentside

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/p2p/clock"
	"github.com/Hello-CTF/NexTerm/internal/p2p/grant"
	"github.com/Hello-CTF/NexTerm/internal/p2p/leg"
	"github.com/Hello-CTF/NexTerm/internal/p2p/legtest"
	"github.com/Hello-CTF/NexTerm/internal/p2p/natprobe"
	"github.com/Hello-CTF/NexTerm/internal/p2p/signal"
)

var epoch = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

type fakeHost struct {
	signing grant.SigningKey
	device  ed25519.PrivateKey
	clock   *clock.Fake

	mu       sync.Mutex
	bridges  map[string]net.Conn
	served   []string
	uploads  [][]agent.P2PAuditRecord
	grants   [][]string
	failUp   error
	terminal bool
}

func (h *fakeHost) DeviceID() string                    { return "dev-1" }
func (h *fakeHost) P2PKey() (ed25519.PrivateKey, error) { return h.device, nil }
func (h *fakeHost) TerminalEnabled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.terminal
}

func (h *fakeHost) FetchP2PConfig(context.Context) (*agent.P2PConfig, agent.P2PEndpointInfo, error) {
	return &agent.P2PConfig{Enabled: true, GrantKeys: []grant.PublicKey{h.signing.Public()}}, agent.P2PEndpointInfo{URL: "https://nx.example.com"}, nil
}

func (h *fakeHost) DialPurposeBridge(_ context.Context, bridgeID, _ string) (net.Conn, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	conn := h.bridges[bridgeID]
	if conn == nil {
		return nil, errors.New("unknown bridge")
	}
	delete(h.bridges, bridgeID)
	return conn, nil
}

// ServeCarrier echoes until the carrier closes.
func (h *fakeHost) ServeCarrier(_ context.Context, conn net.Conn, _ string, purpose string, _ []string) error {
	h.mu.Lock()
	h.served = append(h.served, purpose)
	h.mu.Unlock()
	go func() {
		_, _ = io.Copy(conn, conn)
		_ = conn.Close()
	}()
	return nil
}

func (h *fakeHost) UploadP2PAudit(_ context.Context, grants []string, records []agent.P2PAuditRecord) (*agent.P2PAuditResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failUp != nil {
		return nil, h.failUp
	}
	h.uploads = append(h.uploads, append([]agent.P2PAuditRecord(nil), records...))
	h.grants = append(h.grants, append([]string(nil), grants...))
	return &agent.P2PAuditResponse{Accepted: len(records), Rejected: []agent.P2PAuditRejection{}}, nil
}

type fixture struct {
	t      *testing.T
	host   *fakeHost
	svc    *Service
	net    *legtest.Net
	client ed25519.PrivateKey
	ctx    context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	signing, err := grant.GenerateSigningKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, device, _ := ed25519.GenerateKey(rand.Reader)
	_, client, _ := ed25519.GenerateKey(rand.Reader)
	fake := clock.NewFake(epoch)
	host := &fakeHost{signing: signing, device: device, clock: fake, bridges: make(map[string]net.Conn), terminal: true}
	network := legtest.NewNet()
	svc := New(host, Options{
		Clock: fake, DialLeg: network.Dial, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Probe: func(context.Context, []string) natprobe.Mapping { return natprobe.Independent },
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		svc.Close()
		cancel()
		svc.Wait()
	})
	return &fixture{t: t, host: host, svc: svc, net: network, client: client, ctx: ctx}
}

func (f *fixture) issue(scope []string, ttl time.Duration, mutate func(*grant.Claims)) string {
	f.t.Helper()
	now := f.host.clock.Now().UnixMilli()
	claims := grant.Claims{
		GrantID: "grant-1", DeviceID: "dev-1", OwnerID: "owner", ClientPub: grant.Key(f.client.Public().(ed25519.PublicKey)),
		AgentPub: grant.Key(f.host.device.Public().(ed25519.PublicKey)), Scope: scope, IssuedAt: now, ExpiresAt: now + ttl.Milliseconds(),
	}
	if mutate != nil {
		mutate(&claims)
	}
	token, err := grant.Issue(f.host.signing, claims)
	if err != nil {
		f.t.Fatal(err)
	}
	return token
}

var bridgeSeq struct {
	sync.Mutex
	n int
}

// signalBridge opens a signal bridge to the service and returns the client end.
func (f *fixture) signalBridge() *signal.Conn {
	client, agentEnd := net.Pipe()
	bridgeSeq.Lock()
	bridgeSeq.n++
	id := "b" + strconv.Itoa(bridgeSeq.n)
	bridgeSeq.Unlock()
	f.host.mu.Lock()
	f.host.bridges[id] = agentEnd
	f.host.mu.Unlock()
	go func() { _ = f.svc.ServeSignal(f.ctx, id) }()
	return signal.NewConn(client)
}

// open runs an offer and returns the client leg, or the agent's close frame when it refused.
func (f *fixture) open(token string) (leg.Leg, *signal.Conn, *signal.Frame) {
	f.t.Helper()
	sc := f.signalBridge()
	creds := leg.NewCredentials()
	offer := signal.Frame{Type: signal.TypeOffer, Grant: token, ClientPub: grant.Key(f.client.Public().(ed25519.PublicKey)).String(), Ufrag: creds.Ufrag, Pwd: creds.Pwd, NAT: "independent"}
	if err := sc.Write(f.ctx, offer); err != nil {
		f.t.Fatal(err)
	}
	answer, err := sc.Read()
	if err != nil {
		f.t.Fatal(err)
	}
	if answer.Type == signal.TypeClose {
		return nil, sc, &answer
	}
	if !signal.VerifyAnswer(f.host.device.Public().(ed25519.PublicKey), "grant-1", creds.Ufrag, answer) {
		f.t.Fatal("answer signature invalid")
	}
	go func() {
		for {
			if _, err := sc.Read(); err != nil {
				return
			}
		}
	}()
	l, err := f.net.Dial(f.ctx, leg.Config{Controlling: true}, creds, leg.Credentials{Ufrag: answer.Ufrag, Pwd: answer.Pwd}, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	f.waitFor("session", func() bool { return f.svc.Sessions() == 1 })
	return l, sc, nil
}

func (f *fixture) waitFor(what string, cond func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			f.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// stream opens a direct stream for purpose and reports whether the agent serves it (it echoes) or closes it.
func stream(t *testing.T, l leg.Leg, purpose string) (net.Conn, bool) {
	t.Helper()
	conn, err := l.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := signal.WritePurpose(conn, purpose); err != nil {
		return conn, false
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		return conn, false
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(conn, buffer); err != nil {
		return conn, false
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, string(buffer) == "ping"
}

func (f *fixture) flushAudit() []agent.P2PAuditRecord {
	f.t.Helper()
	f.host.clock.Advance(AuditFlushInterval)
	var records []agent.P2PAuditRecord
	f.waitFor("audit upload", func() bool {
		f.host.mu.Lock()
		defer f.host.mu.Unlock()
		records = nil
		for _, upload := range f.host.uploads {
			records = append(records, upload...)
		}
		return len(records) > 0 && f.svc.audit.pendingCount() == 0
	})
	return records
}

// A stream whose purpose the grant does not cover is refused and audited as denied; covered ones are served.
func TestOutOfScopePurposeRejected(t *testing.T) {
	f := newFixture(t)
	l, _, refused := f.open(f.issue([]string{"terminal"}, grant.DefaultTTL, nil))
	if refused != nil {
		t.Fatalf("offer refused: %+v", refused)
	}
	if _, ok := stream(t, l, "forward"); ok {
		t.Fatal("an out-of-scope stream was served")
	}
	conn, ok := stream(t, l, "terminal")
	if !ok {
		t.Fatal("an in-scope stream was refused")
	}
	_ = conn.Close()
	f.waitFor("terminal stream closed on the agent", func() bool { return f.svc.audit.pendingCount() == 2 })
	records := f.flushAudit()
	var denied, allowed int
	for _, record := range records {
		switch {
		case record.Purpose == "forward" && record.Outcome == "deny" && record.Reason == "out_of_scope":
			denied++
		case record.Purpose == "terminal" && record.Outcome == "" && record.BytesIn >= 4 && record.BytesOut >= 4:
			allowed++
		}
	}
	if denied != 1 || allowed != 1 {
		t.Fatalf("audit records = %+v", records)
	}
	f.host.mu.Lock()
	served := append([]string(nil), f.host.served...)
	f.host.mu.Unlock()
	if len(served) != 1 || served[0] != "terminal" {
		t.Fatalf("served purposes = %v", served)
	}
}

// After expiry new streams are refused while existing ones keep working for the grace; then the session ends.
// A renewed grant replaces the expiry.
func TestGrantExpiryGraceAndRenewal(t *testing.T) {
	f := newFixture(t)
	l, sc, refused := f.open(f.issue([]string{"terminal"}, grant.DefaultTTL, nil))
	if refused != nil {
		t.Fatalf("offer refused: %+v", refused)
	}
	existing, ok := stream(t, l, "terminal")
	if !ok {
		t.Fatal("stream refused")
	}
	f.host.clock.Advance(grant.DefaultTTL)
	if _, ok := stream(t, l, "terminal"); ok {
		t.Fatal("a new stream was served under an expired grant")
	}
	if _, err := existing.Write([]byte("more")); err != nil {
		t.Fatalf("existing stream inside the grace: %v", err)
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(existing, buffer); err != nil {
		t.Fatalf("existing stream inside the grace: %v", err)
	}
	// A refresh now renews the session.
	if err := sc.Write(f.ctx, signal.Frame{Type: signal.TypeGrantRefresh, Grant: f.issue([]string{"terminal"}, grant.DefaultTTL, nil)}); err != nil {
		t.Fatal(err)
	}
	f.waitFor("refresh applied", func() bool {
		f.svc.mu.Lock()
		sess := f.svc.sessions["grant-1"]
		f.svc.mu.Unlock()
		_, claims := sess.currentGrant()
		return claims.ExpiresAt > f.host.clock.Now().UnixMilli()
	})
	f.host.clock.Advance(Grace + time.Second)
	if f.svc.Sessions() != 1 {
		t.Fatal("a renewed session ended at the old grant's grace")
	}
	if _, ok := stream(t, l, "terminal"); !ok {
		t.Fatal("a new stream was refused under the renewed grant")
	}
	// Without another renewal the session ends Grace after the new expiry.
	f.host.clock.Advance(grant.DefaultTTL - Grace - time.Second)
	if f.svc.Sessions() != 1 {
		t.Fatal("the session ended before its grace")
	}
	f.host.clock.Advance(Grace)
	f.waitFor("session ended", func() bool { return f.svc.Sessions() == 0 })
	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the leg survived the session")
	}
}

func TestOfferRefusals(t *testing.T) {
	f := newFixture(t)
	_, otherAgent, _ := ed25519.GenerateKey(rand.Reader)
	for name, token := range map[string]string{
		"other device": f.issue([]string{"terminal"}, grant.DefaultTTL, func(c *grant.Claims) { c.DeviceID = "dev-2" }),
		"other agent":  f.issue([]string{"terminal"}, grant.DefaultTTL, func(c *grant.Claims) { c.AgentPub = grant.Key(otherAgent.Public().(ed25519.PublicKey)) }),
		"expired": f.issue([]string{"terminal"}, time.Minute, func(c *grant.Claims) {
			c.IssuedAt -= 10 * time.Minute.Milliseconds()
			c.ExpiresAt -= 10 * time.Minute.Milliseconds()
		}),
		"garbage": "not-a-grant",
	} {
		_, _, refused := f.open(token)
		if refused == nil || refused.Reason != signal.CloseRefused {
			t.Fatalf("%s: offer accepted", name)
		}
	}
	if f.svc.Sessions() != 0 {
		t.Fatal("a refused offer left a session")
	}
}

func TestRevokeClosesSession(t *testing.T) {
	f := newFixture(t)
	l, sc, _ := f.open(f.issue([]string{"terminal"}, grant.DefaultTTL, nil))
	_ = sc
	f.svc.Revoke("unknown", "x")
	if f.svc.Sessions() != 1 {
		t.Fatal("an unknown grant revoke ended the session")
	}
	f.svc.Revoke("grant-1", "session_revoked")
	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the leg survived the revoke")
	}
	if f.svc.Sessions() != 0 {
		t.Fatal("the session survived the revoke")
	}
}

// RevokeAll ends every session, as when the owner becomes unavailable, and leaves the service accepting offers.
func TestRevokeAllClosesSessionsAndKeepsServing(t *testing.T) {
	f := newFixture(t)
	f.svc.RevokeAll("idle")
	l, _, _ := f.open(f.issue([]string{"terminal"}, grant.DefaultTTL, nil))
	f.svc.RevokeAll(agent.P2PRevokeOwnerUnavailable)
	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the leg survived RevokeAll")
	}
	if f.svc.Sessions() != 0 {
		t.Fatal("a session survived RevokeAll")
	}
	if _, _, refused := f.open(f.issue([]string{"terminal"}, grant.DefaultTTL, nil)); refused != nil {
		t.Fatalf("offer after RevokeAll refused: %+v", refused)
	}
}

// Batches respect the endpoint's limits and carry one token per grant id; failed uploads are retried.
func TestAuditBatching(t *testing.T) {
	f := newFixture(t)
	a := f.svc.audit
	a.start(f.ctx)
	f.host.mu.Lock()
	f.host.failUp = errors.New("server down")
	f.host.mu.Unlock()
	now := epoch.UnixMilli()
	for i := range 250 {
		grantID := fmt.Sprintf("g%d", i%20)
		a.add("token-"+grantID, agent.P2PAuditRecord{GrantID: grantID, Purpose: "terminal", StartedAt: now, EndedAt: now})
	}
	// A renewed token of g0 must wait for its own batch.
	a.add("token-g0-renewed", agent.P2PAuditRecord{GrantID: "g0", Purpose: "terminal", StartedAt: now, EndedAt: now})
	a.flush()
	if a.pendingCount() != 251 {
		t.Fatalf("pending after a failed upload = %d", a.pendingCount())
	}
	f.host.mu.Lock()
	f.host.failUp = nil
	f.host.mu.Unlock()
	for range 10 {
		a.flush()
	}
	if a.pendingCount() != 0 {
		t.Fatalf("pending after the retries = %d", a.pendingCount())
	}
	f.host.mu.Lock()
	defer f.host.mu.Unlock()
	total := 0
	for i, records := range f.host.uploads {
		grants := f.host.grants[i]
		if len(records) > AuditMaxRecords || len(grants) > AuditMaxGrants {
			t.Fatalf("batch %d: %d records, %d grants", i, len(records), len(grants))
		}
		seen := map[string]bool{}
		for _, token := range grants {
			if seen[token] {
				t.Fatalf("batch %d repeats token %s", i, token)
			}
			seen[token] = true
		}
		if seen["token-g0"] && seen["token-g0-renewed"] {
			t.Fatalf("batch %d carries two tokens of one grant", i)
		}
		total += len(records)
	}
	if total != 251 {
		t.Fatalf("uploaded %d records, want 251", total)
	}
}
