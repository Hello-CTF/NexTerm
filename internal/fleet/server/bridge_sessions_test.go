package fleetserver

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"github.com/coder/websocket"
)

type pairedRelay struct {
	user  *websocket.Conn
	agent *websocket.Conn
}

// bridgeRelayFixture is one online device with a control channel that answers bridge requests on demand.
type bridgeRelayFixture struct {
	*httpFixture
	deviceID string
	secret   string
	control  *websocket.Conn
}

func newBridgeRelayFixture(t *testing.T, name string) (*bridgeRelayFixture, *httpSession, *httpSession) {
	t.Helper()
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, name)
	deviceID, secret := enrollWSAgent(t, f, owner, name+"-box")
	control := dialDeviceWS(t, f)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
		Capabilities: []string{agent.PurposeTerminal},
	}); response.Type != "hello_ok" {
		t.Fatalf("hello = %+v", response)
	}
	return &bridgeRelayFixture{httpFixture: f, deviceID: deviceID, secret: secret, control: control}, f.session(t, owner), f.session(t, owner)
}

func (f *bridgeRelayFixture) open(t *testing.T, session *httpSession) pairedRelay {
	t.Helper()
	user := dialRelay(t, f.httpFixture, f.deviceID, session.cookie, "")
	request := readControlMessage(t, f.control, 30*time.Second)
	if request.Type != "bridge" || request.BridgeID == "" {
		t.Fatalf("control message = %+v, want bridge request", request)
	}
	bridge := dialDeviceWS(t, f.httpFixture)
	if response := helloDevice(t, bridge, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: f.deviceID, Secret: f.secret, BridgeID: request.BridgeID,
	}); response.Type != "hello_ok" {
		t.Fatalf("bridge hello = %+v", response)
	}
	relay := pairedRelay{user: user, agent: bridge}
	relay.assertFlows(t)
	return relay
}

// assertFlows round-trips one message through the relay, user to agent.
func (r pairedRelay) assertFlows(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.user.Write(ctx, websocket.MessageBinary, []byte("ping")); err != nil {
		t.Fatalf("relay write: %v", err)
	}
	if _, raw, err := r.agent.Read(ctx); err != nil || string(raw) != "ping" {
		t.Fatalf("agent read = %q %v", raw, err)
	}
}

// assertRevoked waits for the user side to close with the relogin policy violation and the agent side to end.
func (r pairedRelay) assertRevoked(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, err := r.user.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("relay read err = %v, want policy violation close", err)
	}
	var closeErr websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Reason != bridgeSessionReloginMessage {
		t.Fatalf("relay close = %v, want reason %q", err, bridgeSessionReloginMessage)
	}
	if _, _, err := r.agent.Read(ctx); err == nil {
		t.Fatal("agent bridge still open after the relay session was revoked")
	}
}

func TestBridgeRelayClosesWithRevokedSessionOnly(t *testing.T) {
	f, first, second := newBridgeRelayFixture(t, "relay-revoke")
	revoked := f.open(t, first)
	kept := f.open(t, second)

	if err := f.service.CloseAccountSession(first.id); err != nil {
		t.Fatal(err)
	}
	revoked.assertRevoked(t)
	kept.assertFlows(t)
}

func TestBridgeRelayClosesOnUserRevocation(t *testing.T) {
	t.Run("all sessions", func(t *testing.T) {
		f, first, second := newBridgeRelayFixture(t, "relay-logout-all")
		relays := []pairedRelay{f.open(t, first), f.open(t, second)}
		f.service.CloseAccountUser(first.user.ID, "")
		for _, relay := range relays {
			relay.assertRevoked(t)
		}
	})
	t.Run("except the current session", func(t *testing.T) {
		f, current, other := newBridgeRelayFixture(t, "relay-password")
		kept := f.open(t, current)
		revoked := f.open(t, other)
		f.service.CloseAccountUser(current.user.ID, current.id)
		revoked.assertRevoked(t)
		kept.assertFlows(t)
	})
}

// A revocation that never reaches the closers (here: expiry) is caught by the periodic recheck.
func TestBridgeRelayPeriodicCheckClosesExpiredSession(t *testing.T) {
	f, expiring, valid := newBridgeRelayFixture(t, "relay-expiry")
	// Each login session gets its own supervisor; tickers are created in registration order.
	tickers := make(chan chan time.Time, 2)
	f.service.bridges.ticker = func(time.Duration) (<-chan time.Time, func()) {
		ticks := make(chan time.Time)
		select {
		case tickers <- ticks:
		default:
			t.Error("unexpected extra session supervisor")
		}
		return ticks, func() {}
	}
	nextTicker := func() chan time.Time {
		t.Helper()
		select {
		case ticks := <-tickers:
			return ticks
		case <-time.After(30 * time.Second):
			t.Fatal("session supervisor not started")
			return nil
		}
	}
	// Every session check (the registration check and each recheck) reports its completion.
	checked := make(chan struct{}, 16)
	check := f.service.bridges.check
	f.service.bridges.check = func(ctx context.Context, token string) (*account.Identity, error) {
		identity, err := check(ctx, token)
		checked <- struct{}{}
		return identity, err
	}
	waitChecked := func() {
		t.Helper()
		select {
		case <-checked:
		case <-time.After(30 * time.Second):
			t.Fatal("session check did not complete")
		}
	}
	// tick delivers one recheck and waits until it finished.
	tick := func(ticks chan time.Time) {
		t.Helper()
		select {
		case ticks <- time.Now():
		case <-time.After(30 * time.Second):
			t.Fatal("session supervisor stopped receiving ticks")
		}
		waitChecked()
	}
	expired := f.open(t, expiring)
	waitChecked()
	expiredTicks := nextTicker()
	kept := f.open(t, valid)
	waitChecked()
	keptTicks := nextTicker()

	// A recheck while both sessions are valid keeps both relays.
	tick(expiredTicks)
	tick(keptTicks)
	expired.assertFlows(t)
	kept.assertFlows(t)

	userSession := query.Use(f.service.db).UserSession
	if _, err := userSession.WithContext(context.Background()).Where(userSession.ID.Eq(expiring.id)).
		UpdateColumn(userSession.ExpiresAt, time.Now().Add(-time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	tick(expiredTicks)
	tick(keptTicks)
	expired.assertRevoked(t)
	kept.assertFlows(t)
}

// A session revoked before the relay registers is refused before the upgrade.
func TestBridgeRelayRejectsSessionRevokedBeforeRegistration(t *testing.T) {
	f, session, _ := newBridgeRelayFixture(t, "relay-late")
	f.service.bridges.check = func(context.Context, string) (*account.Identity, error) {
		return nil, ipc.NewError(ipc.CodeForbidden, "会话已吊销，请重新登录")
	}
	if response := f.call(t, http.MethodGet, "/fleet/devices/"+f.deviceID+"/bridge", nil, session, ""); response.status != http.StatusUnauthorized {
		t.Fatalf("relay after revocation: HTTP %d %v", response.status, response.body)
	}
	f.service.bridges.mu.Lock()
	remaining := len(f.service.bridges.sessions)
	f.service.bridges.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("refused relay left %d tracked sessions", remaining)
	}
}

// Revoking a login device closes the relays of sessions bound to it and keeps the user's other relays.
func TestBridgeRelayClosesWithRevokedLoginDevice(t *testing.T) {
	f, _, unbound := newBridgeRelayFixture(t, "relay-device")
	device, err := f.accounts.RegisterDevice(context.Background(), unbound.user.ID, "laptop", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	token, session, err := f.accounts.IssueSession(context.Background(), unbound.user.ID, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	bound := &httpSession{id: session.ID, cookie: &http.Cookie{Name: sessionCookieName, Value: token}, user: unbound.user}
	revoked := f.open(t, bound)
	kept := f.open(t, unbound)

	f.service.CloseAccountDevice(unbound.user.ID, device.ID)
	revoked.assertRevoked(t)
	kept.assertFlows(t)
}
