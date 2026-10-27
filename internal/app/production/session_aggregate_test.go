package production

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
)

func TestSessionAggregateListsTheAccountsDevicesThroughTheBridge(t *testing.T) {
	var mu sync.Mutex
	var askedFor []string
	var dialed []string
	connector := &productionConnector{}
	connector.device.Store(&DeviceConnectorConfig{
		Dial: func(_ context.Context, deviceID, purpose string) (net.Conn, error) {
			mu.Lock()
			dialed = append(dialed, deviceID+"/"+purpose)
			mu.Unlock()
			return nil, errors.New("bridge down")
		},
		Devices: func(_ context.Context, userID string) ([]AggregateDevice, error) {
			mu.Lock()
			askedFor = append(askedFor, userID)
			mu.Unlock()
			return []AggregateDevice{
				{ID: "on", Name: "online", Kind: "local-helper", Online: true, Digest: "digest", TerminalEnabled: true},
				{ID: "off", Name: "offline", TerminalEnabled: true},
				{ID: "closed", Name: "closed", Online: true, Digest: "digest"},
			}, nil
		},
	})
	events := make(chan ipc.Event, 4)
	manager := session.NewManager(session.Config{})
	t.Cleanup(func() { _ = manager.Close() })
	service := newSessionAggregateService(manager, connector, ipc.EmitterFunc(func(_ context.Context, event ipc.Event) error {
		events <- event
		return nil
	}))
	t.Cleanup(func() { _ = service.shutdown(context.Background()) })

	list, err := service.list(ipc.WithUserID(context.Background(), "alice"), sessionAggregateRequest{WaitMS: -1})
	if err != nil {
		t.Fatal(err)
	}
	statuses := make(map[string]session.AggregateHostStatus, len(list.Hosts))
	for _, host := range list.Hosts {
		statuses[host.Key] = host
	}
	if got := statuses["device:on"]; got.Status != session.AggregateHostPending || got.DeviceKind != "local-helper" || got.SessionID != "" {
		t.Fatalf("online device = %+v; want a pending listing without a session", got)
	}
	if got := statuses["device:off"].Status; got != session.AggregateHostOffline {
		t.Fatalf("offline device status = %q", got)
	}
	if got := statuses["device:closed"]; got.Status != session.AggregateHostFailed || got.ErrorCode != string(ipc.CodeForbidden) {
		t.Fatalf("terminal-disabled device = %+v", got)
	}

	select {
	case event := <-events:
		update, ok := event.Payload.(session.AggregateUpdate)
		if event.Event != ipc.TopicTerminalSessions || event.OwnerID != "alice" || !ok || update.Host.Key != "device:on" || update.Host.Status != session.AggregateHostFailed {
			t.Fatalf("event = %+v; want alice's failed device refresh", event)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no refresh event")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(askedFor) != 1 || askedFor[0] != "alice" {
		t.Fatalf("devices listed for %v; want the requester only", askedFor)
	}
	if len(dialed) == 0 || dialed[0] != "on/terminal" {
		t.Fatalf("dialed %v; want only the online device's terminal bridge", dialed)
	}
}

func TestRemoteAggregateDevicesLeavesOutThisDesktopAndRevokedDevices(t *testing.T) {
	agent := &syncservice.RemoteFleetAgent{Platform: "linux", TerminalEnabled: true, StateDigest: "d"}
	devices := remoteAggregateDevices([]syncservice.RemoteFleetDevice{
		{ID: "self", Name: "this desktop", Kind: "local-helper", Agent: agent},
		{ID: "gone", Name: "revoked", RevokedAt: 1, Agent: agent},
		{ID: "bare", Name: "no agent"},
		{ID: "box", Name: "server", Agent: agent},
		{ID: "sleepy", Name: "offline", Agent: &syncservice.RemoteFleetAgent{TerminalEnabled: true}},
	}, "self")
	if len(devices) != 2 || devices[0].ID != "box" || !devices[0].Online || devices[1].ID != "sleepy" || devices[1].Online {
		t.Fatalf("devices = %+v", devices)
	}
}
