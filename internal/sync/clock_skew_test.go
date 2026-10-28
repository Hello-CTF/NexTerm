package sync

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

func TestApplyObjectsSkipsRevisionsBeyondClockTolerance(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	groupID := ids.New()
	farFuture := time.Now().Add(48 * time.Hour).UnixMilli()

	objects := []ApplyObject{
		applyGroupObject(t, groupID, "来自未来", farFuture),
		{ID: "tomb-1", Kind: KindTombstone, Payload: applyPayload(t, tombstoneObject{TargetKind: KindGroup, DeletedAt: farFuture})},
		{ID: "pref/theme", Kind: KindPreference, Payload: applyPayload(t, preferenceObject{ID: "pref/theme", Key: "theme", Value: []byte(`"dark"`), UpdatedAt: farFuture})},
	}
	for _, object := range objects {
		entry := mustApplyObjects(t, instance, object).Objects[0]
		requireApplyResult(t, entry, ApplyResultSkipped)
		if !strings.Contains(entry.Warning, "24 小时") || !strings.Contains(entry.Warning, "时间") {
			t.Fatalf("object %s warning = %q, want a clock-skew warning", object.ID, entry.Warning)
		}
	}
	groups, err := instance.db.GroupList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Fatalf("future-dated group was applied: %+v", groups)
	}
}

func TestApplyObjectsAcceptsRevisionsWithinClockTolerance(t *testing.T) {
	instance := newTestInstance(t, false)
	soon := time.Now().Add(time.Hour).UnixMilli()
	entry := mustApplyObjects(t, instance, applyGroupObject(t, ids.New(), "小幅超前", soon)).Objects[0]
	requireApplyResult(t, entry, ApplyResultApplied)
}

func TestFutureTombstoneDoesNotDeleteLocalGroup(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	groupID := ids.New()
	if _, err := instance.db.GroupUpsert(ctx, groupID, nil, "本地分组", 0, 1, 200); err != nil {
		t.Fatal(err)
	}
	farFuture := time.Now().Add(72 * time.Hour).UnixMilli()
	entry := mustApplyObjects(t, instance, ApplyObject{ID: groupID, Kind: KindTombstone, Payload: applyPayload(t, tombstoneObject{TargetKind: KindGroup, DeletedAt: farFuture})}).Objects[0]
	requireApplyResult(t, entry, ApplyResultSkipped)
	groups, err := instance.db.GroupList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].ID != groupID {
		t.Fatalf("local group must survive a far-future tombstone, got %+v", groups)
	}
}
