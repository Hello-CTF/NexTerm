package fleetserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"github.com/coder/websocket"
)

// sendHello writes the hello frame without waiting for a reply.
func sendHello(t *testing.T, conn *websocket.Conn, hello agent.HelloMessage) {
	t.Helper()
	payload, err := json.Marshal(hello)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
}

// drainUntilClosed reads frames until the server closes the connection or the deadline passes.
func drainUntilClosed(conn *websocket.Conn, wait time.Duration) (closed bool) {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return ctx.Err() == nil
		}
	}
}

func TestDeviceControlRevokedBetweenAuthAndRegisterIsTornDown(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "race-owner")
	deviceID, secret := enrollWSAgent(t, f, owner, "race-box")
	f.service.afterAuthenticate = func(id string) {
		if err := f.service.RevokeDevice(context.Background(), &account.Identity{UserID: owner.ID, Role: owner.Role, State: owner.State}, id); err != nil {
			t.Error(err)
		}
	}
	conn := dialDeviceWS(t, f)
	sendHello(t, conn, agent.HelloMessage{Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret})
	if !drainUntilClosed(conn, 3*time.Second) {
		t.Fatal("control channel of a device revoked mid-handshake stayed open")
	}
	if f.service.registry.AgentOnline(deviceID) {
		t.Fatal("revoked device remained registered as online")
	}
}

func TestDeviceBridgeRevokedBetweenAuthAndPairIsTornDown(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "race-bridge-owner")
	deviceID, secret := enrollWSAgent(t, f, owner, "race-bridge-box")
	control := dialDeviceWS(t, f)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
		Capabilities: []string{agent.PurposeTerminal},
	}); response.Type != "hello_ok" {
		t.Fatalf("hello = %+v", response)
	}
	requested := make(chan error, 1)
	go func() {
		conn, err := f.service.registry.RequestBridge(context.Background(), deviceID, agent.PurposeTerminal)
		if conn != nil {
			defer conn.Close()
		}
		requested <- err
	}()
	request := readControlMessage(t, control, 5*time.Second)
	if request.Type != "bridge" {
		t.Fatalf("control message = %+v", request)
	}
	// Mutate the DB only (a revoke that races after authentication): the registry kick of the full path is
	// deliberately skipped so only the post-registration re-check can stop the bridge.
	f.service.afterAuthenticate = func(id string) {
		userDevice := query.Use(f.service.db).UserDevice
		if _, err := userDevice.WithContext(context.Background()).Where(userDevice.ID.Eq(id)).
			UpdateColumns(map[string]any{"revoked_at": time.Now().UnixMilli()}); err != nil {
			t.Error(err)
		}
	}
	bridge := dialDeviceWS(t, f)
	sendHello(t, bridge, agent.HelloMessage{Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret, BridgeID: request.BridgeID})
	if !drainUntilClosed(bridge, 3*time.Second) {
		t.Fatal("bridge of a device revoked mid-handshake stayed open")
	}
	select {
	case err := <-requested:
		if err == nil {
			t.Fatal("RequestBridge delivered a bridge for a revoked device")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RequestBridge never returned")
	}
}

func TestDisabledOwnerDeviceRejected(t *testing.T) {
	f := newServiceFixture(t)
	owner := f.createUser(t, "disabled-owner")
	result := f.enrollAgent(t, owner, "box")
	if _, err := f.service.AuthenticateAgent(context.Background(), result.DeviceID, result.Secret); err != nil {
		t.Fatal(err)
	}
	code, _, err := f.service.IssueEnrollCode(context.Background(), f.identity(owner), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.accounts.SetUserDisabled(context.Background(), owner.ID, true); err != nil {
		t.Fatal(err)
	}
	_, err = f.service.AuthenticateAgent(context.Background(), result.DeviceID, result.Secret)
	requireIPCCode(t, err, ipc.CodeForbidden)
	_, err = f.service.ConsumeEnrollCode(context.Background(), code, "late", "linux", "1.0.0")
	requireIPCCode(t, err, ipc.CodeForbidden)

	if err := f.accounts.SetUserDisabled(context.Background(), owner.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.AuthenticateAgent(context.Background(), result.DeviceID, result.Secret); err != nil {
		t.Fatalf("re-enabled owner must authenticate again: %v", err)
	}
}

func TestResetRequiredOwnerDeviceRejected(t *testing.T) {
	f := newServiceFixture(t)
	owner := f.createUser(t, "reset-owner")
	result := f.enrollAgent(t, owner, "box")
	if err := f.accounts.AdminResetUser(context.Background(), owner.ID); err != nil {
		t.Fatal(err)
	}
	_, err := f.service.AuthenticateAgent(context.Background(), result.DeviceID, result.Secret)
	requireIPCCode(t, err, ipc.CodeForbidden)
}

func TestDisablingOwnerClosesControlChannel(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "kick-disabled-owner")
	deviceID, secret := enrollWSAgent(t, f, owner, "kick-disabled-box")
	control := dialDeviceWS(t, f)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
	}); response.Type != "hello_ok" {
		t.Fatalf("hello = %+v", response)
	}
	if err := f.accounts.SetUserDisabled(context.Background(), owner.ID, true); err != nil {
		t.Fatal(err)
	}
	if !drainUntilClosed(control, 3*time.Second) {
		t.Fatal("control channel stayed open after the owner was disabled")
	}
	if f.service.registry.AgentOnline(deviceID) {
		t.Fatal("device of a disabled owner remained online")
	}
}
