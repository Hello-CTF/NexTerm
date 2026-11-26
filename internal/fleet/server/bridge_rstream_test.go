package fleetserver

import (
	"bytes"
	"context"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/rstream"
	"github.com/coder/websocket"
)

// streamAgent plays an rstream-capable agent: it answers every bridge request by dialing the bridge and handing it
// to an rstream server, and echoes every new stream.
type streamAgent struct {
	t        *testing.T
	f        *httpFixture
	deviceID string
	secret   string
	server   *rstream.Server

	mu      sync.Mutex
	bridges []*websocket.Conn
	resumes int
	raw     int
}

func (a *streamAgent) run(ctx context.Context, control *websocket.Conn) {
	for {
		var request controlMessage
		if err := readJSONMessage(ctx, control, &request); err != nil {
			return
		}
		if request.Type != "bridge" {
			continue
		}
		go a.serve(ctx, request)
	}
}

func (a *streamAgent) serve(ctx context.Context, request controlMessage) {
	bridge, err := dialAgentBridge(ctx, a.f, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: a.deviceID, Secret: a.secret,
		BridgeID: request.BridgeID, Purpose: request.Purpose, SessionEnv: true,
	})
	if err != nil {
		return
	}
	a.mu.Lock()
	a.bridges = append(a.bridges, bridge)
	if !request.Stream {
		a.raw++
	}
	a.mu.Unlock()
	carrier := websocket.NetConn(ctx, bridge, websocket.MessageBinary)
	if !request.Stream {
		_, _ = io.Copy(carrier, carrier)
		_ = carrier.Close()
		return
	}
	conn, resumed, err := a.server.Accept(ctx, carrier, request.Purpose)
	if err != nil {
		return
	}
	if resumed {
		a.mu.Lock()
		a.resumes++
		a.mu.Unlock()
		return
	}
	_, _ = io.Copy(conn, conn)
	_ = conn.(*rstream.Stream).CloseWrite()
	_, _ = io.Copy(io.Discard, conn)
	_ = conn.Close()
}

// dropBridge closes the agent side of the n-th bridge abruptly, as a network failure would.
func (a *streamAgent) dropBridge(n int) {
	a.mu.Lock()
	bridge := a.bridges[n]
	a.mu.Unlock()
	bridge.CloseNow()
}

func (a *streamAgent) counts() (bridges, resumes, raw int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.bridges), a.resumes, a.raw
}

// dialStreamRelay dials the relay the way the desktop does for a stream carrier and reports whether the server
// agreed to carry a stream.
func dialStreamRelay(ctx context.Context, f *httpFixture, deviceID string, cookie *http.Cookie, purpose string, resume bool) (net.Conn, bool, *http.Response, error) {
	url := "ws" + strings.TrimPrefix(f.http.URL, "http") + "/fleet/devices/" + deviceID + "/bridge?purpose=" + purpose + "&stream=1"
	if resume {
		url += "&resume=1"
	}
	conn, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": []string{cookie.Name + "=" + cookie.Value}},
	})
	if err != nil {
		return nil, false, response, err
	}
	conn.SetReadLimit(1 << 20)
	return websocket.NetConn(context.Background(), conn, websocket.MessageBinary), response.Header.Get(rstream.RelayStreamHeader) == "1", response, nil
}

// A stream bridge through the real relay survives a websocket drop mid-transfer: the client re-dials a resume
// bridge, the agent re-attaches the stream, and every byte arrives exactly once. Opens and resumes audit as the
// inner purpose's action.
func TestDeviceBridgeRelayStreamResumesAfterDrop(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "relay-stream")
	deviceID, secret := enrollWSAgent(t, f, owner, "relay-stream-box")
	session := f.session(t, owner)

	control := dialDeviceWS(t, f)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret, SessionEnv: true,
		Capabilities: []string{agent.PurposeTerminal, deviceproto.PurposeForward, rstream.Capability},
	}); response.Type != "hello_ok" {
		t.Fatalf("hello = %+v", response)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	device := &streamAgent{t: t, f: f, deviceID: deviceID, secret: secret, server: rstream.NewServer(rstream.ServerOptions{})}
	defer func() { _ = device.server.Close() }()
	go device.run(ctx, control)

	carrier, stream, _, err := dialStreamRelay(ctx, f, deviceID, session.cookie, deviceproto.PurposeForward, false)
	if err != nil || !stream {
		t.Fatalf("stream relay dial = stream %v, %v", stream, err)
	}
	client, err := rstream.Open(ctx, carrier, deviceproto.PurposeForward, rstream.Options{})
	if err != nil {
		t.Fatal(err)
	}
	client.ResumeWith(func(ctx context.Context) (net.Conn, error) {
		conn, stream, _, err := dialStreamRelay(ctx, f, deviceID, session.cookie, deviceproto.PurposeForward, true)
		if err == nil && !stream {
			_ = conn.Close()
			return nil, rstream.Permanent(io.ErrUnexpectedEOF)
		}
		return conn, err
	})

	payload := make([]byte, 2<<20)
	rng := rand.New(rand.NewPCG(7, 7))
	for i := range payload {
		payload[i] = byte(rng.Uint32())
	}
	writeErr := make(chan error, 1)
	go func() {
		_, err := client.Write(payload)
		writeErr <- err
	}()
	echoed := make([]byte, 0, len(payload))
	buffer := make([]byte, 32*1024)
	dropped := false
	for len(echoed) < len(payload) {
		n, err := client.Read(buffer)
		echoed = append(echoed, buffer[:n]...)
		if err != nil {
			t.Fatalf("read after %d bytes: %v", len(echoed), err)
		}
		if !dropped && len(echoed) >= len(payload)/4 {
			device.dropBridge(0)
			dropped = true
		}
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write: %v", err)
	}
	if !bytes.Equal(echoed, payload) {
		t.Fatal("echo through a resumed stream bridge differs from what was sent")
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if rest, err := io.ReadAll(client); err != nil || len(rest) != 0 {
		t.Fatalf("read to EOF = %d bytes, %v", len(rest), err)
	}
	_ = client.Close()
	select {
	case <-client.Done():
	case <-ctx.Done():
		t.Fatal("stream did not finish")
	}
	if bridges, resumes, raw := device.counts(); bridges < 2 || resumes < 1 || raw != 0 {
		t.Fatalf("agent saw %d bridges, %d resumes, %d raw; want a resume after the drop", bridges, resumes, raw)
	}

	audits := auditPayloadRows(t, f.service.db, auditSourceFleet, auditKindDeviceTerminal)
	if len(audits) < 2 || audits[0]["action"] != "forward_open" || audits[0]["stream"] != true || audits[0]["resume"] != nil {
		t.Fatalf("stream open audit = %v", audits)
	}
	if last := audits[len(audits)-1]; last["action"] != "forward_open" || last["stream"] != true || last["resume"] != true {
		t.Fatalf("resume audit = %v", last)
	}
}

// Without the agent capability a stream request falls back to a raw bridge (no stream header), and a resume
// request is refused before the upgrade.
func TestDeviceBridgeRelayStreamNeedsCapability(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "relay-raw")
	deviceID, secret := enrollWSAgent(t, f, owner, "relay-raw-box")
	session := f.session(t, owner)

	control := dialDeviceWS(t, f)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret, SessionEnv: true,
		Capabilities: []string{agent.PurposeTerminal, deviceproto.PurposeForward},
	}); response.Type != "hello_ok" {
		t.Fatalf("hello = %+v", response)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	device := &streamAgent{t: t, f: f, deviceID: deviceID, secret: secret, server: rstream.NewServer(rstream.ServerOptions{})}
	go device.run(ctx, control)

	conn, stream, _, err := dialStreamRelay(ctx, f, deviceID, session.cookie, deviceproto.PurposeForward, false)
	if err != nil || stream {
		t.Fatalf("stream request to an agent without the capability = stream %v, %v; want a raw bridge", stream, err)
	}
	if _, err := conn.Write([]byte("raw")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 3)
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "raw" {
		t.Fatalf("raw echo = %q, %v", got, err)
	}
	_ = conn.Close()
	if _, _, raw := device.counts(); raw != 1 {
		t.Fatalf("agent was asked for %d raw bridges, want 1", raw)
	}

	_, _, response, err := dialStreamRelay(ctx, f, deviceID, session.cookie, deviceproto.PurposeForward, true)
	if err == nil || response == nil || response.StatusCode != http.StatusConflict {
		t.Fatalf("resume without the capability = %v, %v; want 409", response, err)
	}
}
