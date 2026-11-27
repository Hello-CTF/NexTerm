package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/p2p/agentside"
	"github.com/Hello-CTF/NexTerm/internal/p2p/clock"
	"github.com/Hello-CTF/NexTerm/internal/p2p/grant"
	"github.com/Hello-CTF/NexTerm/internal/p2p/legtest"
	"github.com/Hello-CTF/NexTerm/internal/p2p/natprobe"
	"github.com/Hello-CTF/NexTerm/internal/p2p/selector"
	"github.com/Hello-CTF/NexTerm/internal/rstream"
)

const testDevice = "dev-1"

var testEpoch = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

// harness runs a manager against an in-memory server and agent: relay bridges are pipes into the agent's
// rstream server, the signal bridge reaches a real agentside service, and legs are legtest pairs.
type harness struct {
	t       *testing.T
	clock   *clock.Fake
	net     *legtest.Net
	signing grant.SigningKey
	device  ed25519.PrivateKey
	streams *rstream.Server
	svc     *agentside.Service
	mgr     *Manager
	runCtx  context.Context
	stop    context.CancelFunc

	mu         sync.Mutex
	mode       selector.Mode
	proxied    bool
	insecure   bool
	enabled    bool
	keyState   string
	configErr  error
	keyErr     error
	scope      []string
	grantErr   error
	relayDials int
	resumes    int
	grants     int
	audits     []agent.P2PAuditRecord
	statuses   []DevicePathStatus
	bridges    map[string]net.Conn
	bridgeN    int
	ttl        time.Duration
	stun       []agent.P2PEndpoint
	probe      ProbeFunc
}

func newHarness(t *testing.T, configure ...func(*harness)) *harness {
	t.Helper()
	signing, err := grant.GenerateSigningKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, device, _ := ed25519.GenerateKey(rand.Reader)
	h := &harness{
		t: t, clock: clock.NewFake(testEpoch), net: legtest.NewNet(), signing: signing, device: device,
		streams: rstream.NewServer(rstream.ServerOptions{}), mode: selector.ModeAuto, enabled: true,
		keyState: agent.P2PKeyStateBound, scope: []string{"terminal", "file", "forward"}, bridges: make(map[string]net.Conn),
		ttl: grant.DefaultTTL,
	}
	for _, apply := range configure {
		apply(h)
	}
	var logOut io.Writer = io.Discard
	if os.Getenv("P2P_TEST_LOG") != "" {
		logOut = os.Stderr
	}
	logger := slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h.runCtx, h.stop = context.WithCancel(context.Background())
	h.svc = agentside.New(h, agentside.Options{Clock: h.clock, DialLeg: h.net.Dial, Probe: func(context.Context, []string) natprobe.Mapping { return natprobe.Independent }, Logger: logger})
	probe := h.probe
	if probe == nil {
		probe = func(context.Context, []string) natprobe.Mapping { return natprobe.Independent }
	}
	h.mgr = New(h, Options{
		Clock: h.clock, DialLeg: h.net.Dial, Probe: probe, MigrationSpacing: time.Millisecond, Logger: logger,
		Emit: func(status DevicePathStatus) {
			h.mu.Lock()
			h.statuses = append(h.statuses, status)
			h.mu.Unlock()
		},
	})
	t.Cleanup(func() {
		h.mgr.Close()
		h.svc.Close()
		h.stop()
		_ = h.streams.Close()
		h.svc.Wait()
	})
	return h
}

func (h *harness) set(apply func(*harness)) {
	h.mu.Lock()
	apply(h)
	h.mu.Unlock()
}

// serveAgentCarrier plays the agent's runtime: the rstream server accepts the carrier and new streams echo.
func (h *harness) serveAgentCarrier(conn net.Conn, purpose string) error {
	stream, resumed, err := h.streams.Accept(h.runCtx, conn, purpose)
	if err != nil {
		return err
	}
	if resumed {
		h.mu.Lock()
		h.resumes++
		h.mu.Unlock()
		return nil
	}
	go func() {
		_, _ = io.Copy(stream, stream)
		_ = stream.(*rstream.Stream).CloseWrite()
		_, _ = io.Copy(io.Discard, stream)
		_ = stream.Close()
	}()
	return nil
}

// Backend.

func (h *harness) DialRelay(_ context.Context, _ string, purpose string, resume bool) (net.Conn, bool, error) {
	h.mu.Lock()
	h.relayDials++
	h.mu.Unlock()
	client, agentEnd := net.Pipe()
	go func() { _ = h.serveAgentCarrier(agentEnd, purpose) }()
	return client, true, nil
}

func (h *harness) DialSignal(_ context.Context, _ string) (net.Conn, error) {
	client, agentEnd := net.Pipe()
	h.mu.Lock()
	h.bridgeN++
	id := "signal-" + strconv.Itoa(h.bridgeN)
	h.bridges[id] = agentEnd
	h.mu.Unlock()
	go func() { _ = h.svc.ServeSignal(h.runCtx, id) }()
	return client, nil
}

func (h *harness) Config(context.Context) (*agent.P2PConfig, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.configErr != nil {
		return nil, h.configErr
	}
	return &agent.P2PConfig{Enabled: h.enabled, STUN: h.stun, GrantKeys: []grant.PublicKey{h.signing.Public()}}, nil
}

func (h *harness) Grant(_ context.Context, deviceID string, clientPub ed25519.PublicKey) (*Grant, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.grantErr != nil {
		return nil, h.grantErr
	}
	h.grants++
	now := h.clock.Now().UnixMilli()
	claims := grant.Claims{
		GrantID: "grant-1", DeviceID: deviceID, OwnerID: "owner", ClientPub: grant.Key(clientPub),
		AgentPub: grant.Key(h.device.Public().(ed25519.PublicKey)), Scope: slices.Clone(h.scope),
		IssuedAt: now, ExpiresAt: now + h.ttl.Milliseconds(),
	}
	token, err := grant.Issue(h.signing, claims)
	if err != nil {
		return nil, err
	}
	return &Grant{Grant: token, GrantID: claims.GrantID, DeviceID: deviceID, AgentPub: claims.AgentPub, Scope: claims.Scope,
		IssuedAt: claims.IssuedAt, ExpiresAt: claims.ExpiresAt, RenewAfterMS: (2 * time.Minute).Milliseconds()}, nil
}

func (h *harness) DeviceKeyState(context.Context, string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.keyState, h.keyErr
}

func (h *harness) Mode(context.Context, string) (selector.Mode, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.mode, nil
}

func (h *harness) Endpoint(context.Context) (Endpoint, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Endpoint{URL: "https://nx.example.com", Proxied: h.proxied, Insecure: h.insecure}, nil
}

// agentside.Host.

func (h *harness) DeviceID() string                    { return testDevice }
func (h *harness) P2PKey() (ed25519.PrivateKey, error) { return h.device, nil }
func (h *harness) TerminalEnabled() bool               { return true }

func (h *harness) FetchP2PConfig(ctx context.Context) (*agent.P2PConfig, agent.P2PEndpointInfo, error) {
	config, err := h.Config(ctx)
	return config, agent.P2PEndpointInfo{URL: "https://nx.example.com"}, err
}

func (h *harness) DialPurposeBridge(_ context.Context, bridgeID, _ string) (net.Conn, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	conn := h.bridges[bridgeID]
	if conn == nil {
		return nil, errors.New("unknown bridge")
	}
	delete(h.bridges, bridgeID)
	return conn, nil
}

func (h *harness) ServeCarrier(_ context.Context, conn net.Conn, _ string, purpose string, _ []string) error {
	return h.serveAgentCarrier(conn, purpose)
}

func (h *harness) UploadP2PAudit(_ context.Context, _ []string, records []agent.P2PAuditRecord) (*agent.P2PAuditResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.audits = append(h.audits, records...)
	return &agent.P2PAuditResponse{Accepted: len(records), Rejected: []agent.P2PAuditRejection{}}, nil
}

// Helpers.

// waitFor polls cond in real time; the manager's work runs on goroutines that the fake clock does not drive.
func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s; status %+v", what, h.mgr.Status(context.Background(), testDevice))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (h *harness) waitPath(path, reason string) {
	h.t.Helper()
	h.waitFor("path "+path+" reason "+reason, func() bool {
		status := h.mgr.Status(context.Background(), testDevice)
		return status.Path == path && status.Reason == reason
	})
}

func (h *harness) waitDirect(direct int) {
	h.t.Helper()
	h.waitFor("direct streams = "+strconv.Itoa(direct), func() bool {
		_, got := h.mgr.Counts(testDevice)
		return got == direct
	})
}

// tickUntil advances the fake clock a second at a time, giving the manager real time to react after each step,
// until cond holds or limit of fake time passed.
func (h *harness) tickUntil(what string, limit time.Duration, cond func() bool) {
	h.t.Helper()
	for elapsed := time.Duration(0); ; elapsed += time.Second {
		deadline := time.Now().Add(20 * time.Millisecond)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		if elapsed > limit {
			h.t.Fatalf("no %s after %s; status %+v", what, limit, h.mgr.Status(context.Background(), testDevice))
		}
		h.clock.Advance(time.Second)
	}
}

// tickUntilDirect advances the fake clock a second at a time until direct streams run on the leg; a return
// to direct needs the selector's healthy window, which only fake time can pass.
func (h *harness) tickUntilDirect(direct int, limit time.Duration) {
	h.t.Helper()
	for elapsed := time.Duration(0); ; elapsed += time.Second {
		if _, got := h.mgr.Counts(testDevice); got == direct {
			return
		}
		if elapsed > limit {
			h.t.Fatalf("no %d direct streams after %s; status %+v", direct, limit, h.mgr.Status(context.Background(), testDevice))
		}
		h.clock.Advance(time.Second)
		time.Sleep(5 * time.Millisecond)
	}
}

// tick advances the fake clock by d in steps of one ping interval, letting the manager react between steps.
func (h *harness) tick(d time.Duration) {
	for d > 0 {
		step := min(d, time.Second)
		h.clock.Advance(step)
		d -= step
		time.Sleep(time.Millisecond)
	}
}

func (h *harness) dial(purpose string) *rstream.Stream {
	h.t.Helper()
	conn, err := h.mgr.Dial(context.Background(), testDevice, purpose)
	if err != nil {
		h.t.Fatal(err)
	}
	return conn.(*rstream.Stream)
}

// transfer echoes payload through stream; between writes it calls step with the bytes echoed so far, which may
// change the path.
type transfer struct {
	stream  *rstream.Stream
	payload []byte
	echoed  []byte
	written atomic.Int64
	readErr chan error
}

func startTransfer(t *testing.T, stream *rstream.Stream, size int) *transfer {
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	tr := &transfer{stream: stream, payload: payload, readErr: make(chan error, 1)}
	go func() {
		buffer := make([]byte, 32<<10)
		for len(tr.echoed) < len(payload) {
			n, err := stream.Read(buffer)
			tr.echoed = append(tr.echoed, buffer[:n]...)
			if err != nil {
				tr.readErr <- err
				return
			}
		}
		tr.readErr <- nil
	}()
	return tr
}

// write sends payload[from:to].
func (tr *transfer) write(t *testing.T, from, to int) {
	t.Helper()
	if _, err := tr.stream.Write(tr.payload[from:to]); err != nil {
		t.Fatalf("write %d..%d: %v", from, to, err)
	}
	tr.written.Store(int64(to))
}

func (tr *transfer) finish(t *testing.T) {
	t.Helper()
	select {
	case err := <-tr.readErr:
		if err != nil {
			t.Fatalf("read after %d bytes: %v", len(tr.echoed), err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("transfer did not finish")
	}
	if len(tr.echoed) != len(tr.payload) || !slices.Equal(tr.echoed, tr.payload) {
		t.Fatalf("echoed %d bytes differ from the %d sent", len(tr.echoed), len(tr.payload))
	}
}

var (
	errForbidden = ipc.NewError(ipc.CodeForbidden, "denied")
	offlineErr   = ipc.NewError(ipc.CodeDisconnected, "offline")
)
