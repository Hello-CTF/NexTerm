//go:build unix

package fleetserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The fleet's real device revocation entry shares the account async-boundary
// mutex and bumps the device owner's epoch, so pending generation-bound flows
// (TOTP bindings here) fail closed exactly like an account-side revocation.
func TestFleetRevokeDeviceBumpsEpoch(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "device-epoch-owner")
	session := f.session(t, owner)
	deviceID, _ := enrollAgent(t, f, owner, "device-epoch-box")

	epoch := f.accounts.Epoch(owner.ID)
	secret, _, err := f.accounts.BeginTOTPSetup(context.Background(), owner.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	// The fleet HTTP revocation bumps the device owner's epoch.
	call := f.call(t, http.MethodPost, "/fleet/devices/"+deviceID+"/revoke", map[string]any{}, session, session.csrf)
	if call.status != http.StatusOK {
		t.Fatalf("fleet device revoke status = %d body = %v", call.status, call.body)
	}
	if next := f.accounts.Epoch(owner.ID); next <= epoch {
		t.Fatalf("fleet device revoke did not bump the owner epoch: %d -> %d", epoch, next)
	}

	// The pending TOTP binding from the old generation now fails closed.
	if _, err := f.accounts.ConfirmTOTPSetup(context.Background(), owner.ID, totpCodeForTest(t, secret, time.Now().Unix())); err == nil || !strings.Contains(err.Error(), "绑定已失效") {
		t.Fatalf("stale-generation TOTP confirm = %v, want binding-expired", err)
	}
	if enabled, err := f.accounts.TOTPEnabled(context.Background(), owner.ID); err != nil || enabled {
		t.Fatalf("stale-generation confirm changed TOTP state: enabled=%v err=%v", enabled, err)
	}

	// A fresh binding at the new generation confirms normally.
	secret2, _, err := f.accounts.BeginTOTPSetup(context.Background(), owner.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.accounts.ConfirmTOTPSetup(context.Background(), owner.ID, totpCodeForTest(t, secret2, time.Now().Unix())); err != nil {
		t.Fatalf("fresh-generation TOTP confirm = %v", err)
	}
}
