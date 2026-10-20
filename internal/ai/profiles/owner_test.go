package profiles_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func testContext(contexts []context.Context) context.Context {
	if len(contexts) != 0 {
		return contexts[0]
	}
	return context.Background()
}

func mustOverview(t *testing.T, manager *profiles.Manager, contexts ...context.Context) profiles.Overview {
	t.Helper()
	overview, err := manager.Overview(testContext(contexts))
	if err != nil {
		t.Fatal(err)
	}
	return overview
}

func mustProfile(t *testing.T, manager *profiles.Manager, id string, contexts ...context.Context) (profiles.Profile, bool) {
	t.Helper()
	profile, ok, err := manager.Profile(testContext(contexts), id)
	if err != nil {
		t.Fatal(err)
	}
	return profile, ok
}

func mustActiveProfile(t *testing.T, manager *profiles.Manager, contexts ...context.Context) (profiles.Profile, bool) {
	t.Helper()
	profile, ok, err := manager.ActiveProfile(testContext(contexts))
	if err != nil {
		t.Fatal(err)
	}
	return profile, ok
}

func mustActiveConfig(t *testing.T, manager *profiles.Manager, contexts ...context.Context) (provider.Config, bool) {
	t.Helper()
	config, ok, err := manager.ActiveConfig(testContext(contexts))
	if err != nil {
		t.Fatal(err)
	}
	return config, ok
}

func mustCircuitStatus(t *testing.T, manager *profiles.Manager, id string, contexts ...context.Context) (profiles.CircuitStatus, bool) {
	t.Helper()
	status, ok, err := manager.CircuitStatus(testContext(contexts), id)
	if err != nil {
		t.Fatal(err)
	}
	return status, ok
}

func TestManagerConcurrentOwnerIsolation(t *testing.T) {
	database, _ := openVaultStore(t)
	manager, err := profiles.NewManager(context.Background(), database, profiles.WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	inputs := map[context.Context]profiles.Profile{
		ctxA: {ID: "same-profile", Name: "A", BaseURL: "https://a.example/v1", APIKey: "secret-a", Model: "model-a", Stream: true},
		ctxB: {ID: "same-profile", Name: "B", BaseURL: "https://b.example/v1", APIKey: "secret-b", Model: "model-b", Stream: true},
	}
	for ctx, profile := range inputs {
		if _, err := manager.Save(ctx, profile); err != nil {
			t.Fatal(err)
		}
	}

	var wait sync.WaitGroup
	for ctx, saved := range inputs {
		ctx, saved := ctx, saved
		for worker := 0; worker < 4; worker++ {
			wait.Add(1)
			go func() {
				defer wait.Done()
				saved := saved
				for round := 0; round < 12; round++ {
					saved.APIKey = profiles.MaskedAPIKey
					if _, err := manager.Save(ctx, saved); err != nil {
						t.Errorf("%s save: %v", ipc.OwnerID(ctx), err)
						return
					}
					client, err := manager.ClientFor(ctx, saved.ID)
					if err != nil {
						t.Errorf("%s client: %v", ipc.OwnerID(ctx), err)
						return
					}
					config := client.Config()
					if config.APIKey != map[string]string{"model-a": "secret-a", "model-b": "secret-b"}[saved.Model] || config.Model != saved.Model || config.BaseURL != saved.BaseURL {
						t.Errorf("%s received foreign config: %+v", ipc.OwnerID(ctx), config)
						return
					}
					overview, err := manager.Overview(ctx)
					if err != nil || len(overview.Profiles) != 1 || overview.Profiles[0].Model != saved.Model {
						t.Errorf("%s overview = %+v, %v", ipc.OwnerID(ctx), overview, err)
						return
					}
				}
			}()
		}
	}
	wait.Wait()
	if _, err := manager.Overview(context.Background()); err == nil {
		t.Fatal("ownerless server request read shared profiles")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
}

func TestManagerCircuitsAreOwnerScoped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(`{"error":{"message":"injected"}}`))
	}))
	t.Cleanup(server.Close)
	manager, err := profiles.NewManager(context.Background(), openStore(t), profiles.WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	for _, item := range []struct {
		ctx       context.Context
		threshold int
	}{{ctxA, 1}, {ctxB, 5}} {
		if _, err := manager.Save(item.ctx, profiles.Profile{
			ID: "same-profile", BaseURL: server.URL, Model: "m", CircuitFailureThreshold: intPointer(item.threshold),
		}); err != nil {
			t.Fatal(err)
		}
	}
	clientA, err := manager.ActiveClient(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clientA.ChatBlock(context.Background(), provider.ChatRequest{}); err == nil {
		t.Fatal("failing provider succeeded")
	}
	statusA, ok := mustCircuitStatus(t, manager, "same-profile", ctxA)
	if !ok || statusA.OpenUntil == nil {
		t.Fatalf("owner A circuit = %+v, %v", statusA, ok)
	}
	if _, ok := mustCircuitStatus(t, manager, "same-profile", ctxB); ok {
		t.Fatal("owner A opened owner B's circuit")
	}
	clientB, err := manager.ActiveClient(ctxB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clientB.ChatBlock(context.Background(), provider.ChatRequest{}); err == nil {
		t.Fatal("failing provider succeeded")
	}
	statusB, ok := mustCircuitStatus(t, manager, "same-profile", ctxB)
	if !ok || statusB.ConsecutiveFailures == 0 || statusB.OpenUntil != nil {
		t.Fatalf("owner B circuit = %+v, %v", statusB, ok)
	}
}

func TestMaskedProfileSaveCannotUseOtherOwnerKey(t *testing.T) {
	var authorization atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization.Store(request.Header.Get("Authorization"))
		_, _ = writer.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(server.Close)
	database, _ := openVaultStore(t)
	manager, err := profiles.NewManager(context.Background(), database, profiles.WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	if _, err := manager.Save(ctxA, profiles.Profile{ID: "same-profile", BaseURL: "https://owner-a.invalid/v1", APIKey: "secret-a", Model: "model-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Save(ctxB, profiles.Profile{ID: "same-profile", BaseURL: server.URL, APIKey: profiles.MaskedAPIKey, Model: "model-b"}); err != nil {
		t.Fatal(err)
	}
	clientB, err := manager.ActiveClient(ctxB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clientB.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := authorization.Load().(string); got == "Bearer secret-a" {
		t.Fatal("masked save sent owner A's key to owner B's endpoint")
	} else if got != "" {
		t.Fatalf("authorization = %q, want no inherited key", got)
	}
	clientA, err := manager.ActiveClient(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	if config := clientA.Config(); config.APIKey != "secret-a" || config.BaseURL != "https://owner-a.invalid/v1" {
		t.Fatalf("owner A config changed: %+v", config)
	}
}
