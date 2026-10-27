package sync

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

// Each opt-in switch must persist, read back and gate its own kinds independently of the others.
func TestKindOptInSwitchesAreIndependent(t *testing.T) {
	instance := newTestInstance(t, false)
	cases := []struct {
		name  string
		patch KindOptInPatch
		off   func(KindOptIn) bool
		kind  string
	}{
		{"knownHost", KindOptInPatch{KnownHost: boolPtr(false)}, func(v KindOptIn) bool { return !v.KnownHost }, KindKnownHost},
		{"aiProfile", KindOptInPatch{AIProfile: boolPtr(false)}, func(v KindOptIn) bool { return !v.AIProfile }, KindAIProfile},
		{"aiPermission", KindOptInPatch{AIPermission: boolPtr(false)}, func(v KindOptIn) bool { return !v.AIPermission }, KindAIPermission},
		{"aiGrant", KindOptInPatch{AIGrant: boolPtr(false)}, func(v KindOptIn) bool { return !v.AIGrant }, KindAIGrant},
		{"preferences", KindOptInPatch{Preferences: boolPtr(false)}, func(v KindOptIn) bool { return !v.Preferences }, KindPreference},
	}
	gated := []string{KindKnownHost, KindAIProfile, KindAIPermission, KindAIGrant, KindPreference}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			userID := "u-" + tc.name
			ctx := WithUserID(context.Background(), userID)
			view, err := instance.service.KindOptInSet(ctx, tc.patch)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.off(view) {
				t.Fatalf("switch not disabled: %+v", view)
			}
			disabled := 0
			for _, flag := range []bool{view.KnownHost, view.AIProfile, view.AIPermission, view.AIGrant, view.Preferences} {
				if !flag {
					disabled++
				}
			}
			if disabled != 1 {
				t.Fatalf("other switches must stay enabled: %+v", view)
			}
			optIn, err := instance.service.engine.loadKindOptIn(ctx, userID)
			if err != nil {
				t.Fatal(err)
			}
			for _, kind := range gated {
				want := kind != tc.kind
				if got := optIn.allowsKind(kind); got != want {
					t.Fatalf("allowsKind(%s)=%v, want %v", kind, got, want)
				}
				tombstone := applyPayload(t, tombstoneObject{TargetKind: kind, DeletedAt: 1})
				if got := optIn.allowsRemote(KindTombstone, tombstone); got != want {
					t.Fatalf("allowsRemote(tombstone->%s)=%v, want %v", kind, got, want)
				}
			}
			for _, kind := range []string{KindGroup, KindAsset, KindCredential, KindSnippet, KindTranscript, KindTombstone} {
				if !optIn.allowsKind(kind) {
					t.Fatalf("always-on kind %s gated", kind)
				}
			}
		})
	}
}

// The opt-in key is stored with reconciled cursors; its encoding must not change for existing switches.
func TestKindOptInKeyEncoding(t *testing.T) {
	if got := (kindOptIn{}).key(); got != "optin:00000" {
		t.Fatalf("zero key=%q", got)
	}
	if got := (kindOptIn{optInKnownHost: true, optInAIGrant: true}).key(); got != "optin:10010" {
		t.Fatalf("key=%q", got)
	}
	if got := allKindOptIn().key(); got != "optin:11111" {
		t.Fatalf("all key=%q", got)
	}
}

// Unsupported kinds and tombstone targets are skipped per object with a warning; the rest of the batch still applies.
func TestApplyObjectsIsolatesUnsupportedKindsAndTargets(t *testing.T) {
	instance := newTestInstance(t, false)
	groupID := ids.New()
	assetID := ids.New()
	result := mustApplyObjects(t, instance,
		ApplyObject{ID: ids.New(), Kind: "unknown_kind", Payload: applyPayload(t, map[string]string{"id": "x"})},
		ApplyObject{ID: assetID, Kind: KindTombstone, Payload: applyPayload(t, tombstoneObject{TargetKind: KindAsset, DeletedAt: 10})},
		applyGroupObject(t, groupID, "group", 100),
	)
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	requireApplyResult(t, result.Objects[1], ApplyResultSkipped)
	requireApplyResult(t, result.Objects[2], ApplyResultApplied)
	for _, entry := range result.Objects[:2] {
		if entry.Warning == "" {
			t.Fatalf("skipped object %s must carry a warning", entry.ID)
		}
	}
	if result.Applied != 1 || result.Skipped != 2 {
		t.Fatalf("result=%+v", result)
	}
}

// Push order follows kind dependencies: groups first, then credentials, snippets, assets,
// opt-in settings kinds, tombstones, and session records last.
func TestSortObjectsForPushKindOrder(t *testing.T) {
	objects := map[string]localObject{
		"a-transcript": {kind: KindTranscript},
		"b-tombstone":  {kind: KindTombstone},
		"c-preference": {kind: KindPreference},
		"d-grant":      {kind: KindAIGrant},
		"e-permission": {kind: KindAIPermission},
		"f-profile":    {kind: KindAIProfile},
		"g-knownhost":  {kind: KindKnownHost},
		"h-asset":      {kind: KindAsset},
		"i-snippet":    {kind: KindSnippet},
		"j-credential": {kind: KindCredential},
		"k-group":      {kind: KindGroup, plaintext: []byte(`{}`)},
		"l-unknown":    {kind: "unknown_kind"},
	}
	want := []string{"k-group", "j-credential", "i-snippet", "h-asset", "g-knownhost", "f-profile",
		"e-permission", "d-grant", "c-preference", "b-tombstone", "a-transcript", "l-unknown"}
	got := sortObjectsForPush(objects)
	if len(got) != len(want) {
		t.Fatalf("order=%v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("order=%v, want %v", got, want)
		}
	}
}
