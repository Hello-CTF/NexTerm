package fleetserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	p2pclient "github.com/Hello-CTF/NexTerm/internal/p2p/client"
	"github.com/Hello-CTF/NexTerm/internal/p2p/selector"
	"github.com/Hello-CTF/NexTerm/internal/relaymux"
	"github.com/Hello-CTF/NexTerm/internal/relaymux/relayclient"
	"github.com/Hello-CTF/NexTerm/internal/rstream"
)

// relayClientLinks returns the server's live client links, which a test closes to drop the client's relay session.
func (s *Service) relayClientLinks() []*relaymux.Link {
	s.relay.mu.Lock()
	defer s.relay.mu.Unlock()
	agents := map[*relaymux.Link]bool{}
	for _, link := range s.relay.agents {
		agents[link] = true
	}
	var links []*relaymux.Link
	for link := range s.relay.links {
		if !agents[link] {
			links = append(links, link)
		}
	}
	return links
}

func (r *e2eRig) agentLink() *relaymux.Link { return r.f.service.relay.agent(r.device) }

func (r *e2eRig) relayOnly() {
	r.backend.mu.Lock()
	r.backend.mode = selector.ModeRelay
	r.backend.mu.Unlock()
}

// echoTerminal opens a terminal stream through the path manager and checks that size bytes come back exactly.
func (r *e2eRig) echoTerminal(t *testing.T, size int) {
	t.Helper()
	conn, err := r.manager.Dial(context.Background(), r.device, agent.PurposeTerminal)
	if err != nil {
		t.Fatalf("dial terminal: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, ok := conn.(*rstream.Stream); !ok {
		t.Fatalf("terminal stream is %T, want an rstream", conn)
	}
	tr := startE2ETransfer(t, conn, size)
	tr.write(t, 0, size)
	tr.finish(t)
}

// The relay compatibility matrix: each combination of client, agent and server carries a terminal stream end to
// end through the real fleet server, a real agent runtime and the integrated client dialer, over the path the
// combination calls for.
func TestRelayCompatibilityMatrix(t *testing.T) {
	cases := []struct {
		name    string
		options e2eOptions
		// clientMux: the client opens its streams on a relay link (no bridge); agentMux: the agent keeps a link.
		clientMux, agentMux bool
		carrier             string
	}{
		{name: "new client and agent over QUIC", options: e2eOptions{relay: true, agentCarrier: relaymux.KindQUIC, clientCarrier: relaymux.KindQUIC}, clientMux: true, agentMux: true, carrier: relaymux.KindQUIC},
		{name: "new client and agent over websocket", options: e2eOptions{relay: true, agentCarrier: relaymux.KindWS, clientCarrier: relaymux.KindWS}, clientMux: true, agentMux: true, carrier: relaymux.KindWS},
		{name: "new client and old agent", options: e2eOptions{relay: true, oldAgent: true, clientCarrier: relaymux.KindQUIC}, clientMux: true, carrier: relaymux.KindQUIC},
		{name: "old client and new agent", options: e2eOptions{relay: true, agentCarrier: relaymux.KindQUIC}, agentMux: true, carrier: relaymux.KindQUIC},
		{name: "server without the relay", options: e2eOptions{relay: true, agentCarrier: relaymux.KindWS, clientCarrier: relaymux.KindWS, relayConfig: "404"}},
		{name: "server with the relay disabled", options: e2eOptions{relay: true, agentCarrier: relaymux.KindWS, clientCarrier: relaymux.KindWS, relayConfig: "disabled"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newE2ERigWith(t, tc.options)
			rig.relayOnly()
			if tc.agentMux {
				rig.waitFor(t, "agent relay link", func() bool { return rig.agentLink() != nil })
				if kind := rig.agentLink().Kind(); kind != tc.carrier {
					t.Fatalf("agent link carrier = %s, want %s", kind, tc.carrier)
				}
			}
			rig.echoTerminal(t, 512<<10)
			legacy := rig.backend.legacyDials.Load()
			if tc.clientMux {
				if legacy != 0 {
					t.Fatalf("client dialed %d bridges, want every stream on the relay link", legacy)
				}
				if kind := rig.backend.relay.Kind(rig.backend.relayBase); kind != tc.carrier {
					t.Fatalf("client link carrier = %q, want %s", kind, tc.carrier)
				}
			} else if legacy == 0 {
				t.Fatal("client opened no bridge, want the per-operation bridge")
			}
			if !tc.agentMux {
				// Nothing ever offered or kept a link for the agent: the server reached it over a bridge.
				if link := rig.agentLink(); link != nil {
					t.Fatalf("agent keeps a %s relay link, want none", link.Kind())
				}
			}
		})
	}
}

// A terminal stream on the relay link survives the client's link dropping mid-transfer: the client redials the
// link, resumes the stream on it (resume=1 over the mux, no bridge), the agent's link stays the same, and every
// byte arrives exactly once.
func TestRelayMuxTerminalResumesAfterLinkDrop(t *testing.T) {
	for _, carrier := range relayCarriers {
		t.Run(carrier, func(t *testing.T) {
			rig := newE2ERigWith(t, e2eOptions{relay: true, agentCarrier: carrier, clientCarrier: carrier})
			rig.relayOnly()
			rig.waitFor(t, "agent relay link", func() bool { return rig.agentLink() != nil })
			agentLink := rig.agentLink()
			conn, err := rig.manager.Dial(context.Background(), rig.device, agent.PurposeTerminal)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			const size = 2 << 20
			tr := startE2ETransfer(t, conn, size)
			tr.write(t, 0, size/2)
			links := rig.f.service.relayClientLinks()
			if len(links) != 1 {
				t.Fatalf("server holds %d client links, want 1", len(links))
			}
			_ = links[0].Close()
			tr.write(t, size/2, size)
			tr.finish(t)
			if got := rig.backend.legacyDials.Load(); got != 0 {
				t.Fatalf("client dialed %d bridges, want the resume on the relay link", got)
			}
			if rig.agentLink() != agentLink {
				t.Fatal("the agent's relay link changed; the resume must reach the agent over the same link")
			}
			if links := rig.f.service.relayClientLinks(); len(links) != 1 {
				t.Fatalf("server holds %d client links after the resume, want 1", len(links))
			}
			resumes := 0
			for _, row := range auditPayloadRows(t, rig.f.service.db, auditSourceFleet, auditKindDeviceTerminal) {
				if row["action"] == "terminal_open" && row["resume"] == true {
					resumes++
				}
			}
			if resumes == 0 {
				t.Fatal("no resume was audited")
			}
		})
	}
}

// A stream moves relay (mux) -> direct -> relay (mux) without losing a byte, the relay legs on the mux only.
func TestRelayMuxP2PMigration(t *testing.T) {
	rig := newE2ERigWith(t, e2eOptions{relay: true, agentCarrier: relaymux.KindQUIC, clientCarrier: relaymux.KindQUIC})
	rig.waitFor(t, "agent relay link", func() bool { return rig.agentLink() != nil })
	conn, err := rig.manager.Dial(context.Background(), rig.device, agent.PurposeTerminal)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	const size = 2 << 20
	tr := startE2ETransfer(t, conn, size)
	tr.write(t, 0, size/4)
	rig.waitDirect(t, 1)
	if status := rig.manager.Status(context.Background(), rig.device); status.Path != p2pclient.PathDirect {
		t.Fatalf("status = %+v", status)
	}
	tr.write(t, size/4, size/2)
	_ = rig.legs.last().Close()
	tr.write(t, size/2, 3*size/4)
	rig.waitDirect(t, 0)
	tr.write(t, 3*size/4, size)
	tr.finish(t)
	if got := rig.backend.legacyDials.Load(); got != 0 {
		t.Fatalf("client dialed %d bridges, want relay legs and signaling on the mux", got)
	}
}

// A refused relay stream carries the status, code and message the bridge endpoint answers for the same request,
// so the client maps both to the same ipc error.
func TestRelayMuxRefusalMatchesBridge(t *testing.T) {
	rig := newE2ERigWith(t, e2eOptions{relay: true, agentCarrier: relaymux.KindWS, clientCarrier: relaymux.KindWS})
	rig.waitFor(t, "agent relay link", func() bool { return rig.agentLink() != nil })
	if call := rig.f.call(t, http.MethodPost, "/fleet/devices/"+rig.device+"/terminal-access", map[string]any{"enabled": false}, rig.backend.session, rig.backend.session.csrf); call.status != http.StatusOK {
		t.Fatalf("disable terminal access = %d %v", call.status, call.body)
	}
	for _, purpose := range []string{agent.PurposeTerminal, "bogus"} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _, response, bridgeErr := dialStreamRelay(ctx, rig.f, rig.device, rig.backend.session.cookie, purpose, false)
		if bridgeErr == nil || response == nil {
			t.Fatalf("%s bridge was not refused: %v", purpose, bridgeErr)
		}
		_, _, relayErr := rig.backend.relay.DialDevice(ctx, rig.backend.relaySession(), rig.device, purpose, true, false, func(context.Context) (net.Conn, bool, error) {
			t.Fatal("a refusal fell back to the bridge")
			return nil, false, nil
		})
		cancel()
		reply, refused := relaymux.Refused(relayErr)
		if !refused {
			t.Fatalf("%s relay error = %v, want a refusal", purpose, relayErr)
		}
		status, code, message := bridgeFailure(t, response)
		if reply.Status != status || reply.Code != code || reply.Message != message {
			t.Fatalf("%s relay refusal = %d %s %q, bridge = %d %s %q", purpose, reply.Status, reply.Code, reply.Message, status, code, message)
		}
	}
	// A revoked login is a 401 on both: the client re-logs in.
	if err := rig.f.accounts.RevokeSession(context.Background(), rig.backend.session.id); err != nil {
		t.Fatal(err)
	}
	if err := rig.f.service.CloseAccountSession(rig.backend.session.id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rig.waitFor(t, "client link closed by the revoke", func() bool { return len(rig.f.service.relayClientLinks()) == 0 })
	_, _, err := rig.backend.relay.DialDevice(ctx, rig.backend.relaySession(), rig.device, agent.PurposeTerminal, true, false, func(context.Context) (net.Conn, bool, error) {
		t.Fatal("a rejected login fell back to the bridge")
		return nil, false, nil
	})
	if !errors.Is(err, relayclient.ErrSessionExpired) {
		t.Fatalf("relay dial after revoke = %v, want the session-expired error", err)
	}
	if _, _, response, _ := dialStreamRelay(ctx, rig.f, rig.device, rig.backend.session.cookie, agent.PurposeTerminal, false); response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bridge after revoke = %v, want 401", response)
	}
}

// bridgeFailure reads the status and the ipc error of a refused bridge upgrade.
func bridgeFailure(t *testing.T, response *http.Response) (int, string, string) {
	t.Helper()
	var failure struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&failure); err != nil {
		t.Fatalf("bridge refusal body: %v", err)
	}
	return response.StatusCode, failure.Error.Code, failure.Error.Message
}

// BenchmarkRelayOperationOpen measures opening a terminal operation through the integrated client dialer up to
// its first echoed bytes: an rstream on a relay link stream (QUIC or websocket) against one on a per-operation
// bridge. The link itself is dialed before the timer starts, as it is kept for the client's lifetime.
func BenchmarkRelayOperationOpen(b *testing.B) {
	cases := []struct {
		name    string
		options e2eOptions
	}{
		{name: "mux-quic", options: e2eOptions{relay: true, agentCarrier: relaymux.KindQUIC, clientCarrier: relaymux.KindQUIC}},
		{name: "mux-ws", options: e2eOptions{relay: true, agentCarrier: relaymux.KindWS, clientCarrier: relaymux.KindWS}},
		{name: "bridge", options: e2eOptions{relay: true, oldAgent: true}},
	}
	// The first four bytes name an oversized frame, so the helper bridge's frame filter passes the bytes through.
	probe := []byte{0xff, 0xff, 0xff, 0xff, 0x01}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			rig := newE2ERigWith(b, tc.options)
			if tc.options.clientCarrier != "" {
				rig.waitFor(b, "agent relay link", func() bool { return rig.agentLink() != nil })
			}
			buffer := make([]byte, len(probe))
			open := func() {
				ctx := context.Background()
				carrier, stream, err := rig.backend.DialRelay(ctx, rig.device, agent.PurposeTerminal, false)
				if err != nil || !stream {
					b.Fatalf("dial = stream %v, %v", stream, err)
				}
				conn, err := rstream.Open(ctx, carrier, agent.PurposeTerminal, rstream.Options{})
				if err != nil {
					b.Fatal(err)
				}
				if _, err := conn.Write(probe); err != nil {
					b.Fatal(err)
				}
				if _, err := io.ReadFull(conn, buffer); err != nil {
					b.Fatal(err)
				}
				_ = conn.Close()
			}
			open()
			if tc.options.clientCarrier != "" && rig.backend.legacyDials.Load() != 0 {
				b.Fatal("the mux case dialed a bridge")
			}
			b.ResetTimer()
			for b.Loop() {
				open()
			}
		})
	}
}

// The browser-mode device connector reaches an agent that keeps a relay link over a stream of it, and an agent
// without one over a bridge, with the same bytes either way.
func TestRequestDeviceBridgePurposePrefersTheRelayLink(t *testing.T) {
	for _, oldAgent := range []bool{false, true} {
		rig := newE2ERigWith(t, e2eOptions{relay: true, agentCarrier: relaymux.KindWS, oldAgent: oldAgent})
		if !oldAgent {
			rig.waitFor(t, "agent relay link", func() bool { return rig.agentLink() != nil })
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		conn, err := rig.f.service.RequestDeviceBridgePurpose(ctx, rig.device, agent.PurposeTerminal)
		cancel()
		if err != nil {
			t.Fatalf("old agent %v: %v", oldAgent, err)
		}
		if _, bridged := conn.(*wsConn); bridged != oldAgent {
			t.Fatalf("old agent %v: connection %T", oldAgent, conn)
		}
		tr := startE2ETransfer(t, conn, 256<<10)
		tr.write(t, 0, 256<<10)
		tr.finish(t)
		_ = conn.Close()
	}
}
