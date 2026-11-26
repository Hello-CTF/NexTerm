package fleetserver

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/relaymux"
	"github.com/Hello-CTF/NexTerm/internal/rstream"
	"github.com/coder/websocket"
)

// relayHarness is an HTTP fixture with the QUIC relay on a loopback UDP socket.
type relayHarness struct {
	*httpFixture
	mux *relaymux.UDPMux
	pin string
}

func newRelayHarness(t testing.TB, options ...Option) *relayHarness {
	t.Helper()
	f := newHTTPFixture(t, false, append([]Option{WithRelay(RelaySettings{QUIC: true})}, options...)...)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := relaymux.PinnedCertificate(key)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	mux, err := relaymux.ListenUDPMux(conn, relaymux.ServerTLSConfig(cert))
	if err != nil {
		t.Fatal(err)
	}
	f.service.ServeRelayQUIC(mux)
	f.service.SetRelayQUIC([]int{mux.Addr().(*net.UDPAddr).Port}, cert.Pin)
	t.Cleanup(func() { _ = f.service.Close() })
	return &relayHarness{httpFixture: f, mux: mux, pin: cert.Pin}
}

var relayCarriers = []string{relaymux.KindQUIC, relaymux.KindWS}

// dialConfig reaches the harness over exactly one carrier.
func (h *relayHarness) dialConfig(carrier string, hello relaymux.Hello) relaymux.DialConfig {
	config := relaymux.DialConfig{Hello: hello, Backoff: relaymux.NewQUICBackoff(time.Minute, nil)}
	if carrier == relaymux.KindQUIC {
		config.QUICAddrs = []string{h.mux.Addr().String()}
		config.Pin = h.pin
	} else {
		config.WSURL = "ws" + strings.TrimPrefix(h.http.URL, "http") + relaymux.WSPath
	}
	return config
}

func (h *relayHarness) dial(t testing.TB, carrier string, hello relaymux.Hello) (*relaymux.Link, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	link, err := relaymux.Dial(ctx, h.dialConfig(carrier, hello))
	if err == nil {
		t.Cleanup(func() { _ = link.Close() })
		if link.Kind() != carrier {
			t.Fatalf("link kind = %s, want %s", link.Kind(), carrier)
		}
	}
	return link, err
}

func (h *relayHarness) clientLink(t testing.TB, carrier string, session *httpSession) *relaymux.Link {
	t.Helper()
	link, err := h.dial(t, carrier, relaymux.Hello{Role: relaymux.RoleClient, Token: session.cookie.Value})
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	return link
}

// fakeRelayAgent plays a relay-mux agent: a control channel advertising capabilities plus a relay link that
// serves streams with handler.
type fakeRelayAgent struct {
	link    *relaymux.Link
	headers chan relaymux.StreamHeader
}

func echoStream(_ context.Context, incoming *relaymux.IncomingStream) {
	params := map[string]string{}
	if incoming.Header.Params[relaymux.ParamStream] == "1" {
		params[relaymux.ParamStream] = "1"
	}
	conn, err := incoming.Accept(params)
	if err != nil {
		return
	}
	_, _ = io.Copy(conn, conn)
	_ = relaymux.CloseWrite(conn)
	_, _ = io.Copy(io.Discard, conn)
	_ = conn.Close()
}

var relayAgentCapabilities = []string{agent.PurposeTerminal, deviceproto.PurposeForward, deviceproto.PurposeFile, rstream.Capability, relaymux.Capability}

func (h *relayHarness) startAgent(t testing.TB, carrier, deviceID, secret string, handler func(context.Context, *relaymux.IncomingStream)) *fakeRelayAgent {
	t.Helper()
	control := dialDeviceWS(t, h.httpFixture)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret, SessionEnv: true,
		Capabilities: relayAgentCapabilities,
	}); response.Type != "hello_ok" {
		t.Fatalf("control hello = %+v", response)
	}
	link, err := h.dial(t, carrier, relaymux.Hello{Role: relaymux.RoleAgent, DeviceID: deviceID, Secret: secret, Protocol: agent.ProtocolVersion})
	if err != nil {
		t.Fatalf("agent dial: %v", err)
	}
	fake := &fakeRelayAgent{link: link, headers: make(chan relaymux.StreamHeader, 1024)}
	go func() {
		_ = link.ServeStreams(context.Background(), func(ctx context.Context, incoming *relaymux.IncomingStream) {
			select {
			case fake.headers <- incoming.Header:
			default: // a benchmark opens more streams than anyone inspects
			}
			handler(ctx, incoming)
		})
	}()
	// The server registers the link right after the handshake; wait for it to route there.
	for deadline := time.Now().Add(5 * time.Second); h.service.relay.agent(deviceID) == nil; {
		if time.Now().After(deadline) {
			t.Fatal("agent link not registered")
		}
		time.Sleep(time.Millisecond)
	}
	return fake
}

// requireStreamEnds fails unless conn's read side ends (EOF or a reset) well before a 10 s deadline.
func requireStreamEnds(t testing.TB, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, err := io.Copy(io.Discard, conn)
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatal("the stream stayed open")
	}
}

func roundTrip(t testing.TB, conn net.Conn, payload []byte) {
	t.Helper()
	go func() {
		_, _ = conn.Write(payload)
		_ = relaymux.CloseWrite(conn)
	}()
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Errorf("read: %v", err)
		return
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("echo = %d bytes, want %d identical", len(got), len(payload))
	}
}

// A client stream to an owned device is authorized, audited like a relay bridge and piped to the agent's
// relay link with the header the agent needs.
func TestRelayMuxPipesAuthorizedStream(t *testing.T) {
	for _, carrier := range relayCarriers {
		t.Run(carrier, func(t *testing.T) {
			h := newRelayHarness(t)
			owner := h.createUser(t, "relay-mux-"+carrier)
			deviceID, secret := enrollWSAgent(t, h.httpFixture, owner, "relay-mux-box")
			device := h.startAgent(t, carrier, deviceID, secret, echoStream)
			client := h.clientLink(t, carrier, h.session(t, owner))

			conn, reply, err := client.OpenDeviceStream(context.Background(), relaymux.StreamHeader{
				Purpose: deviceproto.PurposeForward, DeviceID: deviceID, RequestID: "req-1",
				Params: map[string]string{relaymux.ParamStream: "1"},
			})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if reply.Params[relaymux.ParamStream] != "1" {
				t.Fatalf("reply = %+v, want the stream flag", reply)
			}
			payload := make([]byte, 1<<20)
			_, _ = rand.Read(payload)
			roundTrip(t, conn, payload)
			_ = conn.Close()

			header := <-device.headers
			if header.Purpose != deviceproto.PurposeForward || header.DeviceID != deviceID || header.RequestID != "req-1" || header.Params[relaymux.ParamStream] != "1" {
				t.Fatalf("agent header = %+v", header)
			}
			audits := auditPayloadRows(t, h.service.db, auditSourceFleet, auditKindDeviceTerminal)
			if len(audits) != 1 || audits[0]["action"] != "forward_open" || audits[0]["outcome"] != "allow" ||
				audits[0]["requester"] != owner.ID || audits[0]["stream"] != true {
				t.Fatalf("audits = %v", audits)
			}
		})
	}
}

// A device whose agent keeps no relay link is reached through a per-operation bridge, as the bridge relay does.
func TestRelayMuxFallsBackToBridgeForLegacyAgent(t *testing.T) {
	h := newRelayHarness(t)
	owner := h.createUser(t, "relay-mux-legacy")
	deviceID, secret := enrollWSAgent(t, h.httpFixture, owner, "relay-mux-legacy-box")
	control := dialDeviceWS(t, h.httpFixture)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret, SessionEnv: true,
		Capabilities: []string{agent.PurposeTerminal, deviceproto.PurposeForward, rstream.Capability},
	}); response.Type != "hello_ok" {
		t.Fatalf("hello = %+v", response)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	legacy := &streamAgent{t: t, f: h.httpFixture, deviceID: deviceID, secret: secret, server: rstream.NewServer(rstream.ServerOptions{})}
	defer func() { _ = legacy.server.Close() }()
	go legacy.run(ctx, control)

	client := h.clientLink(t, relaymux.KindWS, h.session(t, owner))
	conn, reply, err := client.OpenDeviceStream(ctx, relaymux.StreamHeader{Purpose: deviceproto.PurposeForward, DeviceID: deviceID})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Params[relaymux.ParamStream] == "1" {
		t.Fatal("a raw request came back as a stream")
	}
	if _, err := conn.Write([]byte("legacy")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 6)
	if _, err := io.ReadFull(conn, buffer); err != nil || string(buffer) != "legacy" {
		t.Fatalf("echo = %q, %v", buffer, err)
	}
	_ = conn.Close()
	if bridges, _, raw := legacy.counts(); bridges != 1 || raw != 1 {
		t.Fatalf("legacy agent saw %d bridges (%d raw), want one raw bridge", bridges, raw)
	}
}

type refusalCase struct {
	name    string
	setup   func(t *testing.T, h *relayHarness, owner *account.User) (deviceID string)
	purpose string
	resume  bool
	// afterHello runs once the relay session is up, for refusals that arise during a session.
	afterHello func(t *testing.T, h *relayHarness)
}

// Every refusal of a relay stream carries the status, code and message the bridge relay answers for the same
// request, and both write the same deny audit.
func TestRelayStreamRefusalsMatchTheBridgeRelay(t *testing.T) {
	online := func(capabilities []string) func(*testing.T, *relayHarness, *account.User) string {
		return func(t *testing.T, h *relayHarness, owner *account.User) string {
			deviceID, secret := enrollWSAgent(t, h.httpFixture, owner, "refusal-box")
			control := dialDeviceWS(t, h.httpFixture)
			if response := helloDevice(t, control, agent.HelloMessage{
				Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret, SessionEnv: true,
				Capabilities: capabilities,
			}); response.Type != "hello_ok" {
				t.Fatalf("hello = %+v", response)
			}
			return deviceID
		}
	}
	cases := []refusalCase{
		{name: "unknown purpose", setup: online(relayAgentCapabilities), purpose: "bogus"},
		{name: "missing device", setup: func(*testing.T, *relayHarness, *account.User) string { return "no-such-device" }, purpose: agent.PurposeTerminal},
		{name: "not owner", setup: func(t *testing.T, h *relayHarness, _ *account.User) string {
			other := h.createUser(t, fmt.Sprintf("other-%d", time.Now().UnixNano()))
			deviceID, _ := enrollWSAgent(t, h.httpFixture, other, "other-box")
			return deviceID
		}, purpose: agent.PurposeTerminal},
		{name: "revoked device", setup: func(t *testing.T, h *relayHarness, owner *account.User) string {
			deviceID, _ := enrollWSAgent(t, h.httpFixture, owner, "revoked-box")
			if err := h.service.RevokeDevice(context.Background(), &account.Identity{UserID: owner.ID}, deviceID); err != nil {
				t.Fatal(err)
			}
			return deviceID
		}, purpose: agent.PurposeTerminal},
		{name: "remote access off", setup: func(t *testing.T, h *relayHarness, owner *account.User) string {
			deviceID := online(relayAgentCapabilities)(t, h, owner)
			if err := h.service.SetTerminalEnabled(context.Background(), &account.Identity{UserID: owner.ID}, deviceID, false); err != nil {
				t.Fatal(err)
			}
			return deviceID
		}, purpose: agent.PurposeTerminal},
		{name: "offline", setup: func(t *testing.T, h *relayHarness, owner *account.User) string {
			deviceID, _ := enrollWSAgent(t, h.httpFixture, owner, "offline-box")
			return deviceID
		}, purpose: agent.PurposeTerminal},
		{name: "purpose unsupported", setup: online([]string{agent.PurposeTerminal, relaymux.Capability}), purpose: deviceproto.PurposeForward},
		{name: "resume without rstream", setup: online([]string{agent.PurposeTerminal, relaymux.Capability}), purpose: agent.PurposeTerminal, resume: true},
		{name: "TOTP enrollment enforced", setup: online(relayAgentCapabilities), purpose: agent.PurposeTerminal,
			afterHello: func(t *testing.T, h *relayHarness) {
				if err := h.accounts.SetMFARequired(context.Background(), true); err != nil {
					t.Fatal(err)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newRelayHarness(t)
			owner := h.createUser(t, "refusal-owner")
			session := h.session(t, owner)
			deviceID := c.setup(t, h, owner)

			path := "/fleet/devices/" + deviceID + "/bridge?purpose=" + c.purpose + "&stream=1"
			params := map[string]string{relaymux.ParamStream: "1"}
			if c.resume {
				path += "&resume=1"
				params[relaymux.ParamResume] = "1"
			}
			client := h.clientLink(t, relaymux.KindWS, session)
			if c.afterHello != nil {
				c.afterHello(t, h)
			}
			bridge := h.call(t, "GET", path, nil, session, "")
			failure, _ := bridge.body["error"].(map[string]any)
			if bridge.status < 400 || failure == nil {
				t.Fatalf("bridge relay answered HTTP %d %v, want a refusal", bridge.status, bridge.body)
			}
			before := len(auditPayloadRows(t, h.service.db, auditSourceFleet, auditKindDeviceTerminal))

			_, _, err := client.OpenDeviceStream(context.Background(), relaymux.StreamHeader{Purpose: c.purpose, DeviceID: deviceID, Params: params})
			reply, refused := relaymux.Refused(err)
			if !refused {
				t.Fatalf("relay stream = %v, want a refusal", err)
			}
			if reply.Status != bridge.status || reply.Code != failure["code"] || reply.Message != failure["message"] {
				t.Fatalf("relay refusal = %d %s %q, bridge relay = %d %v %q", reply.Status, reply.Code, reply.Message, bridge.status, failure["code"], failure["message"])
			}
			audits := auditPayloadRows(t, h.service.db, auditSourceFleet, auditKindDeviceTerminal)
			if len(audits) != 2*before {
				t.Fatalf("audits after the relay stream = %d, want twice the bridge relay's %d", len(audits), before)
			}
			for i := range before {
				if fmt.Sprint(audits[i]) != fmt.Sprint(audits[before+i]) {
					t.Fatalf("relay audit %v differs from bridge audit %v", audits[before+i], audits[i])
				}
			}
		})
	}
}

// A session is refused without a valid login, and an agent with bad credentials, with the existing vocabulary.
func TestRelayMuxRefusesUnauthenticatedSessions(t *testing.T) {
	for _, carrier := range relayCarriers {
		t.Run(carrier, func(t *testing.T) {
			h := newRelayHarness(t)
			_, err := h.dial(t, carrier, relaymux.Hello{Role: relaymux.RoleClient, Token: "not-a-session"})
			if reply, ok := relaymux.Refused(err); !ok || reply.Status != http.StatusUnauthorized || reply.Message != bridgeSessionReloginMessage {
				t.Fatalf("bad token = %v", err)
			}
			owner := h.createUser(t, "relay-agent-auth")
			deviceID, _ := enrollWSAgent(t, h.httpFixture, owner, "relay-agent-auth-box")
			_, err = h.dial(t, carrier, relaymux.Hello{Role: relaymux.RoleAgent, DeviceID: deviceID, Secret: "wrong", Protocol: agent.ProtocolVersion})
			if reply, ok := relaymux.Refused(err); !ok || reply.Status != http.StatusForbidden || reply.Code != "forbidden" || reply.Message != "设备凭据无效" {
				t.Fatalf("bad secret = %v", err)
			}
			_, err = h.dial(t, carrier, relaymux.Hello{Role: relaymux.RoleAgent, DeviceID: deviceID, Secret: "wrong", Protocol: agent.ProtocolVersion + 1})
			if reply, ok := relaymux.Refused(err); !ok || reply.Code != "version_mismatch" || reply.ServerVersion == "" {
				t.Fatalf("protocol mismatch = %v", err)
			}
		})
	}
}

// Logging out ends the client's relay session with the relogin goaway and resets its open streams.
func TestRelayMuxLogoutClosesStreams(t *testing.T) {
	for _, carrier := range relayCarriers {
		t.Run(carrier, func(t *testing.T) {
			h := newRelayHarness(t)
			owner := h.createUser(t, "relay-logout")
			deviceID, secret := enrollWSAgent(t, h.httpFixture, owner, "relay-logout-box")
			h.startAgent(t, carrier, deviceID, secret, echoStream)
			session := h.session(t, owner)
			client := h.clientLink(t, carrier, session)
			conn, _, err := client.OpenDeviceStream(context.Background(), relaymux.StreamHeader{Purpose: agent.PurposeTerminal, DeviceID: deviceID})
			if err != nil {
				t.Fatal(err)
			}
			if err := h.service.CloseAccountSession(session.id); err != nil {
				t.Fatal(err)
			}
			requireStreamEnds(t, conn)
			select {
			case <-client.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("logout did not end the relay session")
			}
			if reply, ok := relaymux.Refused(client.Err()); !ok || reply.Status != http.StatusUnauthorized || reply.Message != bridgeSessionReloginMessage {
				t.Fatalf("session end reason = %v", client.Err())
			}
		})
	}
}

// Revoking the device ends the agent's relay link with a goaway and the client's streams to it.
func TestRelayMuxDeviceRevokeClosesStreams(t *testing.T) {
	for _, carrier := range relayCarriers {
		t.Run(carrier, func(t *testing.T) {
			h := newRelayHarness(t)
			owner := h.createUser(t, "relay-revoke")
			deviceID, secret := enrollWSAgent(t, h.httpFixture, owner, "relay-revoke-box")
			device := h.startAgent(t, carrier, deviceID, secret, echoStream)
			client := h.clientLink(t, carrier, h.session(t, owner))
			conn, _, err := client.OpenDeviceStream(context.Background(), relaymux.StreamHeader{Purpose: agent.PurposeTerminal, DeviceID: deviceID})
			if err != nil {
				t.Fatal(err)
			}
			if err := h.service.RevokeDevice(context.Background(), &account.Identity{UserID: owner.ID}, deviceID); err != nil {
				t.Fatal(err)
			}
			select {
			case <-device.link.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("revoke did not end the agent link")
			}
			if reply, ok := relaymux.Refused(device.link.Err()); !ok || reply.Status != http.StatusForbidden || reply.Code != "forbidden" {
				t.Fatalf("agent link end reason = %v", device.link.Err())
			}
			requireStreamEnds(t, conn)
			// The client session itself stays: other devices remain reachable.
			select {
			case <-client.Done():
				t.Fatal("a device revoke ended the owner's relay session")
			default:
			}
		})
	}
}

// Many concurrent streams share one client session and one agent session.
func TestRelayMuxManyConcurrentStreams(t *testing.T) {
	for _, carrier := range relayCarriers {
		t.Run(carrier, func(t *testing.T) {
			h := newRelayHarness(t)
			owner := h.createUser(t, "relay-many")
			deviceID, secret := enrollWSAgent(t, h.httpFixture, owner, "relay-many-box")
			h.startAgent(t, carrier, deviceID, secret, echoStream)
			client := h.clientLink(t, carrier, h.session(t, owner))
			const streams = 64
			var wg sync.WaitGroup
			for i := range streams {
				wg.Add(1)
				go func() {
					defer wg.Done()
					conn, _, err := client.OpenDeviceStream(context.Background(), relaymux.StreamHeader{Purpose: agent.PurposeTerminal, DeviceID: deviceID})
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					roundTrip(t, conn, bytes.Repeat([]byte(fmt.Sprintf("%02d", i)), 32<<10))
				}()
			}
			wg.Wait()
			if n := len(auditPayloadRows(t, h.service.db, auditSourceFleet, auditKindDeviceTerminal)); n != streams {
				t.Fatalf("audits = %d, want one per stream", n)
			}
		})
	}
}

// A device that stops reading stalls the client's writer at a bounded lead; once it reads, every byte arrives.
func TestRelayMuxBackpressure(t *testing.T) {
	for _, carrier := range relayCarriers {
		t.Run(carrier, func(t *testing.T) {
			h := newRelayHarness(t)
			owner := h.createUser(t, "relay-backpressure")
			deviceID, secret := enrollWSAgent(t, h.httpFixture, owner, "relay-backpressure-box")
			release := make(chan struct{})
			received := make(chan int64, 1)
			h.startAgent(t, carrier, deviceID, secret, func(_ context.Context, incoming *relaymux.IncomingStream) {
				conn, err := incoming.Accept(nil)
				if err != nil {
					return
				}
				<-release
				n, _ := io.Copy(io.Discard, conn)
				received <- n
				_ = conn.Close()
			})
			client := h.clientLink(t, carrier, h.session(t, owner))
			conn, _, err := client.OpenDeviceStream(context.Background(), relaymux.StreamHeader{Purpose: agent.PurposeTerminal, DeviceID: deviceID})
			if err != nil {
				t.Fatal(err)
			}
			const total = 96 << 20
			// Client send window, the server's two stream buffers and copy buffer, the agent's receive window,
			// plus carrier and socket buffering.
			const bound = 4*relaymux.StreamWindow + (16 << 20)
			var written atomic.Int64
			writeDone := make(chan struct{})
			go func() {
				defer close(writeDone)
				chunk := make([]byte, 32<<10)
				for written.Load() < total {
					n, err := conn.Write(chunk)
					written.Add(int64(n))
					if err != nil {
						return
					}
				}
				_ = relaymux.CloseWrite(conn)
			}()
			last, stable := int64(-1), 0
			for deadline := time.Now().Add(30 * time.Second); stable < 5; {
				if time.Now().After(deadline) {
					t.Fatalf("writer never stalled (%d bytes)", written.Load())
				}
				time.Sleep(50 * time.Millisecond)
				if current := written.Load(); current == last {
					stable++
				} else {
					last, stable = current, 0
				}
			}
			select {
			case <-writeDone:
				t.Fatal("writer finished while the device was not reading")
			default:
			}
			if last > bound {
				t.Fatalf("writer %d bytes ahead of a stopped device, bound %d", last, bound)
			}
			close(release)
			select {
			case n := <-received:
				if n != total {
					t.Fatalf("device received %d bytes, want %d", n, int64(total))
				}
			case <-time.After(60 * time.Second):
				t.Fatal("transfer did not finish after the device resumed reading")
			}
		})
	}
}

// The config endpoint serves signed-in clients and agents, advertises QUIC only under a reachable host name
// or the override, and carries the pin of the self-signed certificate.
func TestRelayConfigEndpoint(t *testing.T) {
	h := newRelayHarness(t)
	owner := h.createUser(t, "relay-config")
	session := h.session(t, owner)
	port := h.mux.Addr().(*net.UDPAddr).Port

	get := func(host string, header http.Header, cookie *http.Cookie) (int, relaymux.Config) {
		request, _ := http.NewRequest("GET", h.http.URL+relaymux.ConfigPath, nil)
		request.Host = host
		for key, values := range header {
			request.Header[key] = values
		}
		if cookie != nil {
			request.AddCookie(cookie)
		}
		response, err := h.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var config relaymux.Config
		_ = json.NewDecoder(response.Body).Decode(&config)
		return response.StatusCode, config
	}
	if status, _ := get("relay.example.com", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("anonymous config = HTTP %d", status)
	}
	status, config := get("relay.example.com", nil, session.cookie)
	if status != http.StatusOK || !config.Enabled || config.Version != relaymux.Version || config.WSPath != relaymux.WSPath ||
		config.Pin != h.pin || config.ALPN != relaymux.ALPN || len(config.QUIC) != 1 || config.QUIC[0] != (relaymux.Endpoint{Host: "relay.example.com", Port: port}) {
		t.Fatalf("client config = %d %+v", status, config)
	}
	for _, host := range []string{"127.0.0.1:8080", "localhost", "192.168.1.5", "nexterm"} {
		if _, config := get(host, nil, session.cookie); len(config.QUIC) != 0 || !config.Enabled {
			t.Fatalf("config under %s advertises %+v", host, config.QUIC)
		}
	}
	deviceID, secret := enrollWSAgent(t, h.httpFixture, owner, "relay-config-box")
	header := http.Header{agent.HeaderDeviceID: {deviceID}, "Authorization": {"Bearer " + secret}}
	if status, config := get("relay.example.com", header, nil); status != http.StatusOK || len(config.QUIC) != 1 {
		t.Fatalf("agent config = %d %+v", status, config)
	}
	header.Set("Authorization", "Bearer wrong")
	if status, _ := get("relay.example.com", header, nil); status != http.StatusForbidden {
		t.Fatalf("agent config with a bad secret = HTTP %d", status)
	}

	advertised := newRelayHarness(t, WithRelay(RelaySettings{QUIC: true, QUICAdvertise: []relaymux.Endpoint{{Host: "10.0.0.2", Port: 4443}}}))
	advertisedOwner := advertised.createUser(t, "relay-config-adv")
	request, _ := http.NewRequest("GET", advertised.http.URL+relaymux.ConfigPath, nil)
	request.AddCookie(advertised.session(t, advertisedOwner).cookie)
	response, err := advertised.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var override relaymux.Config
	_ = json.NewDecoder(response.Body).Decode(&override)
	if len(override.QUIC) != 1 || override.QUIC[0] != (relaymux.Endpoint{Host: "10.0.0.2", Port: 4443}) {
		t.Fatalf("override config = %+v", override)
	}

	off := newHTTPFixture(t, false, WithRelay(RelaySettings{QUIC: false}))
	off.service.SetRelayQUIC([]int{port}, h.pin)
	offOwner := off.createUser(t, "relay-config-off")
	call := off.call(t, "GET", relaymux.ConfigPath, nil, off.session(t, offOwner), "")
	if call.status != http.StatusOK || call.body["enabled"] != true || len(call.body["quic"].([]any)) != 0 || call.body["spki_sha256"] != nil {
		t.Fatalf("config with QUIC off = %d %v", call.status, call.body)
	}
}

// The relay websocket route is closed under --auth off like every fleet route.
func TestRelayWSClosedWhenAuthOff(t *testing.T) {
	f := newHTTPFixture(t, true)
	_, response, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(f.http.URL, "http")+relaymux.WSPath, nil)
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("relay ws under auth off = %v %v", response, err)
	}
}

func TestParseRelayEnv(t *testing.T) {
	lookup := func(env map[string]string) func(string) (string, bool) {
		return func(key string) (string, bool) { value, ok := env[key]; return value, ok }
	}
	settings, err := ParseRelayEnv(lookup(nil))
	if err != nil || !settings.QUIC || len(settings.QUICListen) != 0 {
		t.Fatalf("defaults = %+v, %v", settings, err)
	}
	settings, err = ParseRelayEnv(lookup(map[string]string{
		EnvRelayQUIC: "0", EnvRelayQUICListen: ":4433, 127.0.0.1:4434", EnvRelayQUICAdvertise: "relay.example.com:4433",
	}))
	if err != nil || settings.QUIC || len(settings.QUICListen) != 2 || settings.QUICAdvertise[0] != (relaymux.Endpoint{Host: "relay.example.com", Port: 4433}) {
		t.Fatalf("parsed = %+v, %v", settings, err)
	}
	for _, env := range []map[string]string{
		{EnvRelayQUIC: "sometimes"},
		{EnvRelayQUICListen: "no-port"},
		{EnvRelayQUICListen: "relay.example.com:4433"},
		{EnvRelayQUICAdvertise: "relay.example.com"},
		{EnvRelayQUICCert: "/etc/cert.pem"},
	} {
		if _, err := ParseRelayEnv(lookup(env)); err == nil {
			t.Fatalf("%v accepted", env)
		}
	}
}
