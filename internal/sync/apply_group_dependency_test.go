package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

func applyNestedGroupObject(t *testing.T, id, parentID string, updatedAt int64) ApplyObject {
	t.Helper()
	return ApplyObject{ID: id, Kind: KindGroup, Payload: applyPayload(t, groupObject{
		ID: id, ParentID: &parentID, Name: "child", CreatedAt: 1, UpdatedAt: updatedAt,
	})}
}

func TestApplyObjectsOrdersParentBeforeChildWithinBatch(t *testing.T) {
	instance := newTestInstance(t, false)
	parentID, childID := ids.New(), ids.New()
	child := applyNestedGroupObject(t, childID, parentID, 100)
	parent := applyGroupObject(t, parentID, "parent", 100)

	result := mustApplyObjects(t, instance, child, parent)
	if result.Applied != 2 || result.Skipped != 0 {
		t.Fatalf("apply result = %+v", result)
	}
	if result.Objects[0].ID != childID || result.Objects[1].ID != parentID {
		t.Fatalf("result order changed: %+v", result.Objects)
	}
	for _, entry := range result.Objects {
		requireApplyResult(t, entry, ApplyResultApplied)
	}
	stored, err := instance.db.GroupGet(context.Background(), childID)
	if err != nil || stored.ParentID == nil || *stored.ParentID != parentID {
		t.Fatalf("child parent = %+v, %v", stored, err)
	}
}

func TestApplyObjectsDefersChildUntilParentArrives(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	parentID, childID := ids.New(), ids.New()
	child := applyNestedGroupObject(t, childID, parentID, 100)

	first := mustApplyObjects(t, instance, child)
	requireApplyResult(t, first.Objects[0], ApplyResultSkipped)
	if !strings.Contains(first.Objects[0].Warning, "尚未同步") {
		t.Fatalf("missing parent warning = %q", first.Objects[0].Warning)
	}
	if _, err := instance.db.GroupGet(ctx, childID); !isNotFound(err) {
		t.Fatalf("deferred child was imported before parent: %v", err)
	}

	mustApplyObjects(t, instance, applyGroupObject(t, parentID, "parent", 100))
	second := mustApplyObjects(t, instance, child)
	requireApplyResult(t, second.Objects[0], ApplyResultApplied)
	stored, err := instance.db.GroupGet(ctx, childID)
	if err != nil || stored.ParentID == nil || *stored.ParentID != parentID {
		t.Fatalf("child parent after retry = %+v, %v", stored, err)
	}
}

func TestApplyObjectsChildOfDeletedParent(t *testing.T) {
	for _, test := range []struct {
		name           string
		deletedAt      int64
		wantChildApply bool
	}{
		{name: "newer tombstone creates orphan", deletedAt: 200, wantChildApply: true},
		{name: "older tombstone waits for parent", deletedAt: 50},
	} {
		t.Run(test.name, func(t *testing.T) {
			instance := newTestInstance(t, false)
			ctx := context.Background()
			parentID, childID := ids.New(), ids.New()
			tombstone := ApplyObject{ID: parentID, Kind: KindTombstone, Payload: applyPayload(t, tombstoneObject{
				TargetKind: KindGroup, DeletedAt: test.deletedAt,
			})}
			mustApplyObjects(t, instance, tombstone)

			result := mustApplyObjects(t, instance, applyNestedGroupObject(t, childID, parentID, 100))
			if test.wantChildApply {
				requireApplyResult(t, result.Objects[0], ApplyResultApplied)
				stored, err := instance.db.GroupGet(ctx, childID)
				if err != nil || stored.ParentID != nil {
					t.Fatalf("deleted-parent orphan = %+v, %v", stored, err)
				}
			} else {
				requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
				if _, err := instance.db.GroupGet(ctx, childID); !isNotFound(err) {
					t.Fatalf("child imported under stale tombstone: %v", err)
				}
			}
			if _, found, err := instance.service.engine.syncTombstoneGet(ctx, parentID); err != nil || !found {
				t.Fatalf("parent tombstone missing: found=%v err=%v", found, err)
			}
		})
	}
}
