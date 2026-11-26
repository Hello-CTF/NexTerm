package fleetserver

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/p2p/grant"
)

func TestP2PGrantHonorsThePerDeviceRelayChoice(t *testing.T) {
	relayed := map[string]bool{}
	var lookupErr error
	f := newP2PFixture(t, WithP2P(p2pTestSettings), WithP2PForceRelay(func(_ context.Context, userID, deviceID string) (bool, error) {
		return relayed[userID+"/"+deviceID], lookupErr
	}))
	owner := f.createUser(t, "p2p-relay-choice")
	device := f.startP2PDevice(t, owner, "p2p-relay-box", "terminal", agent.PurposeP2PSignal)
	session := f.session(t, owner)
	client := newClientKey(t)
	if call := f.requestGrant(t, session, device.id, client); call.status != http.StatusOK {
		t.Fatalf("grant: HTTP %d %v", call.status, call.body)
	}
	relayed[owner.ID+"/"+device.id] = true
	if call := f.requestGrant(t, session, device.id, client); call.status != http.StatusForbidden {
		t.Fatalf("grant with relay forced: HTTP %d %v", call.status, call.body)
	}
	relayed[owner.ID+"/"+device.id] = false
	lookupErr = errors.New("lookup failed")
	if call := f.requestGrant(t, session, device.id, client); call.status == http.StatusOK {
		t.Fatalf("grant with a failed relay lookup: HTTP %d %v", call.status, call.body)
	}
}

func TestP2PGrantRefusedForARevokedLoginSession(t *testing.T) {
	f := newP2PFixture(t, WithP2P(p2pTestSettings))
	owner := f.createUser(t, "p2p-logged-out")
	device := f.startP2PDevice(t, owner, "p2p-logged-out-box", "terminal", agent.PurposeP2PSignal)
	session := f.session(t, owner)
	identity, err := f.accounts.ValidateSession(context.Background(), session.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.accounts.RevokeSession(context.Background(), session.id); err != nil {
		t.Fatal(err)
	}
	// The identity was validated before the revocation, as for a request already past the session middleware.
	_, err = f.service.IssueP2PGrant(context.Background(), identity, device.id, grant.Key(newClientKey(t)), session.cookie.Value)
	if !errors.Is(err, errP2PSessionRevoked) {
		t.Fatalf("err = %v, want errP2PSessionRevoked", err)
	}
	f.service.p2p.grants.mu.Lock()
	live := len(f.service.p2p.grants.byID)
	f.service.p2p.grants.mu.Unlock()
	if live != 0 {
		t.Fatalf("%d grants registered for a revoked session", live)
	}
	if call := f.requestGrant(t, session, device.id, newClientKey(t)); call.status != http.StatusUnauthorized {
		t.Fatalf("grant over HTTP after logout: HTTP %d %v", call.status, call.body)
	}
}
