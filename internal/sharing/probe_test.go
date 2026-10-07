package sharing

import (
	"context"
	"errors"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

// probeService 在共享夹具库上另起一个带 agent 探针的 Service, 探针行为由
// 调用方给出; 原 fixture.service 无探针, 用于先造出链接/分享行。
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

	deny := ipc.NewError(ipc.CodeDisconnected, "设备代理离线")
	calls := 0
	service := probeService(t, fixture, func(ctx context.Context, deviceID string) error {
		calls++
		return deny
	})
	_, _, err := service.CreateLink(context.Background(), fixture.identity(owner), deviceID, ids.New(), false, 0)
	if !errors.Is(err, deny) && ipc.NormalizeError(err).Code != ipc.CodeDisconnected {
		t.Fatalf("CreateLink err = %v, want probe denial", err)
	}
	if calls == 0 {
		t.Fatal("agent probe was not consulted")
	}
	if _, err := service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, fixture.createUser(t, "probe-rcpt").Username, false, 0); ipc.NormalizeError(err).Code != ipc.CodeDisconnected {
		t.Fatalf("CreateHostShare err = %v, want probe denial", err)
	}
}

func TestAgentProbeDeniesResolveWithAudit(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "probe-resolve")
	deviceID := fixture.createAgentDevice(t, owner, "probe-box")
	fixture.setAgentOnline(t, deviceID, true)
	_, token, err := fixture.service.CreateLink(context.Background(), fixture.identity(owner), deviceID, ids.New(), false, 0)
	if err != nil {
		t.Fatal(err)
	}

	service := probeService(t, fixture, func(ctx context.Context, deviceID string) error {
		return ipc.NewError(ipc.CodeDisconnected, "设备代理离线")
	})
	if _, err := service.ResolveLink(context.Background(), token); ipc.NormalizeError(err).Code != ipc.CodeDisconnected {
		t.Fatalf("ResolveLink err = %v, want probe denial", err)
	}
	rows := fixture.auditRows(t, auditKindLinkAccess)
	last := rows[len(rows)-1]
	if last["outcome"] != "deny" || last["reason"] != "agent_offline" {
		t.Fatalf("audit row = %v, want agent_offline deny", last)
	}

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
	_, token, err := service.CreateLink(context.Background(), fixture.identity(owner), deviceID, ids.New(), false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveLink(context.Background(), token); err != nil {
		t.Fatalf("ResolveLink with allow probe: %v", err)
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
	if _, _, err := service.CreateLink(context.Background(), fixture.identity(owner), deviceID, ids.New(), false, 0); ipc.NormalizeError(err).Code != ipc.CodeDisconnected {
		t.Fatalf("CreateLink err = %v, want stale last_seen denial", err)
	}
	if calls != 0 {
		t.Fatalf("probe consulted %d times despite stale last_seen", calls)
	}
}
