package production

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/device"
)

func newDeviceSessionRig(t *testing.T, platform string) (*accountCleanupRig, string) {
	t.Helper()
	ctx := ipcWithUser("owner-a")
	rig := newAccountCleanupRig(t, func(config *session.Config) {
		config.Connector = session.ConnectorFunc(func(_ context.Context, _ session.Asset, generation uint64) (base.Transport, error) {
			dial := func(context.Context, string) (net.Conn, error) { return nil, errors.New("no agent in unit test") }
			return device.New("dev-1", "", dial, generation, platform), nil
		})
	})
	connected, err := rig.manager.ConnectFor(ctx, session.Asset{ID: "device/dev-1", Kind: session.KindDevice, Name: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	return rig, connected.ID
}

func TestExecBatchResolverMarksDeviceAsUnknownEnvironment(t *testing.T) {
	ctx := ipcWithUser("owner-a")
	rig, sessionID := newDeviceSessionRig(t, "linux")
	// A nil asset reader proves the device path never consults the SSH asset store.
	target, err := execBatchResolver{sessions: rig.manager}.Resolve(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if target.Environment != guard.ManagedDeviceEnvironment {
		t.Fatalf("environment = %q, want the managed-device marker so confirmation is required", target.Environment)
	}
	production, err := guard.IsProductionEnvironment(ctx, nil, target.Environment)
	if err != nil || !production {
		t.Fatalf("managed-device environment must classify as production-equivalent: %v %v", production, err)
	}
}

func TestExecBatchResolverRejectsWindowsDevicesInPlainLanguage(t *testing.T) {
	ctx := ipcWithUser("owner-a")
	rig, sessionID := newDeviceSessionRig(t, "windows")
	_, err := execBatchResolver{sessions: rig.manager}.Resolve(ctx, sessionID)
	if !errors.Is(err, execbatch.ErrUnsupported) {
		t.Fatalf("windows device err = %v, want ErrUnsupported", err)
	}
	if strings.Contains(err.Error(), "unsupported") || !strings.Contains(err.Error(), "Windows 设备") {
		t.Fatalf("message should be Chinese only: %q", err.Error())
	}
}

func TestLogTailResolverAcceptsLinuxDeviceAndExplainsWindows(t *testing.T) {
	ctx := ipcWithUser("owner-a")
	linux, linuxSession := newDeviceSessionRig(t, "linux")
	target, err := logTailResolver{sessions: linux.manager}.Resolve(ctx, linuxSession)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := target.Transport.(base.ExecStreamTransport); !ok {
		t.Fatal("device log tail target must expose a streamed exec transport")
	}

	windows, windowsSession := newDeviceSessionRig(t, "windows")
	_, err = logTailResolver{sessions: windows.manager}.Resolve(ctx, windowsSession)
	if !errors.Is(err, session.ErrUnsupported) {
		t.Fatalf("windows device err = %v, want ErrUnsupported", err)
	}
	if strings.Contains(err.Error(), "capability") {
		t.Fatalf("message should not expose the English sentinel: %q", err.Error())
	}
}

func TestProductionAITargetResolutionIncludesOnlyExplicitOwnedDevices(t *testing.T) {
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	manager := session.NewManager(session.Config{Connector: session.ConnectorFunc(func(_ context.Context, _ session.Asset, generation uint64) (base.Transport, error) {
		return device.New("dev", "", func(context.Context, string) (net.Conn, error) { return nil, errors.New("unused") }, generation, "linux"), nil
	})})
	t.Cleanup(func() { _ = manager.Close() })
	services := &ProductionServices{Store: database, Sessions: manager}
	for _, user := range []store.AppUser{
		{ID: "owner-a", Username: "owner-a", Role: "user", PasswordHash: "x", State: "active", CreatedAt: 1, UpdatedAt: 1},
		{ID: "owner-b", Username: "owner-b", Role: "user", PasswordHash: "x", State: "active", CreatedAt: 1, UpdatedAt: 1},
	} {
		if err := database.GORM().Create(&user).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []store.UserDevice{
		{ID: "dev-a", UserID: "owner-a", Name: "A", Kind: "agent", CreatedAt: 1},
		{ID: "dev-off", UserID: "owner-a", Name: "Off", Kind: "agent", CreatedAt: 1},
		{ID: "dev-b", UserID: "owner-b", Name: "B", Kind: "agent", CreatedAt: 1},
	} {
		if err := database.GORM().Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	connected, err := manager.ConnectFor(ctxA, session.Asset{ID: "device/dev-a", Kind: session.KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ConnectFor(ctxB, session.Asset{ID: "device/dev-b", Kind: session.KindDevice}); err != nil {
		t.Fatal(err)
	}

	targets, err := resolveProductionExecTargets(ctxA, services, []string{"device/dev-a"}, nil)
	if err != nil || len(targets.SessionIDs) != 1 || targets.SessionIDs[0] != connected.ID {
		t.Fatalf("explicit device targets = %+v, %v", targets, err)
	}
	if !targets.Production {
		t.Fatal("device targets have no environment tag and must require confirmation")
	}
	for name, test := range map[string]struct {
		assets []string
		tags   []string
	}{
		"other owner's device":   {assets: []string{"device/dev-b"}},
		"disconnected device":    {assets: []string{"device/dev-off"}},
		"device with tag filter": {assets: []string{"device/dev-a"}, tags: []string{"blue"}},
		"unknown device":         {assets: []string{"device/missing"}},
	} {
		if _, err := resolveProductionExecTargets(ctxA, services, test.assets, test.tags); err == nil {
			t.Fatalf("%s: resolved without error", name)
		}
	}
	// Tag selection never sweeps in devices.
	if _, err := resolveProductionExecTargets(ctxA, services, nil, []string{"blue"}); err == nil {
		t.Fatal("tag selection must not match devices")
	}
}

func TestCronConnectNeverDialsDisconnectedDevices(t *testing.T) {
	_, err := cronConnectEvidence{services: &ProductionServices{}}.OwnerMatch(context.Background(), cron.Trigger{}, "device/dev-1")
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeDisconnected || !strings.Contains(appErr.Error(), "设备") {
		t.Fatalf("err = %v, want a Chinese disconnected error for devices", err)
	}
}
