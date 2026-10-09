package profiles_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	"github.com/cloudwego/eino/schema"
)

func intPointer(value int) *int { return &value }

func TestProfileTimeoutFieldsRoundTrip(t *testing.T) {
	ctx := context.Background()
	database := openStore(t)
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	overview, err := manager.Save(ctx, profiles.Profile{
		BaseURL: "https://example.test/v1", Model: "m",
		RequestTimeoutSeconds: intPointer(5), IdleTimeoutSeconds: intPointer(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	saved := overview.Profiles[0]
	if saved.RequestTimeoutSeconds == nil || *saved.RequestTimeoutSeconds != 5 {
		t.Fatalf("request timeout = %v", saved.RequestTimeoutSeconds)
	}
	if saved.IdleTimeoutSeconds == nil || *saved.IdleTimeoutSeconds != 0 {
		t.Fatalf("idle timeout = %v", saved.IdleTimeoutSeconds)
	}
	reloaded, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	profile, ok := reloaded.Profile(saved.ID)
	if !ok || profile.RequestTimeoutSeconds == nil || *profile.RequestTimeoutSeconds != 5 ||
		profile.IdleTimeoutSeconds == nil || *profile.IdleTimeoutSeconds != 0 {
		t.Fatalf("reloaded profile = %+v ok=%v", profile, ok)
	}
}

func TestProfileTimeoutNormalizedAndClamped(t *testing.T) {
	profile := profiles.Profile{
		RequestTimeoutSeconds: intPointer(99999), IdleTimeoutSeconds: intPointer(-3),
	}.Normalized()
	if profile.RequestTimeoutSeconds == nil || *profile.RequestTimeoutSeconds != 3600 {
		t.Fatalf("request timeout = %v", profile.RequestTimeoutSeconds)
	}
	if profile.IdleTimeoutSeconds == nil || *profile.IdleTimeoutSeconds != 0 {
		t.Fatalf("idle timeout = %v", profile.IdleTimeoutSeconds)
	}
	unset := profiles.Profile{}.Normalized()
	if unset.RequestTimeoutSeconds != nil || unset.IdleTimeoutSeconds != nil {
		t.Fatalf("unset timeouts = %+v", unset)
	}
	zero := profiles.Profile{RequestTimeoutSeconds: intPointer(0)}.Normalized()
	if zero.RequestTimeoutSeconds == nil || *zero.RequestTimeoutSeconds != 1 {
		t.Fatalf("zero request timeout = %v", zero.RequestTimeoutSeconds)
	}
}

func TestProfileLookupReturnsClone(t *testing.T) {
	ctx := context.Background()
	manager, err := profiles.NewManager(ctx, openStore(t))
	if err != nil {
		t.Fatal(err)
	}
	overview, err := manager.Save(ctx, profiles.Profile{
		BaseURL: "https://example.test/v1", Model: "m", RequestTimeoutSeconds: intPointer(7),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := overview.Profiles[0].ID
	profile, ok := manager.Profile(id)
	if !ok {
		t.Fatal("saved profile not found by id")
	}
	*profile.RequestTimeoutSeconds = 1
	again, _ := manager.Profile(id)
	if *again.RequestTimeoutSeconds != 7 {
		t.Fatalf("manager state aliased: %d", *again.RequestTimeoutSeconds)
	}
	if _, ok := manager.Profile("missing"); ok {
		t.Fatal("missing profile found")
	}
}

func waitForDisconnect(request *http.Request, release <-chan struct{}) {
	select {
	case <-request.Context().Done():
	case <-release:
	}
}

func TestActiveClientAppliesProfileTimeouts(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		waitForDisconnect(request, release)
	}))
	defer server.Close()
	defer close(release)
	ctx := context.Background()
	manager, err := profiles.NewManager(ctx, openStore(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Save(ctx, profiles.Profile{
		BaseURL: server.URL, Model: "m", RequestTimeoutSeconds: intPointer(1),
	}); err != nil {
		t.Fatal(err)
	}
	client, err := manager.ActiveClient()
	if err != nil {
		t.Fatal(err)
	}
	backstop, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	started := time.Now()
	if _, err := client.ChatBlock(backstop, provider.ChatRequest{Messages: []provider.ChatMessage{provider.UserMessage("ping")}}); err == nil {
		t.Fatal("stalling chat succeeded")
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("profile request timeout not applied, took %v", elapsed)
	}
}

func TestActiveClientAppliesProfileIdleTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n"))
		writer.(http.Flusher).Flush()
		waitForDisconnect(request, release)
	}))
	defer server.Close()
	defer close(release)
	ctx := context.Background()
	manager, err := profiles.NewManager(ctx, openStore(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Save(ctx, profiles.Profile{
		BaseURL: server.URL, Model: "m", Stream: true, IdleTimeoutSeconds: intPointer(1),
	}); err != nil {
		t.Fatal(err)
	}
	client, err := manager.ActiveClient()
	if err != nil {
		t.Fatal(err)
	}
	chatModel, err := client.ChatModel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	backstop, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	started := time.Now()
	reader, err := chatModel.Stream(backstop, []*schema.Message{schema.UserMessage("ping")})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var recvErr error
	for {
		if _, recvErr = reader.Recv(); recvErr != nil {
			break
		}
	}
	if !strings.Contains(recvErr.Error(), "stalled") {
		t.Fatalf("idle error = %v", recvErr)
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("profile idle timeout not applied, took %v", elapsed)
	}
}

func TestManagerConcurrentAccessRace(t *testing.T) {
	ctx := context.Background()
	manager, err := profiles.NewManager(ctx, openStore(t))
	if err != nil {
		t.Fatal(err)
	}
	seed, err := manager.Save(ctx, profiles.Profile{BaseURL: "https://example.test/v1", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	id := seed.Profiles[0].ID
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for round := 0; round < 25; round++ {
				switch (worker + round) % 5 {
				case 0:
					_, _ = manager.Save(ctx, profiles.Profile{BaseURL: "https://example.test/v1", Model: "m"})
				case 1:
					_, _ = manager.Activate(ctx, id)
				case 2:
					_ = manager.Overview()
				case 3:
					_, _ = manager.ActiveProfile()
					_, _ = manager.Profile(id)
				case 4:
					_, _ = manager.ActiveClient()
				}
			}
		}(worker)
	}
	wait.Wait()
}
