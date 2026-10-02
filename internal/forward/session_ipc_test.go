package forward

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type sessionTransportWithoutDialer struct {
	base.Transport
}

func TestSessionManagerCreationIPCErrorCodes(t *testing.T) {
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(_ context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
			return sessionTransportWithoutDialer{Transport: newSessionDialTransport("", generation)}, nil
		}),
		IdleTimeout: -1,
	})
	t.Cleanup(func() { _ = manager.Close() })
	service := NewService(Config{Provider: manager, Policy: Policy{Desktop: true}})
	t.Cleanup(func() { _ = service.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := service.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	assertCreationError := func(sessionID string, want ipc.Code) {
		t.Helper()
		for _, command := range []string{"forward_create", "forward_create_socks"} {
			var args []byte
			var err error
			if command == "forward_create" {
				args, err = json.Marshal(CreateLocalArgs{SessionID: sessionID, TargetHost: "host", TargetPort: 22})
			} else {
				args, err = json.Marshal(CreateSocksArgs{SessionID: sessionID})
			}
			if err != nil {
				t.Fatal(err)
			}
			response := dispatcher.Dispatch(t.Context(), ipc.Request{Command: command, Args: args}, ipc.Environment{})
			if response.OK || response.Error == nil || response.Error.Code != want {
				t.Fatalf("%s session %q response = %+v, want %s", command, sessionID, response, want)
			}
		}
	}

	t.Run("local unsupported", func(t *testing.T) {
		connectedSession, err := manager.Connect(t.Context(), session.Asset{ID: "local-asset", Kind: session.KindLocal})
		if err != nil {
			t.Fatal(err)
		}
		assertCreationError(connectedSession.ID, ipc.CodeUnsupported)
	})
	t.Run("unknown session not found", func(t *testing.T) {
		assertCreationError("missing-session", ipc.CodeNotFound)
	})
	t.Run("disconnected", func(t *testing.T) {
		connectedSession, err := manager.Connect(t.Context(), session.Asset{ID: "ssh-asset", Kind: session.KindSSH})
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.Disconnect(connectedSession.ID); err != nil {
			t.Fatal(err)
		}
		assertCreationError(connectedSession.ID, ipc.CodeDisconnected)
	})
}
