package fleetserver

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/p2p/agentside"
	p2pclient "github.com/Hello-CTF/NexTerm/internal/p2p/client"
	"github.com/Hello-CTF/NexTerm/internal/p2p/grant"
	"github.com/Hello-CTF/NexTerm/internal/p2p/leg"
	"github.com/Hello-CTF/NexTerm/internal/p2p/selector"
	"github.com/Hello-CTF/NexTerm/internal/relaymux"
	"github.com/Hello-CTF/NexTerm/internal/relaymux/relayclient"
	"github.com/Hello-CTF/NexTerm/internal/rstream"
	"github.com/coder/websocket"
)

// e2eStore keeps the agent config in memory.
type e2eStore struct {
	mu     sync.Mutex
	config agent.Config
}

func (s *e2eStore) Load() (*agent.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := s.config
	return &copied, nil
}

func (s *e2eStore) Save(config *agent.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config = *config
	return nil
}

type e2eCollector struct{}

func (e2eCollector) Collect() (agent.Sample, error) { return agent.Sample{}, nil }

// echoHelper stands in for the supervisor helper bridge: it echoes the terminal stream.
func echoHelper(context.Context, string) (io.ReadWriteCloser, error) {
	local, remote := net.Pipe()
	go func() {
		_, _ = io.Copy(remote, remote)
		_ = remote.Close()
	}()
	return local, nil
}

// e2eBackend is the desktop's view of the server: the relay and the P2P routes over the test session cookie.
type e2eBackend struct {
	f        *httpFixture
	session  *httpSession
	deviceID string

	mu   sync.Mutex
	mode selector.Mode

	// relay, when set, is the integrated client's relay dialer over relayBase; without it the backend is a
	// client that predates the relay and dials a bridge per operation. legacyDials counts the bridges.
	relay       *relayclient.Dialer
	relayBase   string
	legacyDials atomic.Int32
}

func (b *e2eBackend) relaySession() relayclient.Session {
	return relayclient.Session{BaseURL: b.relayBase, Transport: http.DefaultTransport, Token: b.session.cookie.Value}
}

func (b *e2eBackend) request(ctx context.Context, method, path string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		encoded, _ := json.Marshal(payload)
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, b.f.http.URL+path, body)
	if err != nil {
		return err
	}
	request.AddCookie(b.session.cookie)
	request.Header.Set(csrfHeaderName, b.session.csrf)
	request.Header.Set("Content-Type", "application/json")
	response, err := b.f.client.Do(request)
	if err != nil {
		return ipc.NewError(ipc.CodeDisconnected, err.Error())
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error *ipc.Error `json:"error"`
		}
		if json.Unmarshal(raw, &failure) == nil && failure.Error != nil {
			return ipc.NewError(failure.Error.Code, failure.Error.Message)
		}
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return json.Unmarshal(raw, out)
}

func (b *e2eBackend) DialRelay(ctx context.Context, deviceID, purpose string, resume bool) (net.Conn, bool, error) {
	legacy := func(ctx context.Context) (net.Conn, bool, error) {
		b.legacyDials.Add(1)
		conn, stream, _, err := dialStreamRelay(ctx, b.f, deviceID, b.session.cookie, purpose, resume)
		return conn, stream, err
	}
	if b.relay == nil {
		return legacy(ctx)
	}
	return b.relay.DialDevice(ctx, b.relaySession(), deviceID, purpose, true, resume, legacy)
}

func (b *e2eBackend) DialSignal(ctx context.Context, deviceID string) (net.Conn, error) {
	if b.relay == nil {
		return b.dialSignalBridge(ctx, deviceID)
	}
	conn, _, err := b.relay.DialDevice(ctx, b.relaySession(), deviceID, agent.PurposeP2PSignal, false, false, func(ctx context.Context) (net.Conn, bool, error) {
		conn, err := b.dialSignalBridge(ctx, deviceID)
		return conn, false, err
	})
	return conn, err
}

func (b *e2eBackend) dialSignalBridge(ctx context.Context, deviceID string) (net.Conn, error) {
	b.legacyDials.Add(1)
	url := "ws" + strings.TrimPrefix(b.f.http.URL, "http") + "/fleet/devices/" + deviceID + "/bridge?purpose=" + agent.PurposeP2PSignal
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": []string{b.session.cookie.Name + "=" + b.session.cookie.Value}}})
	if err != nil {
		return nil, err
	}
	return websocket.NetConn(context.Background(), conn, websocket.MessageBinary), nil
}

func (b *e2eBackend) Config(ctx context.Context) (*agent.P2PConfig, error) {
	var config agent.P2PConfig
	return &config, b.request(ctx, http.MethodGet, "/fleet/p2p/config", nil, &config)
}

func (b *e2eBackend) Grant(ctx context.Context, deviceID string, clientPub ed25519.PublicKey) (*p2pclient.Grant, error) {
	var view p2pclient.Grant
	if err := b.request(ctx, http.MethodPost, "/fleet/devices/"+deviceID+"/p2p/grant", map[string]string{"client_pub": grant.Key(clientPub).String()}, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

func (b *e2eBackend) DeviceKeyState(ctx context.Context, deviceID string) (string, error) {
	var view struct {
		Devices []struct {
			ID    string `json:"id"`
			Agent *struct {
				P2PKey *struct {
					State string `json:"state"`
				} `json:"p2p_key"`
			} `json:"agent"`
		} `json:"devices"`
	}
	if err := b.request(ctx, http.MethodGet, "/fleet/devices", nil, &view); err != nil {
		return "", err
	}
	for _, device := range view.Devices {
		if device.ID == deviceID && device.Agent != nil && device.Agent.P2PKey != nil {
			return device.Agent.P2PKey.State, nil
		}
	}
	return "", nil
}

func (b *e2eBackend) Mode(context.Context, string) (selector.Mode, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.mode, nil
}

func (b *e2eBackend) Endpoint(context.Context) (p2pclient.Endpoint, error) {
	return p2pclient.Endpoint{URL: b.f.http.URL}, nil
}

// legRecorder keeps the legs the client dialed so a test can kill one.
type legRecorder struct {
	mu   sync.Mutex
	legs []leg.Leg
}

func (r *legRecorder) dial(ctx context.Context, cfg leg.Config, local, remote leg.Credentials, sig leg.Signaler) (leg.Leg, error) {
	l, err := leg.Dial(ctx, cfg, local, remote, sig)
	if err == nil {
		r.mu.Lock()
		r.legs = append(r.legs, l)
		r.mu.Unlock()
	}
	return l, err
}

func (r *legRecorder) last() leg.Leg {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.legs[len(r.legs)-1]
}

func loopbackLeg(cfg *leg.Config) { cfg.LoopbackOnly = true }

type e2eRig struct {
	f       *httpFixture
	harness *relayHarness
	backend *e2eBackend
	manager *p2pclient.Manager
	legs    *legRecorder
	svc     *agentside.Service
	device  string
	owner   *account.User
	// revokes receives the reason of every p2p_revoke frame the agent reads, before the frame is applied.
	revokes chan string
}

// newE2ERig starts the real fleet server, a real agent runtime with the agentside service, and a client path
// manager; legs run over real UDP on loopback.
func newE2ERig(t *testing.T) *e2eRig {
	t.Helper()
	return newE2ERigWith(t, e2eOptions{})
}

// e2eOptions vary the rig across the relay compatibility matrix.
type e2eOptions struct {
	// relay runs the fleet server with the QUIC relay on loopback and gives the agent and client the carriers
	// named below; without it the server still offers the websocket relay to an agent that dials it.
	relay bool
	// agentCarrier and clientCarrier force the relay carrier of each side (relaymux.KindQUIC or KindWS); an
	// empty clientCarrier is a client that predates the relay.
	agentCarrier, clientCarrier string
	// oldAgent runs the agent as one that predates the relay.
	oldAgent bool
	// relayConfig replaces GET /relay/config: "404" stands for a server without the relay, "disabled" for one
	// that turned it off.
	relayConfig string
	// dropP2PRevoke makes the agent record p2p_revoke frames without applying them, so a test sees what the
	// agent does on its own.
	dropP2PRevoke bool
}

// forceCarrier makes dial configurations of h use exactly carrier.
func forceCarrier(h *relayHarness, carrier string) func(*relaymux.DialConfig) {
	return func(config *relaymux.DialConfig) {
		config.Backoff = relaymux.NewQUICBackoff(time.Minute, nil)
		if carrier == relaymux.KindQUIC {
			config.QUICAddrs = []string{h.mux.Addr().String()}
			config.Pin = h.pin
			config.WSURL = ""
			return
		}
		config.QUICAddrs = nil
	}
}

func newE2ERigWith(t testing.TB, options e2eOptions) *e2eRig {
	t.Helper()
	var (
		f       *httpFixture
		harness *relayHarness
	)
	if options.relay {
		harness = newRelayHarness(t, WithP2P(P2PSettings{Enabled: true}))
		f = harness.httpFixture
	} else {
		f = newHTTPFixture(t, false, WithP2P(P2PSettings{Enabled: true}))
	}
	owner := f.createUser(t, "p2p-e2e")
	deviceID, secret := enrollAgent(t, f, owner, "p2p-e2e-box")
	var logOut io.Writer = io.Discard
	if os.Getenv("P2P_TEST_LOG") != "" {
		logOut = os.Stderr
	}
	logger := slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: slog.LevelDebug}))
	// The agent probes /healthz, which the full server serves outside the fleet handler.
	handler := f.service.Handler()
	agentFront := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == agent.PathHealth {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"service":"nexterm-server"}`))
			return
		}
		if r.URL.Path == relaymux.ConfigPath {
			switch options.relayConfig {
			case "404":
				http.NotFound(w, r)
				return
			case "disabled":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"enabled":false,"version":1}`))
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(agentFront.Close)
	store := &e2eStore{config: agent.Config{
		Version: agent.ConfigVersion, DeviceID: deviceID, Secret: secret, Name: "p2p-e2e-box", Platform: "linux",
		BaseURLs: []agent.BaseURLEntry{{URL: agentFront.URL}}, MetricsIntervalMS: 60000, TerminalEnabled: true,
	}}
	prober, err := agent.NewProber(store.config.BaseURLs)
	if err != nil {
		t.Fatal(err)
	}
	agentOptions := agent.Options{
		Store: store, DataDir: t.TempDir(), Prober: prober, Manager: agent.EmbeddedServiceManager(),
		Collector: e2eCollector{}, EnsureHelper: func(context.Context) error { return nil }, Bridge: echoHelper,
		NoSelfUpgrade: true, Logger: logger.With("side", "agent"), DisableRelayMux: options.oldAgent,
	}
	if harness != nil && options.agentCarrier != "" {
		agentOptions.RelayDialHook = forceCarrier(harness, options.agentCarrier)
	}
	runtime, err := agent.New(agentOptions)
	if err != nil {
		t.Fatal(err)
	}
	svc := agentside.Install(runtime, agentside.Options{LegConfig: loopbackLeg, AuditInterval: 100 * time.Millisecond, Logger: logger.With("side", "agentside")})
	revokes := make(chan string, 16)
	runtime.SetP2PRevokeHandler(func(grantID, reason string) {
		select {
		case revokes <- reason:
		default:
		}
		if !options.dropP2PRevoke {
			svc.Revoke(grantID, reason)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	backend := &e2eBackend{f: f, session: f.session(t, owner), deviceID: deviceID, mode: selector.ModeAuto}
	if options.clientCarrier != "" {
		backend.relayBase = agentFront.URL
		backend.relay = relayclient.New(relayclient.Options{Configure: forceCarrier(harness, options.clientCarrier)})
		t.Cleanup(backend.relay.Close)
	}
	legs := &legRecorder{}
	manager := p2pclient.New(backend, p2pclient.Options{DialLeg: legs.dial, LegConfig: loopbackLeg, MigrationSpacing: 10 * time.Millisecond, Logger: logger.With("side", "client")})
	t.Cleanup(func() {
		manager.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("agent runtime did not stop")
		}
		svc.Wait()
	})
	rig := &e2eRig{f: f, harness: harness, backend: backend, manager: manager, legs: legs, svc: svc, device: deviceID, owner: owner, revokes: revokes}
	rig.waitFor(t, "device key bound and agent online", func() bool {
		state, err := backend.DeviceKeyState(context.Background(), deviceID)
		return err == nil && state == agent.P2PKeyStateBound && f.service.registry.AgentOnline(deviceID)
	})
	return rig
}

func (r *e2eRig) waitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; status %+v", what, r.manager.Status(context.Background(), r.device))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r *e2eRig) waitDirect(t *testing.T, direct int) {
	t.Helper()
	r.waitFor(t, fmt.Sprintf("%d direct streams", direct), func() bool {
		_, got := r.manager.Counts(r.device)
		return got == direct
	})
}

// e2ePayload is terminal-like data whose first frame header names an oversized frame, so the helper bridge's
// frame filter passes it through untouched.
func e2ePayload(t *testing.T, size int) []byte {
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	copy(payload, []byte{0xff, 0xff, 0xff, 0xff})
	return payload
}

type e2eTransfer struct {
	stream  net.Conn
	payload []byte
	echoed  []byte
	done    chan error
}

func startE2ETransfer(t *testing.T, stream net.Conn, size int) *e2eTransfer {
	tr := &e2eTransfer{stream: stream, payload: e2ePayload(t, size), done: make(chan error, 1)}
	go func() {
		buffer := make([]byte, 32<<10)
		for len(tr.echoed) < len(tr.payload) {
			n, err := stream.Read(buffer)
			tr.echoed = append(tr.echoed, buffer[:n]...)
			if err != nil {
				tr.done <- err
				return
			}
		}
		tr.done <- nil
	}()
	return tr
}

func (tr *e2eTransfer) write(t *testing.T, from, to int) {
	t.Helper()
	if _, err := tr.stream.Write(tr.payload[from:to]); err != nil {
		t.Fatalf("write %d..%d: %v", from, to, err)
	}
}

func (tr *e2eTransfer) finish(t *testing.T) {
	t.Helper()
	select {
	case err := <-tr.done:
		if err != nil {
			t.Fatalf("read after %d of %d bytes: %v", len(tr.echoed), len(tr.payload), err)
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("transfer stalled at %d of %d bytes", len(tr.echoed), len(tr.payload))
	}
	if !bytes.Equal(tr.echoed, tr.payload) {
		t.Fatal("echoed bytes differ from the bytes sent")
	}
}

// A terminal stream starts on the real relay, moves to a real loopback leg mid-transfer, falls back to the relay
// when the leg dies and again after a server revoke, without losing or repeating a byte; the agent uploads the
// direct streams to the audit log.
func TestP2PRuntimeEndToEnd(t *testing.T) {
	rig := newE2ERig(t)
	conn, err := rig.manager.Dial(context.Background(), rig.device, agent.PurposeTerminal)
	if err != nil {
		t.Fatal(err)
	}
	stream := conn.(*rstream.Stream)
	const size = 2 << 20
	tr := startE2ETransfer(t, stream, size)
	tr.write(t, 0, size/4)
	rig.waitDirect(t, 1)
	if status := rig.manager.Status(context.Background(), rig.device); status.Path != p2pclient.PathDirect {
		t.Fatalf("status = %+v", status)
	}
	tr.write(t, size/4, size/2)

	// Kill the leg mid-transfer: the stream resumes on the relay.
	_ = rig.legs.last().Close()
	tr.write(t, size/2, 3*size/4)
	rig.waitDirect(t, 0)
	tr.write(t, 3*size/4, size)
	tr.finish(t)
	if status := rig.manager.Status(context.Background(), rig.device); status.Path != p2pclient.PathRelay {
		t.Fatalf("status after the leg died = %+v", status)
	}

	// A network change retries at once; the return needs the selector's healthy window, so instead of waiting
	// for it the device goes direct through its stored direct mode.
	rig.backend.mu.Lock()
	rig.backend.mode = selector.ModeDirect
	rig.backend.mu.Unlock()
	rig.manager.NetworkChanged()
	rig.waitDirect(t, 1)

	// A server-side revoke reaches the agent as p2p_revoke and the stream returns to the relay.
	second := startE2ETransfer(t, stream, 256<<10)
	second.write(t, 0, 128<<10)
	rig.f.service.revokeP2PGrants(func(*activeGrant) bool { return true }, p2pRevokeSession, true)
	rig.waitDirect(t, 0)
	second.write(t, 128<<10, 256<<10)
	second.finish(t)
	rig.waitFor(t, "agent session closed", func() bool { return rig.svc.Sessions() == 0 })

	_ = stream.Close()
	rig.waitFor(t, "direct stream audit rows", func() bool {
		for _, row := range auditPayloadRows(t, rig.f.service.db, auditSourceFleet, auditKindDeviceTerminal) {
			if row["path"] == pathDirect && row["action"] == "terminal_open" && row["outcome"] == "allow" {
				return true
			}
		}
		return false
	})
}

// Disabling the owner of a device with a live direct terminal revokes the session on the control channel and
// the agent closes it at once, instead of keeping it until the grant expires.
func TestP2PRuntimeOwnerDisabledClosesDirectSession(t *testing.T) {
	rig := newE2ERig(t)
	conn, err := rig.manager.Dial(context.Background(), rig.device, agent.PurposeTerminal)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	rig.waitDirect(t, 1)
	if err := rig.f.accounts.SetUserDisabled(context.Background(), rig.owner.ID, true); err != nil {
		t.Fatal(err)
	}
	select {
	case reason := <-rig.revokes:
		if reason != p2pRevokeOwner {
			t.Fatalf("p2p_revoke reason = %q", reason)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the agent received no p2p_revoke for the disabled owner")
	}
	rig.waitFor(t, "agent session closed", func() bool { return rig.svc.Sessions() == 0 })
}

// Revoking the device kicks its control channel without p2p_revoke frames; the agent still closes its direct
// sessions before it stops.
func TestP2PRuntimeDeviceRevokeClosesDirectSession(t *testing.T) {
	rig := newE2ERig(t)
	conn, err := rig.manager.Dial(context.Background(), rig.device, agent.PurposeTerminal)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	rig.waitDirect(t, 1)
	identity := &account.Identity{UserID: rig.owner.ID, Role: rig.owner.Role, State: rig.owner.State}
	if err := rig.f.service.RevokeDevice(context.Background(), identity, rig.device); err != nil {
		t.Fatal(err)
	}
	rig.waitFor(t, "agent session closed", func() bool { return rig.svc.Sessions() == 0 })
}

// The agent closes its direct sessions itself when the owner becomes unavailable, even when the p2p_revoke
// frames do not take effect (here: dropped), so the kick into owner wait alone ends them.
func TestP2PRuntimeOwnerWaitClosesDirectSessionWithoutRevokeFrames(t *testing.T) {
	rig := newE2ERigWith(t, e2eOptions{dropP2PRevoke: true})
	conn, err := rig.manager.Dial(context.Background(), rig.device, agent.PurposeTerminal)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	rig.waitDirect(t, 1)
	if err := rig.f.accounts.SetUserDisabled(context.Background(), rig.owner.ID, true); err != nil {
		t.Fatal(err)
	}
	rig.waitFor(t, "agent session closed", func() bool { return rig.svc.Sessions() == 0 })
}
