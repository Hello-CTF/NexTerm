package fleetserver

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/coder/websocket"
)

// wsPair builds a real websocket pair: it returns the client-side and server-side connections.
func wsPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		accepted <- conn
	}))
	t.Cleanup(httpServer.Close)
	client, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	server := <-accepted
	t.Cleanup(func() {
		client.CloseNow()
		server.CloseNow()
	})
	return client, server
}

type bridgeOutcome struct {
	conn net.Conn
	err  error
}

// startBridgeRequest registers the control channel and starts a RequestBridge, returning the cancel function, the result channel,
// and the bridgeID read from the control channel.
func startBridgeRequest(t *testing.T, r *Registry, deviceID string) (context.CancelFunc, chan bridgeOutcome, string) {
	t.Helper()
	controlClient, controlServer := wsPair(t)
	r.RegisterControl(deviceID, controlServer, "", []string{agent.PurposeTerminal})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan bridgeOutcome, 1)
	go func() {
		conn, err := r.RequestBridge(ctx, deviceID, agent.PurposeTerminal)
		result <- bridgeOutcome{conn: conn, err: err}
	}()
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	_, raw, err := controlClient.Read(readCtx)
	if err != nil {
		t.Fatal(err)
	}
	var message controlMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatal(err)
	}
	if message.Type != "bridge" || message.BridgeID == "" || message.Purpose != agent.PurposeTerminal {
		t.Fatalf("control message = %+v, want terminal bridge request", message)
	}
	return cancel, result, message.BridgeID
}

func bridgeIDs(r *Registry) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bridges)
}

func pendingIDs(r *Registry) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending)
}

// Abandon before pairing: after the waiter cancels, pairing with the same bridgeID must be rejected and the conn must not be registered.
func TestRegistryAbandonBeforePair(t *testing.T) {
	r := NewRegistry()
	cancel, result, bridgeID := startBridgeRequest(t, r, "dev-abandon-pair")
	cancel()
	if outcome := <-result; outcome.err != errDeviceOffline {
		t.Fatalf("RequestBridge = %v, want errDeviceOffline", outcome.err)
	}
	_, agentConn := wsPair(t)
	if _, err := r.PairBridge("dev-abandon-pair", bridgeID, "", newWSConn(agentConn)); err != errBridgeNotPending {
		t.Fatalf("PairBridge = %v, want errBridgeNotPending", err)
	}
	if count := bridgeIDs(r); count != 0 {
		t.Fatalf("bridges = %d, want 0", count)
	}
	if count := pendingIDs(r); count != 0 {
		t.Fatalf("pending = %d, want 0", count)
	}
}

// Abandon before delivery: when already paired but the waiter has canceled, deliver must close the conn and remove it from the active table,
// never sending it into a channel nobody consumes.
func TestRegistryAbandonBeforeDeliverClosesConn(t *testing.T) {
	r := NewRegistry()
	cancel, result, bridgeID := startBridgeRequest(t, r, "dev-abandon-deliver")
	agentClient, agentConn := wsPair(t)
	deliver, err := r.PairBridge("dev-abandon-deliver", bridgeID, "", newWSConn(agentConn))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if outcome := <-result; outcome.err != errDeviceOffline {
		t.Fatalf("RequestBridge = %v, want errDeviceOffline", outcome.err)
	}
	deliver()
	if count := bridgeIDs(r); count != 0 {
		t.Fatalf("bridges = %d, want 0", count)
	}
	if count := pendingIDs(r); count != 0 {
		t.Fatalf("pending = %d, want 0", count)
	}
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	if _, _, err := agentClient.Read(readCtx); err == nil {
		t.Fatal("agent bridge connection still open after abandoned deliver")
	}
}

// Delivery before abandon: the conn is taken by the waiter normally; after close and unregistration the active table is empty.
func TestRegistryDeliverBeforeAbandon(t *testing.T) {
	r := NewRegistry()
	cancel, result, bridgeID := startBridgeRequest(t, r, "dev-deliver-first")
	defer cancel()
	agentClient, agentConn := wsPair(t)
	conn := newWSConn(agentConn)
	deliver, err := r.PairBridge("dev-deliver-first", bridgeID, "", conn)
	if err != nil {
		t.Fatal(err)
	}
	deliver()
	outcome := <-result
	if outcome.err != nil {
		t.Fatalf("RequestBridge = %v, want delivered conn", outcome.err)
	}
	if outcome.conn != net.Conn(conn) {
		t.Fatal("RequestBridge returned a different conn")
	}
	// After the caller closes the conn (as if the relay dropped), unregistration must leave the active table empty.
	_ = conn.Close()
	r.UnregisterBridge(conn)
	if count := bridgeIDs(r); count != 0 {
		t.Fatalf("bridges = %d, want 0", count)
	}
	if count := pendingIDs(r); count != 0 {
		t.Fatalf("pending = %d, want 0", count)
	}
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	if _, _, err := agentClient.Read(readCtx); err == nil {
		t.Fatal("agent bridge connection still open after caller close")
	}
}

// Double-ready interleave: deliver and cancel race concurrently; whichever side select picks, the conn must never leak -
// either the conn goes to the caller (the test closes it and unregisters), or the abandoning party drains and closes it.
func TestRegistryDeliverAbandonDoubleReadyNoLeak(t *testing.T) {
	for iteration := 0; iteration < 30; iteration++ {
		r := NewRegistry()
		deviceID := "dev-double-ready"
		cancel, result, bridgeID := startBridgeRequest(t, r, deviceID)
		agentClient, agentConn := wsPair(t)
		conn := newWSConn(agentConn)
		deliver, err := r.PairBridge(deviceID, bridgeID, "", conn)
		if err != nil {
			t.Fatal(err)
		}
		go deliver()
		cancel()
		outcome := <-result
		if outcome.conn != nil {
			// select picked delivery: the caller takes over ownership and closes it.
			_ = outcome.conn.Close()
		}
		// In the production path serveDeviceBridge unregisters after the conn closes (defer UnregisterBridge);
		// here the test plays the handler role, and under both interleavings the conn must already be closed at this point.
		r.UnregisterBridge(conn)
		if count := bridgeIDs(r); count != 0 {
			t.Fatalf("iteration %d: bridges = %d, want 0", iteration, count)
		}
		if count := pendingIDs(r); count != 0 {
			t.Fatalf("iteration %d: pending = %d, want 0", iteration, count)
		}
		readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if _, _, readErr := agentClient.Read(readCtx); readErr == nil {
			readCancel()
			t.Fatalf("iteration %d: agent bridge connection still open", iteration)
		}
		readCancel()
	}
}

// Capabilities round-trip: the hello-advertised purposes are stored with the control channel and readable
// while the device is online; unregistration drops them.
func TestRegistryCapabilitiesRoundTrip(t *testing.T) {
	r := NewRegistry()
	_, controlServer := wsPair(t)
	r.RegisterControl("dev-caps", controlServer, "digest-1", []string{"terminal", "file"})
	capabilities, ok := r.Capabilities("dev-caps")
	if !ok || len(capabilities) != 2 || capabilities[0] != "terminal" || capabilities[1] != "file" {
		t.Fatalf("Capabilities = %v, %v", capabilities, ok)
	}
	if digest, ok := r.StateDigest("dev-caps"); !ok || digest != "digest-1" {
		t.Fatalf("StateDigest = %q, %v", digest, ok)
	}
	capabilities[0] = "mutated"
	if again, _ := r.Capabilities("dev-caps"); again[0] != "terminal" {
		t.Fatalf("Capabilities leaked the stored slice: %v", again)
	}
	r.UnregisterControl("dev-caps", controlServer)
	if _, ok := r.Capabilities("dev-caps"); ok {
		t.Fatal("Capabilities reported for an offline device")
	}
}

// Every purpose, including both terminal spellings, requires an explicit capability. A missing capability
// is rejected before any control frame is written or pending bridge is allocated.
func TestRequestBridgeRejectsMissingCapabilities(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		purpose string
	}{
		{name: "file", purpose: "file"},
		{name: "terminal", purpose: agent.PurposeTerminal},
		{name: "empty terminal", purpose: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			r := NewRegistry()
			controlClient, controlServer := wsPair(t)
			r.RegisterControl("dev-no-capabilities", controlServer, "", nil)
			requestCtx, requestCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			_, err := r.RequestBridge(requestCtx, "dev-no-capabilities", testCase.purpose)
			requestCancel()
			if err != errPurposeUnsupported {
				t.Fatalf("RequestBridge = %v, want errPurposeUnsupported", err)
			}
			if count := pendingIDs(r); count != 0 {
				t.Fatalf("pending = %d, want 0", count)
			}
			readCtx, readCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer readCancel()
			if _, _, err := controlClient.Read(readCtx); err == nil {
				t.Fatal("a control frame was written without the required capability")
			}
		})
	}
}

// An advertised purpose passes the gate and reaches the control channel with the purpose on the wire;
// pairing then accepts only a bridge hello echoing the same purpose.
func TestRequestBridgeAdvertisedPurpose(t *testing.T) {
	r := NewRegistry()
	controlClient, controlServer := wsPair(t)
	r.RegisterControl("dev-file", controlServer, "", []string{"terminal", "file"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan bridgeOutcome, 1)
	go func() {
		conn, err := r.RequestBridge(ctx, "dev-file", "file")
		result <- bridgeOutcome{conn: conn, err: err}
	}()
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, raw, err := controlClient.Read(readCtx)
	readCancel()
	if err != nil {
		t.Fatal(err)
	}
	var message controlMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatal(err)
	}
	if message.Type != "bridge" || message.Purpose != "file" {
		t.Fatalf("control message = %+v, want purpose=file bridge request", message)
	}
	_, terminalConn := wsPair(t)
	if _, err := r.PairBridge("dev-file", message.BridgeID, "", newWSConn(terminalConn)); err != errBridgeNotPending {
		t.Fatalf("PairBridge with terminal echo = %v, want errBridgeNotPending", err)
	}
	_, fileConn := wsPair(t)
	conn := newWSConn(fileConn)
	deliver, err := r.PairBridge("dev-file", message.BridgeID, "file", conn)
	if err != nil {
		t.Fatal(err)
	}
	deliver()
	outcome := <-result
	if outcome.err != nil {
		t.Fatalf("RequestBridge = %v, want delivered conn", outcome.err)
	}
	_ = outcome.conn.Close()
	r.UnregisterBridge(conn)
	if count := bridgeIDs(r); count != 0 {
		t.Fatalf("bridges = %d, want 0", count)
	}
}

func TestPurposeUnsupportedMessageNamesPurposeAndUpgrade(t *testing.T) {
	for _, purpose := range []string{"forward", ""} {
		message := purposeUnsupportedMessage(purpose)
		if !strings.Contains(message, "升级") {
			t.Fatalf("message %q for %q should tell the user to upgrade the agent", message, purpose)
		}
	}
	if !strings.Contains(purposeUnsupportedMessage("forward"), "forward") || !strings.Contains(purposeUnsupportedMessage(""), "terminal") {
		t.Fatal("message should name the missing purpose, defaulting to terminal")
	}
}
