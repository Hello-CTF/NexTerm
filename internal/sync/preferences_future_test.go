package sync

import (
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

// A grant stamped far in the future must be refused so a later revocation tombstone cannot be outranked.
func TestApplyAIGrantRejectsFutureRevision(t *testing.T) {
	instance := newTestInstance(t, true)
	future := time.Now().Add(24 * time.Hour).UnixMilli()
	objects := []ApplyObject{
		{ID: "device/dev-1", Kind: KindAIGrant, Payload: applyPayload(t, aiGrantDeviceObject{ID: "device/dev-1", DeviceID: "dev-1", Kinds: []string{"terminal_write"}, UpdatedAt: future})},
		{ID: "rule/r-1", Kind: KindAIGrant, Payload: applyPayload(t, aiGrantRuleObject{ID: "rule/r-1", DeviceID: "dev-1", Action: "write_file", Path: "/x/**", CreatedAt: 100, UpdatedAt: future})},
		{ID: "mode/dev-1", Kind: KindAIGrant, Payload: applyPayload(t, aiGrantModeObject{ID: "mode/dev-1", DeviceID: "dev-1", Mode: string(guard.Silent), UpdatedAt: future})},
	}
	for _, object := range objects {
		entry := mustApplyObjects(t, instance, object).Objects[0]
		if entry.Result != ApplyResultSkipped || entry.Warning == "" {
			t.Fatalf("future object %s = %+v, want skipped with warning", object.ID, entry)
		}
	}
	for _, key := range []string{guard.DeviceGrantsSettingKey, guard.GrantRulesSettingKey, guard.DevicePermissionSettingKey} {
		if _, _, found := readPreferenceSetting(t, &testDevice{db: instance.db}, key); found {
			t.Fatalf("future-dated payload wrote %s", key)
		}
	}
}
