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
			"tabId":        info.ID,
			"channelId":    "channel-a",
			"clientId":     payloadClient,
			"attachmentId": info.AttachmentID,
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
