package app

import (
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

// TestStoreRetentionPolicyCommandFields 命令日志保留策略: 单独策略字段直接生效,
// 默认 (未配置) 不开启任何清理。
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

// TestStoreRetentionPolicyCommandFollowsTranscript commandFollowTranscript
// 开启时命令保留镜像 transcript retention (含其默认值), 默认不开启同步。
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
