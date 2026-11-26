package fleetserver

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/relaymux"
	"github.com/coder/websocket"
)

// The benchmarks compare opening one device operation: a stream on an existing relay session against today's
// per-operation bridge, where the client upgrades a websocket and the agent dials a second one to pair. Every
// operation is authorized and audited the same way, and each one carries one echoed byte. The bridge runs over
// plain HTTP and over TLS (the deployed case: two TLS handshakes per operation); the relay's QUIC carrier is
// always TLS, its handshake paid once per session.

func benchmarkEcho(b *testing.B, conn io.ReadWriteCloser) {
	if _, err := conn.Write([]byte{1}); err != nil {
		b.Fatal(err)
	}
	var one [1]byte
	if _, err := io.ReadFull(conn, one[:]); err != nil {
		b.Fatal(err)
	}
	_ = conn.Close()
}

func BenchmarkRelayMuxOpenStream(b *testing.B) {
	for _, carrier := range relayCarriers {
		b.Run(carrier, func(b *testing.B) {
			h := newRelayHarness(b)
			owner := h.createUser(b, "bench-mux-"+carrier)
			deviceID, secret := enrollWSAgent(b, h.httpFixture, owner, "bench-mux-box")
			h.startAgent(b, carrier, deviceID, secret, echoStream)
			client := h.clientLink(b, carrier, h.session(b, owner))
			header := relaymux.StreamHeader{Purpose: agent.PurposeTerminal, DeviceID: deviceID}
			b.ResetTimer()
			for b.Loop() {
				conn, _, err := client.OpenDeviceStream(context.Background(), header)
				if err != nil {
					b.Fatal(err)
				}
				benchmarkEcho(b, conn)
			}
		})
	}
}

func BenchmarkBridgeRelayPerOperation(b *testing.B) {
	for _, secure := range []bool{false, true} {
		name := "http"
		if secure {
			name = "tls"
		}
		b.Run(name, func(b *testing.B) {
			h := newRelayHarness(b)
			base := h.http.URL
			httpClient := http.DefaultClient
			if secure {
				server := httptest.NewUnstartedServer(h.service.Handler())
				server.StartTLS()
				b.Cleanup(server.Close)
				base = server.URL
				httpClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
			}
			wsBase := "ws" + strings.TrimPrefix(base, "http")
			owner := h.createUser(b, "bench-bridge-"+name)
			deviceID, secret := enrollWSAgent(b, h.httpFixture, owner, "bench-bridge-box")
			ctx, cancel := context.WithCancel(context.Background())
			b.Cleanup(cancel)
			control, _, err := websocket.Dial(ctx, wsBase+agent.PathDeviceWS, &websocket.DialOptions{HTTPClient: httpClient})
			if err != nil {
				b.Fatal(err)
			}
			hello := agent.HelloMessage{Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret, SessionEnv: true,
				Capabilities: []string{agent.PurposeTerminal}}
			if err := writeJSONMessage(ctx, control, hello); err != nil {
				b.Fatal(err)
			}
			var ok controlMessage
			if err := readJSONMessage(ctx, control, &ok); err != nil || ok.Type != "hello_ok" {
				b.Fatalf("hello = %+v, %v", ok, err)
			}
			// The agent answers every bridge request with an outbound bridge that echoes.
			go func() {
				for {
					var request controlMessage
					if err := readJSONMessage(ctx, control, &request); err != nil {
						return
					}
					if request.Type != "bridge" {
						continue
					}
					go func() {
						bridge, _, err := websocket.Dial(ctx, wsBase+agent.PathDeviceWS, &websocket.DialOptions{HTTPClient: httpClient})
						if err != nil {
							return
						}
						bridgeHello := hello
						bridgeHello.BridgeID, bridgeHello.Purpose, bridgeHello.Capabilities = request.BridgeID, request.Purpose, nil
						if err := writeJSONMessage(ctx, bridge, bridgeHello); err != nil {
							return
						}
						var paired controlMessage
						if err := readJSONMessage(ctx, bridge, &paired); err != nil || paired.Type != "hello_ok" {
							bridge.CloseNow()
							return
						}
						conn := websocket.NetConn(ctx, bridge, websocket.MessageBinary)
						_, _ = io.Copy(conn, conn)
						_ = conn.Close()
					}()
				}
			}()
			header := http.Header{"Cookie": {sessionCookieName + "=" + h.session(b, owner).cookie.Value}}
			url := wsBase + "/fleet/devices/" + deviceID + "/bridge?purpose=terminal"
			b.ResetTimer()
			for b.Loop() {
				conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: httpClient, HTTPHeader: header})
				if err != nil {
					b.Fatal(err)
				}
				benchmarkEcho(b, websocket.NetConn(ctx, conn, websocket.MessageBinary))
			}
		})
	}
}

func writeJSONMessage(ctx context.Context, conn *websocket.Conn, message any) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, payload)
}
