package production

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestComposedAIRuntimeShellHistoryFollowsDesktopMode(t *testing.T) {
	for _, test := range []struct {
		name    string
		desktop bool
		want    bool
	}{
		{name: "desktop", desktop: true, want: true},
		{name: "server", desktop: false, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			if !test.desktop {
				ctx = ipc.WithUserID(ctx, "owner-a")
			}
			database, err := store.OpenInMemory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			initTestVault(t, ctx, database)
			profileManager, err := profiles.NewManager(ctx, database, profiles.WithShared(test.desktop))
			if err != nil {
				t.Fatal(err)
			}
			requests := make(chan spawnScriptRequest, 4)
			provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					http.Error(writer, err.Error(), http.StatusBadRequest)
					return
				}
				var parsed spawnScriptRequest
				if err := json.Unmarshal(body, &parsed); err != nil {
					http.Error(writer, err.Error(), http.StatusBadRequest)
					return
				}
				requests <- parsed
				writer.Header().Set("Content-Type", "text/event-stream")
				spawnWriteContent(writer, "done")
			}))
			t.Cleanup(provider.Close)
			if _, err := profileManager.Save(ctx, profiles.Profile{
				BaseURL: provider.URL, APIKey: "test-key", Model: "test-model", ContextWindow: 32768, Stream: true,
			}); err != nil {
				t.Fatal(err)
			}
			sessions := session.NewManager(session.Config{})
			services := &ProductionServices{Store: database, Profiles: profileManager, Sessions: sessions, desktop: test.desktop}
			if err := composeAIRuntime(ctx, services); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				services.closeAIRuntime()
				_ = sessions.Close()
				_ = database.Close()
			})

			stream := &agent.SliceStream{}
			if _, err := services.Agent.Start(ctx, agent.ChatArgs{Message: "inspect tools"}, agent.StaticStream(stream)); err != nil {
				t.Fatal(err)
			}
			for _, event := range waitOutcomeClosed(t, stream) {
				if event.Type == "error" {
					t.Fatalf("composed agent run failed: %s", event.Message)
				}
			}
			select {
			case request := <-requests:
				advertised := false
				for _, name := range spawnToolNames(&request) {
					if name == "shell_history" {
						advertised = true
					}
				}
				if advertised != test.want {
					t.Fatalf("shell_history advertised = %v, want %v; tools = %v", advertised, test.want, spawnToolNames(&request))
				}
			case <-time.After(5 * time.Second):
				t.Fatal("provider did not receive the composed agent request")
			}
		})
	}
}
