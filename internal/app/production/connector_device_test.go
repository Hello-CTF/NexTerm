package production

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/device"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
)

func deviceAsset(deviceID string) session.Asset {
	return session.Asset{
		ID:      "device/" + deviceID,
		Name:    "dev",
		Kind:    session.KindDevice,
		Options: map[string]any{"deviceId": deviceID},
	}
}

func TestConnectDeviceRequiresConnectorConfig(t *testing.T) {
	connector := &productionConnector{}
	_, err := connector.Connect(t.Context(), deviceAsset("dev-1"), 1)
	if !errors.Is(err, session.ErrUnsupported) {
		t.Fatalf("unset device connector = %v; want ErrUnsupported", err)
	}
}

func TestConnectDeviceRequiresDeviceID(t *testing.T) {
	connector := &productionConnector{}
	connector.device.Store(&DeviceConnectorConfig{
		Dial:   func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("dial must not run") },
		Digest: func(context.Context, string) (string, error) { return "digest", nil },
	})
	_, err := connector.Connect(t.Context(), deviceAsset(""), 1)
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam {
		t.Fatalf("empty deviceId = %v; want bad_param", err)
	}
}

func TestConnectDeviceAuthorizesBeforeDial(t *testing.T) {
	ctx := t.Context()
	authorizeErr := ipc.NewError(ipc.CodeForbidden, "设备不属于该用户")
	var calls []string
	connector := &productionConnector{}
	connector.device.Store(&DeviceConnectorConfig{
		Dial: func(context.Context, string, string) (net.Conn, error) {
			calls = append(calls, "dial")
			return nil, errors.New("dial must not run")
		},
		Digest: func(context.Context, string) (string, error) {
			calls = append(calls, "digest")
			return "digest", nil
		},
		Authorize: func(_ context.Context, userID, deviceID string) error {
			calls = append(calls, "authorize:"+userID+":"+deviceID)
			return authorizeErr
		},
	})
	_, err := connector.Connect(ctx, deviceAsset("dev-1"), 1)
	if !errors.Is(err, authorizeErr) {
		t.Fatalf("authorize failure = %v; want the authorize error", err)
	}
	if len(calls) != 1 || calls[0] != "authorize::dev-1" {
		t.Fatalf("calls = %v; authorize must run first and alone", calls)
	}
}

func TestConnectDeviceAuthorizeUserIDFromOptions(t *testing.T) {
	ctx := t.Context()
	var gotUser, gotDevice string
	connector := &productionConnector{}
	connector.device.Store(&DeviceConnectorConfig{
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("not dialed in this test")
		},
		Digest: func(context.Context, string) (string, error) { return "digest", nil },
		Authorize: func(_ context.Context, userID, deviceID string) error {
			gotUser, gotDevice = userID, deviceID
			return nil
		},
	})
	// Reconnect runs in a background ctx (no userID): authorization falls back to reading the authenticated user the IPC layer wrote into asset options.
	asset := deviceAsset("dev-1")
	asset.Options = map[string]any{"deviceId": "dev-1", "userId": "u-1"}
	if _, err := connector.Connect(ctx, asset, 2); err != nil {
		t.Fatal(err)
	}
	if gotUser != "u-1" || gotDevice != "dev-1" {
		t.Fatalf("authorize = (%q, %q); want (u-1, dev-1) from asset options", gotUser, gotDevice)
	}

	ctx = ipc.WithUserID(ctx, "u-ctx")
	if _, err := connector.Connect(ctx, asset, 3); err != nil {
		t.Fatal(err)
	}
	if gotUser != "u-ctx" {
		t.Fatalf("authorize user = %q; request ctx user must win over options", gotUser)
	}
}

func TestConnectDeviceDigestFailurePropagates(t *testing.T) {
	digestErr := ipc.NewError(ipc.CodeDisconnected, "设备未返回终端状态，可能已离线")
	connector := &productionConnector{}
	connector.device.Store(&DeviceConnectorConfig{
		Dial:   func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("dial must not run") },
		Digest: func(context.Context, string) (string, error) { return "", digestErr },
	})
	_, err := connector.Connect(t.Context(), deviceAsset("dev-1"), 1)
	if !errors.Is(err, digestErr) {
		t.Fatalf("digest failure = %v; want the digest error", err)
	}
}

func TestConnectDeviceReturnsDeviceTransport(t *testing.T) {
	ctx := t.Context()
	var authorized string
	connector := &productionConnector{}
	connector.device.Store(&DeviceConnectorConfig{
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("not dialed in this test")
		},
		Digest: func(_ context.Context, deviceID string) (string, error) { return "digest-" + deviceID, nil },
		Authorize: func(_ context.Context, userID, deviceID string) error {
			authorized = deviceID
			return nil
		},
	})
	transport, err := connector.Connect(ctx, deviceAsset("dev-1"), 3)
	if err != nil {
		t.Fatal(err)
	}
	if authorized != "dev-1" {
		t.Fatalf("authorized device = %q; want dev-1", authorized)
	}
	deviceTransport, ok := transport.(*device.Transport)
	if !ok {
		t.Fatalf("transport = %T; want *device.Transport", transport)
	}
	if deviceTransport.Kind() != session.KindDevice || deviceTransport.Generation() != 3 {
		t.Fatalf("transport = %s gen %d; want device gen 3", deviceTransport.Kind(), deviceTransport.Generation())
	}
	if _, err := transport.Exec(ctx, "echo hi", base.ExecOptions{}); err == nil || !strings.Contains(err.Error(), "not dialed") {
		t.Fatalf("Exec error = %v; want the bridge dial failure", err)
	}
}

func TestProductionDurableResolverFanOut(t *testing.T) {
	resolver := productionDurableResolver{}
	transport := device.New("dev-1", "digest", func(context.Context, string) (net.Conn, error) {
		return nil, errors.New("not dialed in this test")
	}, 1)
	provider, err := resolver.ResolveDurable(t.Context(), transport)
	if err != nil {
		t.Fatal(err)
	}
	if provider == nil {
		t.Fatal("device transport must resolve a durable provider")
	}
	if _, err := resolver.ResolveDurable(t.Context(), local.NewWithConfig(local.Config{})); !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("non-SSH non-device transport without ssh resolver = %v; want ErrUnsupported", err)
	}
}

func TestConnectDeviceErrorMessageMentionsConfig(t *testing.T) {
	connector := &productionConnector{}
	_, err := connector.Connect(t.Context(), deviceAsset("dev-1"), 1)
	if err == nil || !strings.Contains(err.Error(), "设备终端通道未配置") {
		t.Fatalf("unset connector error = %v; want the configuration hint", err)
	}
}

func TestConnectDevicePassesPlatformToTransport(t *testing.T) {
	connector := &productionConnector{}
	connector.device.Store(&DeviceConnectorConfig{
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("not dialed in this test")
		},
		Digest:   func(_ context.Context, deviceID string) (string, error) { return "digest-" + deviceID, nil },
		Platform: func(_ context.Context, _ string) (string, error) { return "windows", nil },
	})
	transport, err := connector.Connect(t.Context(), deviceAsset("dev-1"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := transport.(*device.Transport).Platform(); got != "windows" {
		t.Fatalf("platform = %q, want windows", got)
	}
}
