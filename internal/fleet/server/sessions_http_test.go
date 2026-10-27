package fleetserver

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionListAnswersTheCallersDevicesWithACoalescedAudit(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC).UnixMilli())
	fixture := newHTTPFixture(t, false, WithNow(now.Load))
	alice := fixture.createUser(t, "list-alice")
	var mu sync.Mutex
	var asked []string
	var waits []time.Duration
	fixture.service.SetSessionLister(func(_ context.Context, userID string, wait time.Duration) (any, int, error) {
		mu.Lock()
		asked = append(asked, userID)
		waits = append(waits, wait)
		mu.Unlock()
		return map[string]any{"hosts": []any{}, "sessions": []any{}, "complete": true}, 3, nil
	})
	session := fixture.session(t, alice)

	for range 3 {
		if got := fixture.call(t, "GET", "/fleet/sessions?wait_ms=99999", nil, session, ""); got.status != http.StatusOK || got.body["complete"] != true {
			t.Fatalf("session list: HTTP %d %v", got.status, got.body)
		}
	}
	if got := fixture.call(t, "GET", "/fleet/sessions", nil, nil, ""); got.status == http.StatusOK {
		t.Fatalf("anonymous session list answered HTTP %d", got.status)
	}
	mu.Lock()
	if len(asked) != 3 || asked[0] != alice.ID || waits[0] != sessionListMaxWait {
		t.Fatalf("listed for %v with waits %v; want alice with the capped wait", asked, waits)
	}
	mu.Unlock()

	audits := func() (lists, opens int) {
		for _, row := range auditPayloadRows(t, fixture.service.db, auditSourceFleet, auditKindDeviceTerminal) {
			switch row["action"] {
			case "terminal_list":
				if row["devices"] != float64(3) {
					t.Fatalf("listing audit = %v; want the device count", row)
				}
				lists++
			case "terminal_open":
				opens++
			}
		}
		return lists, opens
	}
	if lists, opens := audits(); lists != 1 || opens != 0 {
		t.Fatalf("audits list=%d open=%d; want one coalesced listing record and no terminal opens", lists, opens)
	}
	now.Add(sessionListAuditInterval.Milliseconds())
	fixture.call(t, "GET", "/fleet/sessions", nil, session, "")
	if lists, _ := audits(); lists != 2 {
		t.Fatalf("listing audits after the interval = %d, want 2", lists)
	}
}

func TestCheckDeviceTerminalAppliesTheOwnerRulesWithoutAnAudit(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "check-owner")
	other := fixture.createUser(t, "check-other")
	device := fixture.enrollAgent(t, owner, "check-box")
	ctx := context.Background()
	if err := fixture.service.CheckDeviceTerminal(ctx, owner.ID, device.DeviceID); err != nil {
		t.Fatalf("owner check = %v", err)
	}
	if err := fixture.service.CheckDeviceTerminal(ctx, other.ID, device.DeviceID); err == nil {
		t.Fatal("another account passed the check")
	}
	if err := fixture.service.SetTerminalEnabled(ctx, fixture.identity(owner), device.DeviceID, false); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.CheckDeviceTerminal(ctx, owner.ID, device.DeviceID); err == nil {
		t.Fatal("a device with terminal access off passed the check")
	}
	if err := fixture.service.RevokeDevice(ctx, fixture.identity(owner), device.DeviceID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.CheckDeviceTerminal(ctx, owner.ID, device.DeviceID); err == nil {
		t.Fatal("a revoked device passed the check")
	}
	for _, row := range fixture.auditRows(t, auditKindDeviceTerminal) {
		if row["action"] == "terminal_open" {
			t.Fatalf("the check wrote a terminal audit: %v", row)
		}
	}
}
