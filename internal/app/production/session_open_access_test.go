package production

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// The session manager's open-access boundary must follow the deployment
// scope: desktop builds and explicitly open servers accept identity-less
// IPC, while a non-open server rejects it. The builder propagates
// config.Desktop || owner.OpenAccess(ctx) into the manager.
func TestProductionOpenAccessFollowsDeploymentScope(t *testing.T) {
	newProduction := func(t *testing.T, ctx context.Context, desktop bool) *Production {
		t.Helper()
		application, err := NewProduction(ctx, ProductionConfig{
			Config: Config{
				Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
				Streams: ipc.StreamFactoryFuncs{},
			},
			DataDir: t.TempDir(),
			Desktop: desktop,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := application.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = application.Shutdown(context.Background()) })
		return application
	}
	dispatchList := func(t *testing.T, application *Production) ipc.Response {
		t.Helper()
		return application.Dispatcher.Dispatch(t.Context(), ipc.Request{
			Command: "session_list", Args: json.RawMessage(`null`),
		}, application.Environment("client-a"))
	}

	t.Run("desktop accepts identity-less", func(t *testing.T) {
		application := newProduction(t, context.Background(), true)
		if response := dispatchList(t, application); !response.OK {
			t.Fatalf("desktop session_list = %+v, want OK", response)
		}
	})
	t.Run("explicitly open non-desktop accepts identity-less", func(t *testing.T) {
		application := newProduction(t, owner.WithOpenAccess(context.Background()), false)
		if response := dispatchList(t, application); !response.OK {
			t.Fatalf("open server session_list = %+v, want OK", response)
		}
	})
	t.Run("non-open server rejects identity-less", func(t *testing.T) {
		application := newProduction(t, context.Background(), false)
		response := dispatchList(t, application)
		if response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
			t.Fatalf("non-open session_list = %+v, want forbidden", response)
		}
	})
}
