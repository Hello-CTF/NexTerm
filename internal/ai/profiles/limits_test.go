package profiles_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/provider"
)

func TestProfileMaxTokensRoundTripThroughStore(t *testing.T) {
	ctx := context.Background()
	database := openStore(t)
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	overview, err := manager.Save(ctx, profiles.Profile{
		BaseURL: "https://example.test/v1", Model: "m", ContextWindow: 128_000, MaxTokens: intPointer(4_096),
	})
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	profile, ok := reloaded.Profile(overview.Profiles[0].ID)
	if !ok || profile.MaxTokens == nil || *profile.MaxTokens != 4_096 {
		t.Fatalf("reloaded profile = %+v ok=%v", profile, ok)
	}
	*profile.MaxTokens = 1
	again, _ := reloaded.Profile(overview.Profiles[0].ID)
	if *again.MaxTokens != 4_096 {
		t.Fatalf("manager state aliased: %d", *again.MaxTokens)
	}
}

func TestProfileMaxTokensClamped(t *testing.T) {
	profile := profiles.Profile{ContextWindow: 32_768, MaxTokens: intPointer(99_999)}.Normalized()
	if profile.MaxTokens == nil || *profile.MaxTokens != 16_384 {
		t.Fatalf("MaxTokens = %v, want 16384", profile.MaxTokens)
	}
	unset := profiles.Profile{}.Normalized()
	if unset.MaxTokens != nil {
		t.Fatalf("unset MaxTokens = %v", *unset.MaxTokens)
	}
}

func TestProfileMaxTokensAbsentInLegacyJSON(t *testing.T) {
	ctx := context.Background()
	database := openStore(t)
	raw := `{"version":1,"profiles":[{"id":"p1","baseUrl":"https://example.test/v1","model":"m","contextWindow":64000,"stream":true,"temperature":0.3}],"activeId":"p1"}`
	if err := database.SettingSet(ctx, profiles.SettingKey, raw); err != nil {
		t.Fatal(err)
	}
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	profile, ok := manager.Profile("p1")
	if !ok {
		t.Fatal("legacy profile not found")
	}
	if profile.MaxTokens != nil || profile.CircuitFailureThreshold != nil || profile.CircuitCooldownSeconds != nil {
		t.Fatalf("absent fields materialized: %+v", profile)
	}
	threshold, cooldown := profile.CircuitConfig()
	if threshold != provider.DefaultCircuitThreshold || cooldown != provider.DefaultCircuitCooldown {
		t.Fatalf("CircuitConfig() = %d, %v", threshold, cooldown)
	}
}

func TestProfileCircuitFieldsRoundTripAndClamp(t *testing.T) {
	ctx := context.Background()
	database := openStore(t)
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	overview, err := manager.Save(ctx, profiles.Profile{
		BaseURL: "https://example.test/v1", Model: "m",
		CircuitFailureThreshold: intPointer(3), CircuitCooldownSeconds: intPointer(120),
	})
	if err != nil {
		t.Fatal(err)
	}
	saved := overview.Profiles[0]
	if saved.CircuitFailureThreshold == nil || *saved.CircuitFailureThreshold != 3 ||
		saved.CircuitCooldownSeconds == nil || *saved.CircuitCooldownSeconds != 120 {
		t.Fatalf("saved circuit fields = %+v", saved)
	}
	reloaded, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	profile, ok := reloaded.Profile(saved.ID)
	if !ok || profile.CircuitFailureThreshold == nil || *profile.CircuitFailureThreshold != 3 ||
		profile.CircuitCooldownSeconds == nil || *profile.CircuitCooldownSeconds != 120 {
		t.Fatalf("reloaded profile = %+v ok=%v", profile, ok)
	}
	threshold, cooldown := profile.CircuitConfig()
	if threshold != 3 || cooldown != 120*time.Second {
		t.Fatalf("CircuitConfig() = %d, %v", threshold, cooldown)
	}
	clamped := profiles.Profile{
		CircuitFailureThreshold: intPointer(0), CircuitCooldownSeconds: intPointer(-9),
	}.Normalized()
	if clamped.CircuitFailureThreshold == nil || *clamped.CircuitFailureThreshold != 1 ||
		clamped.CircuitCooldownSeconds == nil || *clamped.CircuitCooldownSeconds != 1 {
		t.Fatalf("clamped circuit fields = %+v", clamped)
	}
}

func TestManagerCircuitBreakerSharedAcrossClients(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(`{"error":{"message":"injected"}}`))
	}))
	defer server.Close()
	ctx := context.Background()
	manager, err := profiles.NewManager(ctx, openStore(t))
	if err != nil {
		t.Fatal(err)
	}
	overview, err := manager.Save(ctx, profiles.Profile{
		BaseURL: server.URL, Model: "m",
		CircuitFailureThreshold: intPointer(1), CircuitCooldownSeconds: intPointer(3600),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := overview.Profiles[0].ID
	client, err := manager.ActiveClient()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ChatBlock(ctx, provider.ChatRequest{}); err == nil {
		t.Fatal("failing server succeeded")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
	status, ok := manager.CircuitStatus(id)
	if !ok || status.ConsecutiveFailures != 1 || status.OpenUntil == nil || !status.OpenUntil.After(time.Now()) {
		t.Fatalf("CircuitStatus() = %+v ok=%v", status, ok)
	}
	second, err := manager.ActiveClient()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.ChatBlock(ctx, provider.ChatRequest{}); !errors.Is(err, provider.ErrCircuitOpen) {
		t.Fatalf("second client error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("open circuit hit the server: calls = %d", calls.Load())
	}
	if _, ok := manager.CircuitStatus("missing"); ok {
		t.Fatal("missing profile reported circuit status")
	}
}
