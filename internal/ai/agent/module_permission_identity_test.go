package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

type permissionIdentityFixture struct {
	manager     *guard.Manager
	legacyCalls *atomic.Int64
	dispatcher  *ipc.Dispatcher
	tab         session.TabInfo
}

// newPermissionIdentityFixture wires the request-scoped session terminal the
// way production does: open managers for desktop/open deployments, closed
// managers for account servers.
func newPermissionIdentityFixture(t *testing.T, open bool, ownerID string) *permissionIdentityFixture {
	t.Helper()
	sessions := session.NewManager(session.Config{IdleTimeout: -1, OpenAccess: open})
	t.Cleanup(func() { _ = sessions.Close() })
	tab, err := sessions.RegisterRemoteSession(context.Background(), session.RemoteSessionOptions{
		OwnerID: ownerID, AssetID: "asset-a", Title: "terminal",
	})
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := guard.NewManager(context.Background(), &grantSettings{values: make(map[string]string)})
	if err != nil {
		t.Fatal(err)
	}
	deps := tools.WithSession(tools.Dependencies{}, sessions)
	legacyTabSession := deps.TabSession
	legacyCalls := &atomic.Int64{}
	deps.TabSession = func(tabID string) string {
		legacyCalls.Add(1)
		return legacyTabSession(tabID)
	}
	dispatcher := newGrantDispatcher(t, Config{Permissions: permissions, Tools: tools.NewRegistry(deps)})
	return &permissionIdentityFixture{manager: permissions, legacyCalls: legacyCalls, dispatcher: dispatcher, tab: tab}
}

func (f *permissionIdentityFixture) followGlobal(t *testing.T, ctx context.Context) ipc.Response {
	t.Helper()
	args, err := json.Marshal(map[string]any{
		"scope":        map[string]any{"tabId": f.tab.ID},
		"followGlobal": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return f.dispatcher.Dispatch(ctx, ipc.Request{Command: "ai_set_permission", Args: args}, ipc.Environment{})
}

func TestAIPermissionFollowGlobalDeviceIdentityContract(t *testing.T) {
	t.Run("identity-less vouched caller fails closed on account server", func(t *testing.T) {
		fixture := newPermissionIdentityFixture(t, false, "desktop")
		if err := fixture.manager.SetDeviceMode(context.Background(), "asset-a", guard.Silent); err != nil {
			t.Fatal(err)
		}
		// The HTTP layer vouches identity-less callers only on open deployments;
		// the vouch alone must not resolve device identity on a closed manager.
		response := fixture.followGlobal(t, WithOpenAccess(context.Background()))
		if response.OK || response.Error == nil || !strings.Contains(response.Error.Message, "无法确定设备身份") {
			t.Fatalf("identity-less followGlobal on account server = %+v", response)
		}
		if mode, ok := mustAgentDeviceMode(t, fixture.manager, "asset-a"); !ok || mode != guard.Silent {
			t.Fatalf("device mode must stay untouched, got %v %v", mode, ok)
		}
		if calls := fixture.legacyCalls.Load(); calls != 0 {
			t.Fatalf("request path used unscoped TabSession %d times", calls)
		}
	})

	t.Run("identity-less caller resolves device on open desktop deployment", func(t *testing.T) {
		fixture := newPermissionIdentityFixture(t, true, "desktop")
		if err := fixture.manager.SetDeviceMode(context.Background(), "asset-a", guard.Silent); err != nil {
			t.Fatal(err)
		}
		response := fixture.followGlobal(t, WithOpenAccess(context.Background()))
		if !response.OK {
			t.Fatalf("open desktop followGlobal failed: %+v", response.Error)
		}
		if got := permissionOf(t, response.Data); got.Mode != guard.ReadWrite {
			t.Fatalf("cleared response = %+v", got)
		}
		if _, ok := mustAgentDeviceMode(t, fixture.manager, "asset-a"); ok {
			t.Fatal("device mode override not cleared")
		}
		if calls := fixture.legacyCalls.Load(); calls != 0 {
			t.Fatalf("request path used unscoped TabSession %d times", calls)
		}
	})

	t.Run("account owner resolves own device and peer account fails closed", func(t *testing.T) {
		fixture := newPermissionIdentityFixture(t, false, "alice")
		alice := ipc.WithUserID(context.Background(), "alice")
		if err := fixture.manager.SetDeviceMode(alice, "asset-a", guard.Silent); err != nil {
			t.Fatal(err)
		}
		if response := fixture.followGlobal(t, alice); !response.OK {
			t.Fatalf("owner followGlobal failed: %+v", response.Error)
		}
		if _, ok := mustAgentDeviceMode(t, fixture.manager, "asset-a", alice); ok {
			t.Fatal("owner device mode override not cleared")
		}

		if err := fixture.manager.SetDeviceMode(alice, "asset-a", guard.Silent); err != nil {
			t.Fatal(err)
		}
		bob := ipc.WithUserID(context.Background(), "bob")
		response := fixture.followGlobal(t, bob)
		if response.OK || response.Error == nil || !strings.Contains(response.Error.Message, "无法确定设备身份") {
			t.Fatalf("cross-account followGlobal = %+v", response)
		}
		if mode, ok := mustAgentDeviceMode(t, fixture.manager, "asset-a", alice); !ok || mode != guard.Silent {
			t.Fatalf("cross-account call must not touch owner state, got %v %v", mode, ok)
		}
		if calls := fixture.legacyCalls.Load(); calls != 0 {
			t.Fatalf("request path used unscoped TabSession %d times", calls)
		}
	})
}
