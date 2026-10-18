package sync

import (
	"context"
	"strings"
	"testing"
)

func pushGroupDependencyObject(t *testing.T, server *testSyncServer, userID string, dek []byte, id, kind string, payload any) {
	t.Helper()
	plaintext, err := marshalObject(payload)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := sealObject(dek, plaintext, id, kind)
	if err != nil {
		t.Fatal(err)
	}
	objects := &objectStore{db: server.db.GORM()}
	head, err := objects.currentHead(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := objects.push(context.Background(), userID, head, []WireObject{{ID: id, Blob: blob}}); err != nil {
		t.Fatal(err)
	}
}

func TestEnginePushesParentBeforeLexicallyEarlierChild(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	parentID, childID := strings.Repeat("Z", 26), strings.Repeat("0", 26)
	putDeviceGroup(t, deviceA, parentID, nil, "parent", 100)
	putDeviceGroup(t, deviceA, childID, &parentID, "child", 100)

	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	child, err := deviceB.db.GroupGet(context.Background(), childID)
	if err != nil || child.ParentID == nil || *child.ParentID != parentID {
		t.Fatalf("child hierarchy after one sync = %+v, %v", child, err)
	}
}

func TestEngineRecoversGroupParentAcrossSyncRoundsWithoutPushingOrphan(t *testing.T) {
	server := newTestSyncServer(t)
	dek := server.createUser(t, "alice", "alice-pw-123")
	user, err := server.accounts.GetUserByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	device := newTestDevice(t)
	parentID, childID := strings.Repeat("Z", 26), strings.Repeat("0", 26)
	childPayload := groupObject{ID: childID, ParentID: &parentID, Name: "child", CreatedAt: 1, UpdatedAt: 100}
	pushGroupDependencyObject(t, server, user.ID, dek, childID, KindGroup, childPayload)

	syncDevice(t, device, server, "alice", "alice-pw-123")
	child, err := device.db.GroupGet(context.Background(), childID)
	if err != nil || child.ParentID != nil {
		t.Fatalf("temporary orphan = %+v, %v", child, err)
	}
	if _, _, found, err := device.engine.syncStateGet(context.Background(), user.ID, childID); err != nil || found {
		t.Fatalf("temporary orphan marked reconciled: found=%v err=%v", found, err)
	}
	syncDevice(t, device, server, "alice", "alice-pw-123")
	if _, _, found, err := device.engine.syncStateGet(context.Background(), user.ID, childID); err != nil || found {
		t.Fatalf("missing parent stopped retrying: found=%v err=%v", found, err)
	}

	pushGroupDependencyObject(t, server, user.ID, dek, parentID, KindGroup,
		groupObject{ID: parentID, Name: "parent", CreatedAt: 1, UpdatedAt: 100})
	syncDevice(t, device, server, "alice", "alice-pw-123")
	syncDevice(t, device, server, "alice", "alice-pw-123")
	child, err = device.db.GroupGet(context.Background(), childID)
	if err != nil || child.ParentID == nil || *child.ParentID != parentID {
		t.Fatalf("child parent not restored = %+v, %v", child, err)
	}
	if report := syncDevice(t, device, server, "alice", "alice-pw-123"); report.Pushed != 0 {
		t.Fatalf("restored hierarchy did not converge: %+v", report)
	}
}

func TestEngineDeletedParentLeavesOrphanVisibleWithoutRevival(t *testing.T) {
	server := newTestSyncServer(t)
	dek := server.createUser(t, "alice", "alice-pw-123")
	user, err := server.accounts.GetUserByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	device := newTestDevice(t)
	parentID, childID := strings.Repeat("Z", 26), strings.Repeat("0", 26)
	pushGroupDependencyObject(t, server, user.ID, dek, childID, KindGroup,
		groupObject{ID: childID, ParentID: &parentID, Name: "orphan", CreatedAt: 1, UpdatedAt: 100})
	pushGroupDependencyObject(t, server, user.ID, dek, parentID, KindTombstone,
		tombstoneObject{TargetKind: KindGroup, DeletedAt: 200})

	syncDevice(t, device, server, "alice", "alice-pw-123")
	syncDevice(t, device, server, "alice", "alice-pw-123")
	child, err := device.db.GroupGet(context.Background(), childID)
	if err != nil || child.ParentID != nil {
		t.Fatalf("orphan after parent tombstone = %+v, %v", child, err)
	}
	if _, err := device.db.GroupGet(context.Background(), parentID); !isNotFound(err) {
		t.Fatalf("deleted parent was revived: %v", err)
	}
	assertDeviceTombstone(t, device, parentID, KindGroup)
	if report := syncDevice(t, device, server, "alice", "alice-pw-123"); report.Pushed != 0 {
		t.Fatalf("deleted-parent orphan did not converge: %+v", report)
	}
}
