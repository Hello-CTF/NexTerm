package fleetserver

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/p2p/grant"
	"github.com/coder/websocket"
)

var p2pTestSettings = P2PSettings{Enabled: true, STUNListen: []string{DefaultSTUNListen}}

type p2pFixture struct {
	*httpFixture
	clock *atomic.Int64
}

func newP2PFixture(t *testing.T, options ...Option) *p2pFixture {
	t.Helper()
	clock := &atomic.Int64{}
	clock.Store(time.Now().UnixMilli())
	options = append([]Option{WithNow(clock.Load)}, options...)
	return &p2pFixture{httpFixture: newHTTPFixture(t, false, options...), clock: clock}
}

func (f *p2pFixture) advance(d time.Duration) { f.clock.Add(d.Milliseconds()) }

type p2pDevice struct {
	id      string
	secret  string
	key     ed25519.PrivateKey
	control *websocket.Conn
}

// startP2PDevice enrolls a device, connects its control channel with capabilities, and registers a device key.
func (f *p2pFixture) startP2PDevice(t *testing.T, owner *account.User, name string, capabilities ...string) *p2pDevice {
	t.Helper()
	deviceID, secret := enrollAgent(t, f.httpFixture, owner, name)
	device := &p2pDevice{id: deviceID, secret: secret}
	device.connect(t, f, capabilities...)
	_, device.key, _ = ed25519.GenerateKey(nil)
	if state := f.registerKey(t, device, device.key.Public().(ed25519.PublicKey)); state != agent.P2PKeyStateBound {
		t.Fatalf("first key registration state = %q", state)
	}
	return device
}

func (d *p2pDevice) connect(t *testing.T, f *p2pFixture, capabilities ...string) {
	t.Helper()
	control := dialDeviceWS(t, f.httpFixture)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: d.id, Secret: d.secret, Capabilities: capabilities,
	}); response.Type != "hello_ok" {
		t.Fatalf("hello = %+v", response)
	}
	d.control = control
}

func (f *p2pFixture) registerKey(t *testing.T, device *p2pDevice, public ed25519.PublicKey) string {
	t.Helper()
	call := f.call(t, "POST", agent.PathP2PKey, agent.P2PKeyRequest{DeviceID: device.id, Secret: device.secret, PublicKey: grant.Key(public).String()}, nil, "")
	if call.status != http.StatusOK {
		t.Fatalf("register key: HTTP %d %v", call.status, call.body)
	}
	state, _ := call.body["state"].(string)
	return state
}

func (f *p2pFixture) requestGrant(t *testing.T, session *httpSession, deviceID string, client ed25519.PublicKey) httpCall {
	t.Helper()
	return f.call(t, "POST", "/fleet/devices/"+deviceID+"/p2p/grant", map[string]string{"client_pub": grant.Key(client).String()}, session, session.csrf)
}

func (f *p2pFixture) trustedKeys(t *testing.T, session *httpSession) []grant.PublicKey {
	t.Helper()
	call := f.call(t, "GET", "/fleet/p2p/config", nil, session, "")
	if call.status != http.StatusOK {
		t.Fatalf("config: HTTP %d %v", call.status, call.body)
	}
	encoded, _ := json.Marshal(call.body["grant_keys"])
	var keys []grant.PublicKey
	if err := json.Unmarshal(encoded, &keys); err != nil {
		t.Fatal(err)
	}
	return keys
}

func newClientKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	public, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return public
}

func readControlFrame(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatal(err)
	}
	return frame
}

func p2pAudits(t *testing.T, f *httpFixture, kind, action string) []map[string]any {
	t.Helper()
	var matched []map[string]any
	for _, row := range auditPayloadRows(t, f.service.db, auditSourceFleet, kind) {
		if row["action"] == action {
			matched = append(matched, row)
		}
	}
	return matched
}

func TestP2PGrantVerifiesForTheAgentAndKeepsItsIDAcrossRenewals(t *testing.T) {
	f := newP2PFixture(t, WithP2P(p2pTestSettings))
	owner := f.createUser(t, "p2p-owner")
	device := f.startP2PDevice(t, owner, "p2p-box", "terminal", "file", "forward", agent.PurposeP2PSignal)
	session := f.session(t, owner)
	client := newClientKey(t)

	first := f.requestGrant(t, session, device.id, client)
	if first.status != http.StatusOK {
		t.Fatalf("grant: HTTP %d %v", first.status, first.body)
	}
	token, _ := first.body["grant"].(string)
	agentPub := device.key.Public().(ed25519.PublicKey)
	claims, err := grant.VerifyAgent(token, f.trustedKeys(t, session), time.UnixMilli(f.clock.Load()), device.id, agentPub, client, "terminal")
	if err != nil {
		t.Fatal(err)
	}
	if claims.OwnerID != owner.ID || claims.GrantID != first.body["grant_id"] || !slices.Equal(claims.Scope, []string{"terminal", "file", "forward"}) {
		t.Fatalf("claims = %+v", claims)
	}
	if claims.ExpiresAt-claims.IssuedAt != grant.DefaultTTL.Milliseconds() || first.body["agent_pub"] != grant.Key(agentPub).String() {
		t.Fatalf("grant view = %v", first.body)
	}
	if _, err := grant.Verify(token, f.trustedKeys(t, session), time.UnixMilli(f.clock.Load()), device.id, client, "listen"); !errors.Is(err, grant.ErrScope) {
		t.Fatalf("unadvertised purpose err = %v", err)
	}

	f.advance(2 * time.Minute)
	renewed := f.requestGrant(t, session, device.id, client)
	if renewed.status != http.StatusOK || renewed.body["grant_id"] != first.body["grant_id"] || renewed.body["grant"] == token {
		t.Fatalf("renewal: HTTP %d %v", renewed.status, renewed.body)
	}
	if opens := p2pAudits(t, f.httpFixture, "device_terminal", auditActionP2POpen); len(opens) != 1 || opens[0]["grant_id"] != first.body["grant_id"] || opens[0]["outcome"] != "allow" {
		t.Fatalf("p2p_open audits = %v", opens)
	}

	// Another client key of the same session is another direct session.
	other := f.requestGrant(t, session, device.id, newClientKey(t))
	if other.status != http.StatusOK || other.body["grant_id"] == first.body["grant_id"] {
		t.Fatalf("second client: HTTP %d %v", other.status, other.body)
	}
	// A renewal after the grant and its grace lapsed starts a new session.
	f.advance(grant.DefaultTTL + p2pGrantGrace + time.Second)
	if lapsed := f.requestGrant(t, session, device.id, client); lapsed.status != http.StatusOK || lapsed.body["grant_id"] == first.body["grant_id"] {
		t.Fatalf("lapsed renewal: HTTP %d %v", lapsed.status, lapsed.body)
	}
}

func TestP2PGrantPolicy(t *testing.T) {
	client := newClientKey(t)
	t.Run("server switch off", func(t *testing.T) {
		f := newP2PFixture(t)
		owner := f.createUser(t, "p2p-off")
		device := f.startP2PDevice(t, owner, "p2p-off-box", "terminal", agent.PurposeP2PSignal)
		session := f.session(t, owner)
		if call := f.requestGrant(t, session, device.id, client); call.status != http.StatusForbidden {
			t.Fatalf("grant with P2P off: HTTP %d %v", call.status, call.body)
		}
		config := f.call(t, "GET", "/fleet/p2p/config", nil, session, "")
		if config.status != http.StatusOK || config.body["enabled"] != false || len(config.body["grant_keys"].([]any)) != 0 || len(config.body["stun"].([]any)) != 0 {
			t.Fatalf("config with P2P off: %v", config.body)
		}
		if denials := p2pAudits(t, f.httpFixture, "device_terminal", auditActionP2POpen); len(denials) != 1 || denials[0]["reason"] != "p2p_disabled" {
			t.Fatalf("denial audits = %v", denials)
		}
	})

	f := newP2PFixture(t, WithP2P(p2pTestSettings))
	owner := f.createUser(t, "p2p-policy")
	stranger := f.createUser(t, "p2p-stranger")
	recipient := f.createUser(t, "p2p-recipient")
	device := f.startP2PDevice(t, owner, "p2p-policy-box", "terminal", agent.PurposeP2PSignal)
	ownerSession := f.session(t, owner)

	// A host share never extends to direct connections: the recipient is not the owner.
	share := f.call(t, "POST", "/share/host-shares", map[string]any{
		"device_id": device.id, "recipient_username": "p2p-recipient", "write": true, "ttl_ms": 3600000,
	}, ownerSession, ownerSession.csrf)
	if share.status != http.StatusOK {
		t.Fatalf("create host share: HTTP %d %v", share.status, share.body)
	}
	for name, session := range map[string]*httpSession{"stranger": f.session(t, stranger), "share recipient": f.session(t, recipient)} {
		if call := f.requestGrant(t, session, device.id, client); call.status != http.StatusForbidden {
			t.Fatalf("%s grant: HTTP %d %v", name, call.status, call.body)
		}
	}
	if call := f.call(t, "POST", "/fleet/devices/"+device.id+"/p2p/grant", map[string]string{"client_pub": grant.Key(client).String()}, ownerSession, ""); call.status != http.StatusForbidden {
		t.Fatalf("grant without CSRF: HTTP %d %v", call.status, call.body)
	}
	if call := f.call(t, "POST", "/fleet/devices/"+device.id+"/p2p/grant", map[string]string{"client_pub": "short"}, ownerSession, ownerSession.csrf); call.status != http.StatusBadRequest {
		t.Fatalf("grant with a bad key: HTTP %d %v", call.status, call.body)
	}
	if call := f.call(t, "POST", "/fleet/devices/"+device.id+"/p2p/grant", map[string]string{"client_pub": grant.Key(client).String()}, nil, ""); call.status != http.StatusUnauthorized {
		t.Fatalf("anonymous grant: HTTP %d %v", call.status, call.body)
	}

	if err := f.service.SetTerminalEnabled(context.Background(), &account.Identity{UserID: owner.ID}, device.id, false); err != nil {
		t.Fatal(err)
	}
	if call := f.requestGrant(t, ownerSession, device.id, client); call.status != http.StatusForbidden {
		t.Fatalf("grant with terminal access off: HTTP %d %v", call.status, call.body)
	}
	if err := f.service.SetTerminalEnabled(context.Background(), &account.Identity{UserID: owner.ID}, device.id, true); err != nil {
		t.Fatal(err)
	}

	// An agent without the capability (an old agent) gets no grant, and neither does an offline one.
	device.control.CloseNow()
	waitOffline(t, f, device.id)
	if call := f.requestGrant(t, ownerSession, device.id, client); call.status != http.StatusServiceUnavailable {
		t.Fatalf("offline grant: HTTP %d %v", call.status, call.body)
	}
	device.connect(t, f, "terminal")
	if call := f.requestGrant(t, ownerSession, device.id, client); call.status != http.StatusForbidden {
		t.Fatalf("grant without capability: HTTP %d %v", call.status, call.body)
	}

	// No registered key: no grant.
	bare := &p2pDevice{}
	bare.id, bare.secret = enrollAgent(t, f.httpFixture, owner, "p2p-keyless")
	bare.connect(t, f, "terminal", agent.PurposeP2PSignal)
	if call := f.requestGrant(t, ownerSession, bare.id, client); call.status != http.StatusForbidden {
		t.Fatalf("grant without a device key: HTTP %d %v", call.status, call.body)
	}
	reasons := map[string]bool{}
	for _, row := range p2pAudits(t, f.httpFixture, "device_terminal", auditActionP2POpen) {
		reasons[row["reason"].(string)] = true
	}
	for _, want := range []string{"not_owner", "terminal_disabled", "offline", "capability_missing", "device_key_missing"} {
		if !reasons[want] {
			t.Fatalf("no %s denial audit in %v", want, reasons)
		}
	}
}

func waitOffline(t *testing.T, f *p2pFixture, deviceID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for f.service.registry.AgentOnline(deviceID) {
		if time.Now().After(deadline) {
			t.Fatal("device stayed online")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestP2PDeviceKeyTrustOnFirstUseAndOwnerConfirmation(t *testing.T) {
	f := newP2PFixture(t, WithP2P(p2pTestSettings))
	owner := f.createUser(t, "p2p-tofu")
	stranger := f.createUser(t, "p2p-tofu-stranger")
	device := f.startP2PDevice(t, owner, "p2p-tofu-box", "terminal", agent.PurposeP2PSignal)
	session := f.session(t, owner)
	original := device.key.Public().(ed25519.PublicKey)
	client := newClientKey(t)

	if state := f.registerKey(t, device, original); state != agent.P2PKeyStateBound {
		t.Fatalf("same key again = %q", state)
	}
	live := f.requestGrant(t, session, device.id, client)
	if live.status != http.StatusOK {
		t.Fatalf("grant: HTTP %d %v", live.status, live.body)
	}

	replacement := newClientKey(t)
	if state := f.registerKey(t, device, replacement); state != agent.P2PKeyStatePending {
		t.Fatalf("changed key = %q", state)
	}
	f.service.p2p.grants.pushes.Wait()
	if frame := readControlFrame(t, device.control); frame["type"] != "p2p_revoke" || frame["grant_id"] != live.body["grant_id"] || frame["reason"] != p2pRevokeKey {
		t.Fatalf("frame after key change = %v", frame)
	}
	if call := f.requestGrant(t, session, device.id, client); call.status != http.StatusForbidden {
		t.Fatalf("grant with a pending key: HTTP %d %v", call.status, call.body)
	}
	list := f.call(t, "GET", "/fleet/devices", nil, session, "")
	p2pKey := list.body["devices"].([]any)[0].(map[string]any)["agent"].(map[string]any)["p2p_key"].(map[string]any)
	if p2pKey["state"] != agent.P2PKeyStatePending || p2pKey["public_key"] != grant.Key(original).String() || p2pKey["pending_key"] != grant.Key(replacement).String() {
		t.Fatalf("device list key = %v", p2pKey)
	}

	confirm := func(session *httpSession, key ed25519.PublicKey) httpCall {
		return f.call(t, "POST", "/fleet/devices/"+device.id+"/p2p/key/confirm", map[string]string{"public_key": grant.Key(key).String()}, session, session.csrf)
	}
	if call := confirm(f.session(t, stranger), replacement); call.status != http.StatusForbidden {
		t.Fatalf("stranger confirm: HTTP %d %v", call.status, call.body)
	}
	if call := confirm(session, newClientKey(t)); call.status != http.StatusBadRequest {
		t.Fatalf("confirm of another key: HTTP %d %v", call.status, call.body)
	}
	if call := confirm(session, replacement); call.status != http.StatusOK {
		t.Fatalf("confirm: HTTP %d %v", call.status, call.body)
	}
	confirmed := f.requestGrant(t, session, device.id, client)
	if confirmed.status != http.StatusOK || confirmed.body["agent_pub"] != grant.Key(replacement).String() {
		t.Fatalf("grant after confirm: HTTP %d %v", confirmed.status, confirmed.body)
	}

	// The superseded key is not trusted again, but presenting the trusted key withdraws an unconfirmed change.
	if state := f.registerKey(t, device, original); state != agent.P2PKeyStatePending {
		t.Fatalf("superseded key = %q", state)
	}
	if state := f.registerKey(t, device, replacement); state != agent.P2PKeyStateBound {
		t.Fatalf("trusted key after a withdrawn change = %q", state)
	}
	actions := map[string]int{}
	for _, row := range auditPayloadRows(t, f.service.db, auditSourceFleet, auditKindDeviceP2PKey) {
		actions[row["action"].(string)]++
	}
	if actions["p2p_key_bind"] != 1 || actions["p2p_key_change"] != 2 || actions["p2p_key_restore"] != 1 || actions["p2p_key_confirm"] < 2 {
		t.Fatalf("key audits = %v", actions)
	}
}

func TestP2PRevokeReachesOnlyAgentsThatAdvertiseTheCapability(t *testing.T) {
	f := newP2PFixture(t, WithP2P(p2pTestSettings))
	owner := f.createUser(t, "p2p-revoke")
	device := f.startP2PDevice(t, owner, "p2p-revoke-box", "terminal", agent.PurposeP2PSignal)
	session := f.session(t, owner)
	identity := &account.Identity{UserID: owner.ID}

	logout := f.requestGrant(t, session, device.id, newClientKey(t))
	if logout.status != http.StatusOK {
		t.Fatalf("grant: HTTP %d %v", logout.status, logout.body)
	}
	if err := f.service.CloseAccountSession(session.id); err != nil {
		t.Fatal(err)
	}
	f.service.p2p.grants.pushes.Wait()
	if frame := readControlFrame(t, device.control); frame["type"] != "p2p_revoke" || frame["grant_id"] != logout.body["grant_id"] || frame["reason"] != p2pRevokeSession {
		t.Fatalf("frame after logout = %v", frame)
	}

	session = f.session(t, owner)
	disabled := f.requestGrant(t, session, device.id, newClientKey(t))
	if disabled.status != http.StatusOK {
		t.Fatalf("grant: HTTP %d %v", disabled.status, disabled.body)
	}
	if err := f.service.SetTerminalEnabled(context.Background(), identity, device.id, false); err != nil {
		t.Fatal(err)
	}
	f.service.p2p.grants.pushes.Wait()
	frames := []map[string]any{readControlFrame(t, device.control), readControlFrame(t, device.control)}
	revoked := slices.IndexFunc(frames, func(frame map[string]any) bool { return frame["type"] == "p2p_revoke" })
	if revoked < 0 || frames[revoked]["grant_id"] != disabled.body["grant_id"] || frames[revoked]["reason"] != p2pRevokeTerminal || frames[1-revoked]["type"] != "config" {
		t.Fatalf("frames after disabling terminal access = %v", frames)
	}
	if err := f.service.SetTerminalEnabled(context.Background(), identity, device.id, true); err != nil {
		t.Fatal(err)
	}
	if frame := readControlFrame(t, device.control); frame["type"] != "config" {
		t.Fatalf("frame = %v", frame)
	}

	// The device reconnects as an agent without the capability: its live grant is revoked without a frame, which
	// such an agent would treat as a fatal unknown control message.
	live := f.requestGrant(t, session, device.id, newClientKey(t))
	if live.status != http.StatusOK {
		t.Fatalf("grant: HTTP %d %v", live.status, live.body)
	}
	device.connect(t, f, "terminal")
	if err := f.service.CloseAccountSession(session.id); err != nil {
		t.Fatal(err)
	}
	f.service.p2p.grants.pushes.Wait()
	if err := f.service.SetTerminalEnabled(context.Background(), identity, device.id, false); err != nil {
		t.Fatal(err)
	}
	if frame := readControlFrame(t, device.control); frame["type"] != "config" {
		t.Fatalf("an agent without the capability received %v", frame)
	}
	if f.service.registry.pushP2PRevoke(device.id, "g-any", "test") {
		t.Fatal("pushP2PRevoke wrote to an agent without the capability")
	}
}

func TestP2PRevokeOnLogoutOfOtherSessions(t *testing.T) {
	f := newP2PFixture(t, WithP2P(p2pTestSettings))
	owner := f.createUser(t, "p2p-logout-all")
	device := f.startP2PDevice(t, owner, "p2p-logout-box", "terminal", agent.PurposeP2PSignal)
	kept, dropped := f.session(t, owner), f.session(t, owner)
	keptGrant := f.requestGrant(t, kept, device.id, newClientKey(t))
	droppedGrant := f.requestGrant(t, dropped, device.id, newClientKey(t))
	if keptGrant.status != http.StatusOK || droppedGrant.status != http.StatusOK {
		t.Fatalf("grants: %v %v", keptGrant.body, droppedGrant.body)
	}
	f.service.CloseAccountUser(owner.ID, kept.id)
	f.service.p2p.grants.pushes.Wait()
	if frame := readControlFrame(t, device.control); frame["grant_id"] != droppedGrant.body["grant_id"] {
		t.Fatalf("frame = %v", frame)
	}
	f.service.p2p.grants.mu.Lock()
	_, keptLive := f.service.p2p.grants.byID[keptGrant.body["grant_id"].(string)]
	_, droppedLive := f.service.p2p.grants.byID[droppedGrant.body["grant_id"].(string)]
	f.service.p2p.grants.mu.Unlock()
	if !keptLive || droppedLive {
		t.Fatalf("live grants after logout-all: kept=%v dropped=%v", keptLive, droppedLive)
	}
}

func TestP2PConfigEndpoints(t *testing.T) {
	settings := p2pTestSettings
	settings.STUNExternal = []agent.P2PEndpoint{{Host: "stun.example.net", Port: 3478}}
	f := newP2PFixture(t, WithP2P(settings))
	f.service.SetP2PSTUNPorts([]int{3478, 5349})
	owner := f.createUser(t, "p2p-config")
	device := f.startP2PDevice(t, owner, "p2p-config-box", "terminal", agent.PurposeP2PSignal)
	session := f.session(t, owner)

	get := func(path, host string, header http.Header) (int, agent.P2PConfig) {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, f.http.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = host
		for key, values := range header {
			request.Header[key] = values
		}
		response, err := f.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var config agent.P2PConfig
		if response.StatusCode == http.StatusOK {
			if err := json.NewDecoder(response.Body).Decode(&config); err != nil {
				t.Fatal(err)
			}
		}
		return response.StatusCode, config
	}
	sessionHeader := http.Header{"Cookie": []string{session.cookie.Name + "=" + session.cookie.Value}}
	status, config := get("/fleet/p2p/config", "nx.example.com:8443", sessionHeader)
	want := []agent.P2PEndpoint{{Host: "nx.example.com", Port: 3478}, {Host: "nx.example.com", Port: 5349}, {Host: "stun.example.net", Port: 3478}}
	if status != http.StatusOK || !config.Enabled || !slices.Equal(config.STUN, want) || config.TURN == nil || len(config.TURN) != 0 || len(config.GrantKeys) != 1 || config.Timeouts.GatherMS != 3000 || config.Timeouts.CheckMS != 8000 || config.KeyState != "" {
		t.Fatalf("session config: HTTP %d %+v", status, config)
	}
	if status, config := get("/fleet/p2p/config", "[2001:db8::1]:443", sessionHeader); status != http.StatusOK || config.STUN[0] != (agent.P2PEndpoint{Host: "2001:db8::1", Port: 3478}) {
		t.Fatalf("IPv6 host config: HTTP %d %+v", status, config)
	}
	if status, _ := get("/fleet/p2p/config", "nx.example.com", nil); status != http.StatusUnauthorized {
		t.Fatalf("anonymous config: HTTP %d", status)
	}

	agentHeader := http.Header{agent.HeaderDeviceID: []string{device.id}, "Authorization": []string{"Bearer " + device.secret}}
	status, config = get(agent.PathP2PConfig, "agent.example.com", agentHeader)
	if status != http.StatusOK || config.KeyState != agent.P2PKeyStateBound || config.STUN[0].Host != "agent.example.com" || len(config.GrantKeys) != 1 {
		t.Fatalf("agent config: HTTP %d %+v", status, config)
	}
	for name, header := range map[string]http.Header{
		"wrong secret": {agent.HeaderDeviceID: []string{device.id}, "Authorization": []string{"Bearer wrong"}},
		"no device":    {"Authorization": []string{"Bearer " + device.secret}},
		"no secret":    {agent.HeaderDeviceID: []string{device.id}},
		"session only": sessionHeader,
	} {
		if status, _ := get(agent.PathP2PConfig, "agent.example.com", header); status != http.StatusForbidden {
			t.Fatalf("%s: HTTP %d", name, status)
		}
	}

	f.service.p2p.mu.Lock()
	f.service.p2p.settings.STUNAdvertise = []agent.P2PEndpoint{{Host: "203.0.113.7", Port: 3478}}
	f.service.p2p.mu.Unlock()
	if _, config := get("/fleet/p2p/config", "nx.example.com", sessionHeader); !slices.Equal(config.STUN, []agent.P2PEndpoint{{Host: "203.0.113.7", Port: 3478}, {Host: "stun.example.net", Port: 3478}}) {
		t.Fatalf("advertised config = %+v", config.STUN)
	}
}

func TestP2PSigningKeyRotation(t *testing.T) {
	f := newP2PFixture(t, WithP2P(p2pTestSettings))
	owner := f.createUser(t, "p2p-rotate")
	session := f.session(t, owner)
	first := f.trustedKeys(t, session)
	if len(first) != 1 || first[0].Alg != grant.Alg {
		t.Fatalf("keys = %+v", first)
	}
	f.advance(p2pKeyRotation)
	rotated := f.trustedKeys(t, session)
	if len(rotated) != 2 || rotated[1].KID != first[0].KID || rotated[0].KID == first[0].KID {
		t.Fatalf("keys after rotation = %+v", rotated)
	}
	signing, err := f.service.p2p.keys.signing(context.Background(), f.service)
	if err != nil || signing.KID != rotated[0].KID {
		t.Fatalf("signing key = %v, %v", signing.KID, err)
	}
	f.advance(p2pKeyOverlap + p2pKeyCacheTTL + time.Second)
	if after := f.trustedKeys(t, session); len(after) != 1 || after[0].KID != rotated[0].KID {
		t.Fatalf("keys after the overlap = %+v", after)
	}
	all, err := f.service.p2p.keys.all(context.Background(), f.service)
	if err != nil || len(all) != 2 {
		t.Fatalf("retained keys = %d, %v", len(all), err)
	}
}

func TestP2PSignalPurposeUsesTheRelayChecks(t *testing.T) {
	f := newP2PFixture(t, WithP2P(p2pTestSettings))
	owner := f.createUser(t, "p2p-signal")
	stranger := f.createUser(t, "p2p-signal-stranger")
	device := f.startP2PDevice(t, owner, "p2p-signal-box", "terminal")
	session := f.session(t, owner)
	path := "/fleet/devices/" + device.id + "/bridge?purpose=" + agent.PurposeP2PSignal
	if call := f.call(t, "GET", path, nil, session, ""); call.status != http.StatusForbidden || !strings.Contains(call.body["error"].(map[string]any)["message"].(string), agent.PurposeP2PSignal) {
		t.Fatalf("signal relay to an agent without the capability: HTTP %d %v", call.status, call.body)
	}
	device.connect(t, f, "terminal", agent.PurposeP2PSignal)
	if call := f.call(t, "GET", path, nil, f.session(t, stranger), ""); call.status != http.StatusForbidden {
		t.Fatalf("stranger signal relay: HTTP %d %v", call.status, call.body)
	}
	relay := dialRelay(t, f.httpFixture, device.id, session.cookie, agent.PurposeP2PSignal)
	defer relay.CloseNow()
	if frame := readControlFrame(t, device.control); frame["type"] != "bridge" || frame["purpose"] != agent.PurposeP2PSignal {
		t.Fatalf("bridge request = %v", frame)
	}
	if opens := p2pAudits(t, f.httpFixture, "device_terminal", "p2p_signal_open"); len(opens) == 0 || opens[len(opens)-1]["outcome"] != "allow" {
		t.Fatalf("signal audits = %v", opens)
	}
}

func TestP2PAuditUpload(t *testing.T) {
	f := newP2PFixture(t, WithP2P(p2pTestSettings))
	owner := f.createUser(t, "p2p-audit")
	device := f.startP2PDevice(t, owner, "p2p-audit-box", "terminal", "file", agent.PurposeP2PSignal)
	other := f.startP2PDevice(t, owner, "p2p-audit-other", "terminal", agent.PurposeP2PSignal)
	session := f.session(t, owner)
	issued := f.requestGrant(t, session, device.id, newClientKey(t))
	foreign := f.requestGrant(t, session, other.id, newClientKey(t))
	if issued.status != http.StatusOK || foreign.status != http.StatusOK {
		t.Fatalf("grants: %v %v", issued.body, foreign.body)
	}
	grantID := issued.body["grant_id"].(string)
	issuedAt := int64(issued.body["issued_at"].(float64))
	expiresAt := int64(issued.body["expires_at"].(float64))
	f.advance(time.Minute)

	records := []agent.P2PAuditRecord{
		{GrantID: grantID, Purpose: "terminal", StartedAt: issuedAt + 10, EndedAt: issuedAt + 5000, BytesIn: 10, BytesOut: 20},
		{GrantID: grantID, Purpose: "forward", StartedAt: issuedAt + 10, EndedAt: issuedAt + 20, Outcome: "deny", Reason: "out_of_scope"},
		{GrantID: grantID, Purpose: "forward", StartedAt: issuedAt + 10, EndedAt: issuedAt + 20},
		{GrantID: foreign.body["grant_id"].(string), Purpose: "terminal", StartedAt: issuedAt + 10, EndedAt: issuedAt + 20},
		{GrantID: "g-made-up", Purpose: "terminal", StartedAt: issuedAt + 10, EndedAt: issuedAt + 20},
		{GrantID: grantID, Purpose: "terminal", StartedAt: issuedAt + 10, EndedAt: issuedAt},
		{GrantID: grantID, Purpose: "terminal", StartedAt: expiresAt + p2pGrantGrace.Milliseconds() + 1, EndedAt: expiresAt + p2pGrantGrace.Milliseconds() + 2},
		{GrantID: grantID, Purpose: "bogus", StartedAt: issuedAt + 10, EndedAt: issuedAt + 20},
		{GrantID: grantID, Purpose: "file", StartedAt: issuedAt + 10, EndedAt: issuedAt + 20, BytesIn: -1},
		{GrantID: grantID, Purpose: "file", StartedAt: issuedAt + 10, EndedAt: issuedAt + 20, Outcome: "maybe"},
	}
	request := agent.P2PAuditRequest{
		DeviceID: device.id, Secret: device.secret,
		Grants:  []string{issued.body["grant"].(string), foreign.body["grant"].(string), "v1.garbage.sig"},
		Records: records,
	}
	call := f.call(t, "POST", agent.PathP2PAudit, request, nil, "")
	if call.status != http.StatusOK || call.body["accepted"] != float64(2) {
		t.Fatalf("audit upload: HTTP %d %v", call.status, call.body)
	}
	wantRejected := map[float64]string{2: "out_of_scope", 3: "unknown_grant", 4: "unknown_grant", 5: "bad_time", 6: "outside_grant", 7: "unknown_purpose", 8: "bad_bytes", 9: "bad_outcome"}
	rejected := call.body["rejected"].([]any)
	if len(rejected) != len(wantRejected) {
		t.Fatalf("rejected = %v", rejected)
	}
	for _, entry := range rejected {
		item := entry.(map[string]any)
		if wantRejected[item["index"].(float64)] != item["reason"] {
			t.Fatalf("rejected = %v", rejected)
		}
	}
	stored := p2pAudits(t, f.httpFixture, "device_terminal", "terminal_open")
	var direct []map[string]any
	for _, row := range stored {
		if row["path"] == pathDirect {
			direct = append(direct, row)
		}
	}
	if len(direct) != 1 || direct[0]["grant_id"] != grantID || direct[0]["requester"] != owner.ID || direct[0]["device_id"] != device.id || direct[0]["bytes_out"] != float64(20) || direct[0]["outcome"] != "allow" {
		t.Fatalf("direct terminal audits = %v", direct)
	}
	if denied := p2pAudits(t, f.httpFixture, "device_terminal", "forward_open"); len(denied) != 1 || denied[0]["outcome"] != "deny" || denied[0]["path"] != pathDirect {
		t.Fatalf("direct forward audits = %v", denied)
	}

	// The device credential decides the device: another device cannot report under this grant.
	borrowed := request
	borrowed.DeviceID, borrowed.Secret = other.id, other.secret
	borrowed.Grants = []string{issued.body["grant"].(string)}
	borrowed.Records = records[:1]
	if call := f.call(t, "POST", agent.PathP2PAudit, borrowed, nil, ""); call.status != http.StatusOK || call.body["accepted"] != float64(0) {
		t.Fatalf("borrowed grant: HTTP %d %v", call.status, call.body)
	}
	if call := f.call(t, "POST", agent.PathP2PAudit, agent.P2PAuditRequest{DeviceID: device.id, Secret: "wrong"}, nil, ""); call.status != http.StatusForbidden {
		t.Fatalf("bad credential: HTTP %d %v", call.status, call.body)
	}
	tooMany := agent.P2PAuditRequest{DeviceID: device.id, Secret: device.secret, Records: make([]agent.P2PAuditRecord, maxP2PAuditRecords+1)}
	if call := f.call(t, "POST", agent.PathP2PAudit, tooMany, nil, ""); call.status != http.StatusBadRequest {
		t.Fatalf("oversized batch: HTTP %d %v", call.status, call.body)
	}

	// Uploads are rate limited per device; the bucket refills with time.
	limited := agent.P2PAuditRequest{DeviceID: device.id, Secret: device.secret}
	statuses := []int{}
	for range p2pAuditBurst + 1 {
		statuses = append(statuses, f.call(t, "POST", agent.PathP2PAudit, limited, nil, "").status)
	}
	if statuses[len(statuses)-1] != http.StatusTooManyRequests {
		t.Fatalf("statuses = %v", statuses)
	}
	f.advance(10 * time.Second)
	if call := f.call(t, "POST", agent.PathP2PAudit, limited, nil, ""); call.status != http.StatusOK {
		t.Fatalf("after refill: HTTP %d %v", call.status, call.body)
	}
}

func TestParseP2PEnv(t *testing.T) {
	lookup := func(values map[string]string) func(string) (string, bool) {
		return func(key string) (string, bool) {
			value, ok := values[key]
			return value, ok
		}
	}
	defaults, err := ParseP2PEnv(lookup(nil))
	if err != nil || !defaults.Enabled || !slices.Equal(defaults.STUNListen, []string{":3478"}) || defaults.STUNAdvertise != nil || defaults.STUNExternal != nil {
		t.Fatalf("defaults = %+v, %v", defaults, err)
	}
	disabled, err := ParseP2PEnv(lookup(map[string]string{EnvP2P: "0", EnvSTUNListen: ""}))
	if err != nil || disabled.Enabled || len(disabled.STUNListen) != 0 {
		t.Fatalf("disabled = %+v, %v", disabled, err)
	}
	custom, err := ParseP2PEnv(lookup(map[string]string{
		EnvSTUNListen:    ":3478, 0.0.0.0:5349,[::]:3479,:3478",
		EnvSTUNAdvertise: "nx.example.com:3478,[2001:db8::2]:5349",
		EnvSTUNExternal:  "stun.example.net, 198.51.100.4:19302, 2001:db8::9, [2001:db8::a]",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(custom.STUNListen, []string{":3478", "0.0.0.0:5349", "[::]:3479"}) {
		t.Fatalf("listen = %v", custom.STUNListen)
	}
	if !slices.Equal(custom.STUNAdvertise, []agent.P2PEndpoint{{Host: "nx.example.com", Port: 3478}, {Host: "2001:db8::2", Port: 5349}}) {
		t.Fatalf("advertise = %v", custom.STUNAdvertise)
	}
	if !slices.Equal(custom.STUNExternal, []agent.P2PEndpoint{{Host: "stun.example.net", Port: 3478}, {Host: "198.51.100.4", Port: 19302}, {Host: "2001:db8::9", Port: 3478}, {Host: "2001:db8::a", Port: 3478}}) {
		t.Fatalf("external = %v", custom.STUNExternal)
	}
	for name, values := range map[string]map[string]string{
		"bad switch":          {EnvP2P: "maybe"},
		"listen without port": {EnvSTUNListen: "0.0.0.0"},
		"listen hostname":     {EnvSTUNListen: "example.com:3478"},
		"listen port zero":    {EnvSTUNListen: ":0"},
		"advertise no port":   {EnvSTUNAdvertise: "nx.example.com"},
		"advertise bad host":  {EnvSTUNAdvertise: "nx example:3478"},
		"external bad port":   {EnvSTUNExternal: "stun.example.net:99999"},
	} {
		if _, err := ParseP2PEnv(lookup(values)); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

// The agent's endpoint client speaks the agent routes as the server serves them.
func TestP2PAgentClientAgainstTheServer(t *testing.T) {
	f := newP2PFixture(t, WithP2P(p2pTestSettings))
	owner := f.createUser(t, "p2p-client")
	deviceID, secret := enrollAgent(t, f.httpFixture, owner, "p2p-client-box")
	client, err := agent.NewEndpointClient(agent.BaseURLEntry{URL: f.http.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key, err := agent.LoadOrCreateP2PKey(agent.P2PKeyPath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	registered, err := client.RegisterP2PKey(ctx, deviceID, secret, key.Public().(ed25519.PublicKey))
	if err != nil || registered.State != agent.P2PKeyStateBound {
		t.Fatalf("register = %+v, %v", registered, err)
	}
	config, err := client.P2PConfig(ctx, deviceID, secret)
	if err != nil || !config.Enabled || config.KeyState != agent.P2PKeyStateBound || len(config.GrantKeys) != 1 {
		t.Fatalf("config = %+v, %v", config, err)
	}
	uploaded, err := client.UploadP2PAudit(ctx, agent.P2PAuditRequest{DeviceID: deviceID, Secret: secret,
		Records: []agent.P2PAuditRecord{{GrantID: "g-unknown", Purpose: "terminal", StartedAt: 1, EndedAt: 2}}})
	if err != nil || uploaded.Accepted != 0 || len(uploaded.Rejected) != 1 || uploaded.Rejected[0].Reason != "unknown_grant" {
		t.Fatalf("upload = %+v, %v", uploaded, err)
	}
	if _, err := client.P2PConfig(ctx, deviceID, "wrong"); err == nil {
		t.Fatal("config with a wrong secret succeeded")
	}
}
