package app

import (
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

// TestStoreRetentionPolicyCommandFields: command log retention policy - the separate policy fields take effect directly,
// and by default (unconfigured) no cleanup is enabled.
func TestStoreRetentionPolicyCommandFields(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	source := StoreRetentionPolicy(database)

	policy, err := source.RetentionPolicy(ctx)
	if err != nil || policy.CommandMaxAge != 0 || policy.CommandMaxCount != 0 {
		t.Fatalf("default command policy = %+v, %v; want retention off", policy, err)
	}

	if err := database.SettingSet(ctx, RetentionSettingKey, `{"commandMaxAgeMs":60000,"commandMaxCount":7}`); err != nil {
		t.Fatal(err)
	}
	policy, err = source.RetentionPolicy(ctx)
	if err != nil || policy.CommandMaxAge != time.Minute || policy.CommandMaxCount != 7 {
		t.Fatalf("separate command policy = %+v, %v", policy, err)
	}

	for _, raw := range []string{`{"commandMaxAgeMs":-1}`, `{"commandMaxCount":-1}`} {
		if err := database.SettingSet(ctx, RetentionSettingKey, raw); err != nil {
			t.Fatal(err)
		}
		if _, err := source.RetentionPolicy(ctx); err == nil {
			t.Fatalf("expected invalid command setting rejection: %s", raw)
		}
	}
}

// TestStoreRetentionPolicyCommandFollowsTranscript: with commandFollowTranscript enabled, command retention mirrors
// transcript retention (including its defaults), and syncing is off by default.
func TestStoreRetentionPolicyCommandFollowsTranscript(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	source := StoreRetentionPolicy(database)

	if err := database.SettingSet(ctx, RetentionSettingKey, `{"commandFollowTranscript":true,"commandMaxAgeMs":60000,"commandMaxCount":7}`); err != nil {
		t.Fatal(err)
	}
	policy, err := source.RetentionPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if policy.CommandMaxAge != TranscriptDefaultRetentionMaxAge || policy.CommandMaxCount != TranscriptDefaultRetentionMaxCount {
		t.Fatalf("follow with unset transcript setting = %+v, want transcript defaults", policy)
	}

	if err := database.SettingSet(ctx, TranscriptRetentionSettingKey, `{"maxAgeMs":120000,"maxCount":9}`); err != nil {
		t.Fatal(err)
	}
	policy, err = source.RetentionPolicy(ctx)
	if err != nil || policy.CommandMaxAge != 2*time.Minute || policy.CommandMaxCount != 9 {
		t.Fatalf("follow with transcript setting = %+v, %v", policy, err)
	}

	if err := database.SettingSet(ctx, TranscriptRetentionSettingKey, `{invalid`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.RetentionPolicy(ctx); err == nil {
		t.Fatal("invalid transcript setting must surface through follow mode")
	}
}
