package sync

import (
	"context"
	"crypto/ed25519"
	"net"
	"net/http"
	urlpkg "net/url"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	p2pclient "github.com/Hello-CTF/NexTerm/internal/p2p/client"
	"github.com/Hello-CTF/NexTerm/internal/p2p/grant"
)

// Direct-connection routes of the linked server, for the path manager.

// P2PConfig fetches GET /fleet/p2p/config.
func (p *RemoteProxy) P2PConfig(ctx context.Context) (*agent.P2PConfig, error) {
	return remoteGet(ctx, p, func(ctx context.Context, client *remoteClient) (*agent.P2PConfig, error) {
		var view agent.P2PConfig
		if err := client.api(ctx, http.MethodGet, "/fleet/p2p/config", nil, &view); err != nil {
			return nil, err
		}
		return &view, nil
	})
}

// P2PGrant asks POST /fleet/devices/{id}/p2p/grant for a grant bound to clientPub; it also renews one.
func (p *RemoteProxy) P2PGrant(ctx context.Context, deviceID string, clientPub ed25519.PublicKey) (*p2pclient.Grant, error) {
	if deviceID == "" {
		return nil, ipc.NewError(ipc.CodeBadParam, "deviceId 不能为空")
	}
	return remoteGet(ctx, p, func(ctx context.Context, client *remoteClient) (*p2pclient.Grant, error) {
		var view p2pclient.Grant
		path := "/fleet/devices/" + urlpkg.PathEscape(deviceID) + "/p2p/grant"
		if err := client.api(ctx, http.MethodPost, path, map[string]string{"client_pub": grant.Key(clientPub).String()}, &view); err != nil {
			return nil, err
		}
		return &view, nil
	})
}

// DeviceP2PKeyState reports the device's direct-connection key state from the device list; "" when the server
// predates direct connections or the device has no agent.
func (p *RemoteProxy) DeviceP2PKeyState(ctx context.Context, deviceID string) (string, error) {
	devices, err := p.ListDevices(ctx)
	if err != nil {
		return "", err
	}
	for _, device := range devices.Devices {
		if device.ID == deviceID {
			if device.Agent == nil || device.Agent.P2PKey == nil {
				return "", nil
			}
			return device.Agent.P2PKey.State, nil
		}
	}
	return "", ipc.NewError(ipc.CodeNotFound, "设备不存在或已撤销")
}

// P2PEndpoint is the linked server URL and whether a manual proxy is configured for it.
func (p *RemoteProxy) P2PEndpoint(ctx context.Context) (p2pclient.Endpoint, error) {
	config, err := p.service.remoteConfig(ctx)
	if err != nil {
		return p2pclient.Endpoint{}, err
	}
	return p2pclient.Endpoint{URL: config.URL, Proxied: config.Proxy.Enabled()}, nil
}

// DialDeviceBridgeCarrier dials one relay bridge offering a stream carrier; stream reports whether the server and
// agent carry one. With resume, refusals that a retry cannot fix are rstream.Permanent.
func (p *RemoteProxy) DialDeviceBridgeCarrier(ctx context.Context, deviceID, purpose string, resume bool) (net.Conn, bool, error) {
	if deviceID == "" {
		return nil, false, ipc.NewError(ipc.CodeBadParam, "deviceId 不能为空")
	}
	conn, stream, err := p.dialDeviceBridgeCarrier(ctx, deviceID, purpose, resume)
	if err != nil && resume {
		return nil, false, resumeDialError(err)
	}
	return conn, stream, err
}

// DialDeviceSignal opens the raw p2p-signal bridge to a device.
func (p *RemoteProxy) DialDeviceSignal(ctx context.Context, deviceID string) (net.Conn, error) {
	if deviceID == "" {
		return nil, ipc.NewError(ipc.CodeBadParam, "deviceId 不能为空")
	}
	var conn net.Conn
	err := p.call(ctx, func(ctx context.Context, client *remoteClient) error {
		dialed, _, err := p.service.engine.dialDevice(ctx, client, deviceID, agent.PurposeP2PSignal, false, false, func(ctx context.Context) (net.Conn, bool, error) {
			conn, err := client.dialDeviceBridge(ctx, deviceID, agent.PurposeP2PSignal)
			return conn, false, err
		})
		if err != nil {
			return err
		}
		conn = dialed
		return nil
	})
	return conn, err
}
