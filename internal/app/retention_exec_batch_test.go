package app

import (
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestStoreRetentionPolicyExecBatchFields(t *testing.T) {
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	source := StoreRetentionPolicy(database)
	policy, err := source.RetentionPolicy(t.Context())
	if err != nil || retentionEnabled(policy) || policy.ExecBatchMaxAge != 0 || policy.ExecBatchMaxCount != 0 {
		t.Fatalf("default exec batch policy = %+v, %v", policy, err)
	}
	if err := database.SettingSet(t.Context(), RetentionSettingKey, `{"execBatchMaxAgeMs":60000,"execBatchMaxCount":7}`); err != nil {
		t.Fatal(err)
	}
	policy, err = source.RetentionPolicy(t.Context())
	if err != nil || !retentionEnabled(policy) || policy.ExecBatchMaxAge != time.Minute || policy.ExecBatchMaxCount != 7 {
		t.Fatalf("configured exec batch policy = %+v, %v", policy, err)
	}
	for _, raw := range []string{`{"execBatchMaxAgeMs":-1}`, `{"execBatchMaxCount":-1}`, `{"execBatchMaxAgeMs":9223372036854775807}`} {
		if err := database.SettingSet(t.Context(), RetentionSettingKey, raw); err != nil {
			t.Fatal(err)
		}
		if _, err := source.RetentionPolicy(t.Context()); err == nil {
			t.Fatalf("expected invalid exec batch setting rejection: %s", raw)
		}
	}
}
