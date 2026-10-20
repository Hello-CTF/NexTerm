package production

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// The trusted caller is bound to the attachment at the IPC boundary: when the
// environment and the payload name different callers, the report is rejected;
// a single source (environment-only or payload-only) is a legitimate
// fallback; an empty caller is rejected.
func TestProductionStreamAckBindsTrustedCaller(t *testing.T) {
	application, err := NewProduction(context.Background(), ProductionConfig{
		Config: Config{
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		DataDir: t.TempDir(),
		Desktop: true,
		Connector: session.ConnectorFunc(func(_ context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
			return newAccountCleanupTransport(asset.Kind, generation), nil
		}),
		Terminals: session.TerminalFactoryFunc(func(config session.TerminalConfig) (session.TerminalState, error) {
			return terminal.NewTab(config.TabID, config.SessionID, int(config.Cols), int(config.Rows), terminal.UTF8), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Shutdown(context.Background()) })

	connected, err := application.Services.Sessions.ConnectFor(owner.WithOpenAccess(t.Context()), session.Asset{ID: "asset-local", Name: "local", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	info, err := application.Services.Sessions.OpenTabFor(owner.WithOpenAccess(t.Context()), session.OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "channel-a", Cols: 80, Rows: 24, Ephemeral: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatch := func(environmentCaller, payloadClient string) ipc.Response {
		args, _ := json.Marshal(map[string]any{
			"tabId":         info.ID,
			"channelId":     "channel-a",
			"clientId":      payloadClient,
			"attachmentId":  info.AttachmentID,
			"acceptedBytes": 0,
		})
		return application.Dispatcher.Dispatch(t.Context(), ipc.Request{
			Command: "terminal_stream_ack", Args: args,
		}, application.Environment(environmentCaller))
	}

	// Environment caller and payload client disagree: rejected.
	if response := dispatch("client-a", "client-b"); response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
		t.Fatalf("mismatched caller = %+v, want forbidden", response)
	}
	// Environment-only caller: a legitimate single-source fallback.
	if response := dispatch("client-a", ""); !response.OK {
		t.Fatalf("environment-only caller = %+v, want OK", response)
	}
	// Payload-only client: a legitimate single-source fallback.
	if response := dispatch("", "client-a"); !response.OK {
		t.Fatalf("payload-only caller = %+v, want OK", response)
	}
	// Both empty: rejected.
	if response := dispatch("", ""); response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
		t.Fatalf("empty caller = %+v, want forbidden", response)
	}
}

// The stream-control commands share the ACK's trusted-caller and incarnation
// contract at the IPC boundary: pause, resume, and recover resolve the caller
// from the environment and the payload with the same consistency rules, and
// bind it to the attachment — a cross-client or stale-incarnation report is
// forbidden, never OK.
func TestProductionStreamControlBindsTrustedCaller(t *testing.T) {
	application, err := NewProduction(context.Background(), ProductionConfig{
		Config: Config{
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		DataDir: t.TempDir(),
		Desktop: true,
		Connector: session.ConnectorFunc(func(_ context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
			return newAccountCleanupTransport(asset.Kind, generation), nil
		}),
		Terminals: session.TerminalFactoryFunc(func(config session.TerminalConfig) (session.TerminalState, error) {
			return terminal.NewTab(config.TabID, config.SessionID, int(config.Cols), int(config.Rows), terminal.UTF8), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Shutdown(context.Background()) })

	connected, err := application.Services.Sessions.ConnectFor(owner.WithOpenAccess(t.Context()), session.Asset{ID: "asset-local", Name: "local", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	info, err := application.Services.Sessions.OpenTabFor(owner.WithOpenAccess(t.Context()), session.OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "channel-a", Cols: 80, Rows: 24, Ephemeral: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	infoB, err := application.Services.Sessions.AttachTabFor(owner.WithOpenAccess(t.Context()), info.ID, session.AttachOptions{
		ClientID: "client-b", ChannelID: "channel-b",
	})
	if err != nil {
		t.Fatal(err)
	}

	dispatch := func(command, environmentCaller, payloadClient string, attachmentID uint64, acceptedBytes int64, episodeToken uint64) ipc.Response {
		args, _ := json.Marshal(map[string]any{
			"tabId":         info.ID,
			"channelId":     "channel-b",
			"clientId":      payloadClient,
			"attachmentId":  attachmentID,
			"acceptedBytes": acceptedBytes,
			"episodeToken":  episodeToken,
		})
		return application.Dispatcher.Dispatch(t.Context(), ipc.Request{
			Command: command, Args: args,
		}, application.Environment(environmentCaller))
	}

	// Open the episode and learn the token the server issued for it, the way
	// the client does from the pause response.
	opened := dispatch("terminal_pause", "client-b", "", infoB.AttachmentID, 0, 0)
	if !opened.OK {
		t.Fatalf("opening pause = %+v, want OK", opened)
	}
	var openedPayload struct {
		EpisodeToken uint64 `json:"episodeToken"`
	}
	if err := json.Unmarshal(opened.Data, &openedPayload); err != nil || openedPayload.EpisodeToken == 0 {
		t.Fatalf("pause response = %s, want a nonzero episodeToken", opened.Data)
	}
	token := openedPayload.EpisodeToken

	for _, command := range []string{"terminal_pause", "terminal_resume", "terminal_recover"} {
		// Environment caller and payload client disagree: rejected.
		if response := dispatch(command, "client-a", "client-b", infoB.AttachmentID, 0, token); response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
			t.Fatalf("%s mismatched caller = %+v, want forbidden", command, response)
		}
		// Environment-only caller: a legitimate single-source fallback.
		if response := dispatch(command, "client-b", "", infoB.AttachmentID, 0, token); !response.OK {
			t.Fatalf("%s environment-only caller = %+v, want OK", command, response)
		}
		// Payload-only client: a legitimate single-source fallback.
		if response := dispatch(command, "", "client-b", infoB.AttachmentID, 0, token); !response.OK {
			t.Fatalf("%s payload-only caller = %+v, want OK", command, response)
		}
		// Both empty: rejected.
		if response := dispatch(command, "", "", infoB.AttachmentID, 0, token); response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
			t.Fatalf("%s empty caller = %+v, want forbidden", command, response)
		}
		// Cross-client: client A presenting client B's incarnation is forbidden.
		if response := dispatch(command, "client-a", "client-a", infoB.AttachmentID, 0, token); response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
			t.Fatalf("%s cross-client = %+v, want forbidden", command, response)
		}
		// Stale incarnation: the previous attachment id is forbidden.
		if response := dispatch(command, "client-b", "client-b", infoB.AttachmentID-1, 0, token); response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
			t.Fatalf("%s stale incarnation = %+v, want forbidden", command, response)
		}
		// Stale episode: a resume carrying the previous episode's token is
		// forbidden and never clears the current floor.
		if command == "terminal_resume" {
			if response := dispatch(command, "client-b", "client-b", infoB.AttachmentID, 0, token-1); response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
				t.Fatalf("%s stale episode token = %+v, want forbidden", command, response)
			}
		}
	}
}
