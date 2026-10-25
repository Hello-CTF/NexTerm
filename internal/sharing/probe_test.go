package sharing

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// probeService starts another Service with an agent probe on the shared fixture database; the probe behavior is given
// by the caller; the original fixture.service has no probe and is used to create link/share rows first.
func probeService(t *testing.T, f *serviceFixture, probe AgentProbeFunc) *Service {
	t.Helper()
	service, err := New(Config{DB: f.db, Accounts: f.accounts}, WithNow(func() int64 { return f.now }), WithAgentProbe(probe))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestAgentProbeDeniesCreate(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "probe-create")
	deviceID := fixture.createAgentDevice(t, owner, "probe-box")
	fixture.setAgentOnline(t, deviceID, true)

	calls := 0
	service := probeService(t, fixture, func(ctx context.Context, deviceID string) error {
		calls++
		return ipc.NewError(ipc.CodeDisconnected, "设备代理离线")
	})
	if _, err := service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, fixture.createUser(t, "probe-rcpt").Username, false, 0); ipc.NormalizeError(err).Code != ipc.CodeDisconnected {
		t.Fatalf("CreateHostShare err = %v, want probe denial", err)
	}
	if calls == 0 {
		t.Fatal("agent probe was not consulted")
	}
}

func TestAgentProbeDeniesTerminalOpenWithAudit(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "probe-resolve")
	deviceID := fixture.createAgentDevice(t, owner, "probe-box")
	fixture.setAgentOnline(t, deviceID, true)

	service := probeService(t, fixture, func(ctx context.Context, deviceID string) error {
		return ipc.NewError(ipc.CodeDisconnected, "设备代理离线")
	})
	recipient := fixture.createUser(t, "probe-terminal")
	if _, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, recipient.Username, false, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthorizeTerminalOpen(context.Background(), fixture.identity(recipient), deviceID); ipc.NormalizeError(err).Code != ipc.CodeDisconnected {
		t.Fatalf("AuthorizeTerminalOpen err = %v, want probe denial", err)
	}
	hostRows := fixture.auditRows(t, auditKindHostTerminal)
	lastHost := hostRows[len(hostRows)-1]
	if lastHost["outcome"] != "deny" || lastHost["reason"] != "agent_offline" {
		t.Fatalf("host audit row = %v, want agent_offline deny", lastHost)
	}
}

func TestAgentProbeAllowPasses(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "probe-allow")
	deviceID := fixture.createAgentDevice(t, owner, "probe-box")
	fixture.setAgentOnline(t, deviceID, true)
	service := probeService(t, fixture, func(ctx context.Context, deviceID string) error { return nil })
	if _, err := service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, fixture.createUser(t, "probe-allow-rcpt").Username, false, 0); err != nil {
		t.Fatalf("CreateHostShare with allow probe: %v", err)
	}
}

func TestAgentProbeSkippedWhenLastSeenStale(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "probe-stale")
	deviceID := fixture.createAgentDevice(t, owner, "probe-box")
	fixture.setAgentOnline(t, deviceID, false)
	calls := 0
	service := probeService(t, fixture, func(ctx context.Context, deviceID string) error {
		calls++
		return nil
	})
	if _, err := service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, fixture.createUser(t, "probe-stale-rcpt").Username, false, 0); ipc.NormalizeError(err).Code != ipc.CodeDisconnected {
		t.Fatalf("CreateHostShare err = %v, want stale last_seen denial", err)
	}
	if calls != 0 {
		t.Fatalf("probe consulted %d times despite stale last_seen", calls)
	}
}
