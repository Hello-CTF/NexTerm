package fleetserver

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/fs/remote"
	"github.com/coder/websocket"
)

// A desktop reaches the file-mux channel through the HTTP relay: the bridge request names file-mux, the agent's
// multiplexed file server answers several operations over the one relayed bridge, and the open audits as file_open.
func TestDeviceBridgeRelayFileMuxEndToEnd(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "relay-filemux")
	deviceID, secret := enrollWSAgent(t, f, owner, "relay-filemux-box")
	session := f.session(t, owner)

	control := dialDeviceWS(t, f)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
		Capabilities: []string{agent.PurposeTerminal, deviceproto.PurposeFile, deviceproto.PurposeFileMux},
	}); response.Type != "hello_ok" {
		t.Fatalf("hello = %+v", response)
	}
	defer func() { _ = control.Close(websocket.StatusNormalClosure, "") }()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	served := make(chan error, 1)
	go func() {
		readCtx, readCancel := context.WithTimeout(ctx, 10*time.Second)
		defer readCancel()
		var request controlMessage
		if err := readJSONMessage(readCtx, control, &request); err != nil {
			served <- err
			return
		}
		if request.Type != "bridge" || request.Purpose != deviceproto.PurposeFileMux {
			served <- errors.New("unexpected control message " + request.Type + " " + request.Purpose)
			return
		}
		bridgeWS, err := dialAgentBridge(ctx, f, agent.HelloMessage{
			Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
			BridgeID: request.BridgeID, Purpose: deviceproto.PurposeFileMux,
		})
		if err != nil {
			served <- err
			return
		}
		served <- remote.ServeMux(ctx, websocket.NetConn(ctx, bridgeWS, websocket.MessageBinary))
	}()

	relay := dialRelay(t, f, deviceID, session.cookie, deviceproto.PurposeFileMux)
	relay.SetReadLimit(1 << 20)
	dials := 0
	mux := remote.NewMux(func(context.Context) (net.Conn, error) {
		dials++
		if dials > 1 {
			return nil, errors.New("file-mux channel redialed")
		}
		return websocket.NetConn(ctx, relay, websocket.MessageBinary), nil
	})
	defer func() { _ = mux.Close() }()
	files := remote.NewFileSystem(remote.Options{Dial: mux.Open})

	path := filepath.Join(t.TempDir(), "note.txt")
	if err := files.WriteFile(ctx, path, []byte("over the relay"), false); err != nil {
		t.Fatalf("write through file-mux relay: %v", err)
	}
	data, err := files.ReadFile(ctx, path, 1<<20)
	if err != nil || string(data) != "over the relay" {
		t.Fatalf("read through file-mux relay = %q, %v", data, err)
	}
	if onDisk, err := os.ReadFile(path); err != nil || string(onDisk) != "over the relay" {
		t.Fatalf("file on the device = %q, %v", onDisk, err)
	}
	_ = mux.Close()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("agent file-mux server: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("agent file-mux server did not end after the client closed the channel")
	}

	audits := auditPayloadRows(t, f.service.db, auditSourceFleet, auditKindDeviceTerminal)
	if len(audits) != 1 || audits[0]["action"] != "file_open" || audits[0]["outcome"] != "allow" {
		t.Fatalf("file-mux audits = %v; want one allowed file_open", audits)
	}
}

// readJSONMessage reads one JSON websocket message; unlike readControlMessage it is safe off the test goroutine.
func readJSONMessage(ctx context.Context, conn *websocket.Conn, message any) error {
	_, raw, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, message)
}

// dialAgentBridge dials /ws/device as the agent's outbound bridge and completes its hello, off the test goroutine.
func dialAgentBridge(ctx context.Context, f *httpFixture, hello agent.HelloMessage) (*websocket.Conn, error) {
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(f.http.URL, "http")+agent.PathDeviceWS, nil)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(hello)
	if err != nil {
		conn.CloseNow()
		return nil, err
	}
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		conn.CloseNow()
		return nil, err
	}
	var response controlMessage
	if err := readJSONMessage(ctx, conn, &response); err != nil {
		conn.CloseNow()
		return nil, err
	}
	if response.Type != "hello_ok" {
		conn.CloseNow()
		return nil, errors.New("bridge hello answered " + response.Type)
	}
	conn.SetReadLimit(1 << 20)
	return conn, nil
}
