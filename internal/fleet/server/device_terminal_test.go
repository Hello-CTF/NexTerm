package fleetserver

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

func TestAuthorizeDeviceTerminal(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "dev-term-owner")
	other := fixture.createUser(t, "dev-term-other")
	result := fixture.enrollAgent(t, owner, "term-box")
	ctx := context.Background()

	if err := fixture.service.AuthorizeDeviceTerminal(ctx, owner.ID, result.DeviceID); err != nil {
		t.Fatalf("owner authorize = %v; want nil", err)
	}
	allows := fixture.auditRows(t, auditKindDeviceTerminal)
	if len(allows) != 1 || allows[0]["outcome"] != "allow" || allows[0]["action"] != "terminal_open" {
		t.Fatalf("terminal_open audits = %v; want one allow", allows)
	}

	var appErr *ipc.Error
	if err := fixture.service.AuthorizeDeviceTerminal(ctx, other.ID, result.DeviceID); !errors.As(err, &appErr) || appErr.Code != ipc.CodeForbidden {
		t.Fatalf("non-owner authorize = %v; want forbidden", err)
	}
	if err := fixture.service.AuthorizeDeviceTerminal(ctx, "", result.DeviceID); !errors.As(err, &appErr) || appErr.Code != ipc.CodeForbidden {
		t.Fatalf("empty user authorize = %v; want forbidden", err)
	}

	deviceAgent := query.Use(fixture.db).DeviceAgent
	if _, err := deviceAgent.WithContext(ctx).Where(deviceAgent.DeviceID.Eq(result.DeviceID)).
		UpdateColumns(map[string]any{"terminal_enabled": false}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.AuthorizeDeviceTerminal(ctx, owner.ID, result.DeviceID); !errors.As(err, &appErr) || appErr.Code != ipc.CodeForbidden {
		t.Fatalf("terminal disabled authorize = %v; want forbidden", err)
	}
	if _, err := deviceAgent.WithContext(ctx).Where(deviceAgent.DeviceID.Eq(result.DeviceID)).
		UpdateColumns(map[string]any{"terminal_enabled": true}); err != nil {
		t.Fatal(err)
	}

	if err := fixture.service.RevokeDevice(ctx, fixture.identity(owner), result.DeviceID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.AuthorizeDeviceTerminal(ctx, owner.ID, result.DeviceID); !errors.As(err, &appErr) || appErr.Code != ipc.CodeForbidden {
		t.Fatalf("revoked authorize = %v; want forbidden", err)
	}
	// 与桥端点同语义: 授权判定 (owner/吊销) 每次都写审计, 终端开关检查在审计之后,
	// 所以终端关闭的 owner 也是一条 allow; 空 userID 在进入授权前拒绝, 不落审计。
	audits := fixture.auditRows(t, auditKindDeviceTerminal)
	outcomes := make([]any, 0, len(audits))
	for _, row := range audits {
		outcomes = append(outcomes, row["outcome"])
	}
	if len(audits) != 4 || outcomes[0] != "allow" || outcomes[1] != "deny" || outcomes[2] != "allow" || outcomes[3] != "deny" {
		t.Fatalf("terminal_open audits = %v; want allow/deny/allow/deny", outcomes)
	}
}

func TestDeviceStateDigestFromControlHello(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "dev-digest-owner")
	deviceID, secret := enrollWSAgent(t, f, owner, "digest-box")

	if _, ok := f.service.DeviceStateDigest(deviceID); ok {
		t.Fatal("offline device must not report a state digest")
	}
	control := dialDeviceWS(t, f)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret, StateDigest: "digest-abc",
	}); response.Type != "hello_ok" {
		t.Fatalf("hello = %+v", response)
	}
	if digest, ok := f.service.DeviceStateDigest(deviceID); !ok || digest != "digest-abc" {
		t.Fatalf("DeviceStateDigest = %q %v; want digest-abc", digest, ok)
	}
}

func TestRequestDeviceBridgeOffline(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "dev-bridge-owner")
	result := fixture.enrollAgent(t, owner, "bridge-box")

	if _, err := fixture.service.RequestDeviceBridge(context.Background(), result.DeviceID); err == nil {
		t.Fatal("offline device bridge must fail")
	}
}
