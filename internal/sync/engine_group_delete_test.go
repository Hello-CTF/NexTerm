package sync

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestEngineParentGroupDeletionCannotReviveDescendants(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	deviceC := newTestDevice(t)
	ctx := context.Background()

	rootID, childID, grandchildID, assetID := ids.New(), ids.New(), ids.New(), ids.New()
	putDeviceGroup(t, deviceA, rootID, nil, "root", 100)
	putDeviceGroup(t, deviceA, childID, &rootID, "child", 100)
	putDeviceGroup(t, deviceA, grandchildID, &childID, "grandchild", 100)
	putDeviceAsset(t, deviceA, store.AssetRow{
		ID: assetID, GroupID: &grandchildID, Kind: "ssh", Name: "host", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100,
	})
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	if err := deviceA.db.GroupDelete(ctx, rootID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{rootID, childID, grandchildID} {
		assertDeviceTombstone(t, deviceA, id, KindGroup)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	for _, id := range []string{rootID, childID, grandchildID} {
		assertServerObjectKind(t, server, "alice", "alice-pw-123", id, KindTombstone)
	}
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	syncDevice(t, deviceC, server, "alice", "alice-pw-123")
	for _, device := range []*testDevice{deviceA, deviceB, deviceC} {
		groups, err := device.db.GroupList(ctx)
		if err != nil || len(groups) != 0 {
			t.Fatalf("deleted groups reappeared: %+v, %v", groups, err)
		}
		asset, err := device.db.AssetGet(ctx, assetID)
		if err != nil || asset.GroupID != nil || asset.DeletedAt != nil {
			t.Fatalf("asset was lost or remained grouped: %+v, %v", asset, err)
		}
	}
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if groups, err := deviceB.db.GroupList(ctx); err != nil || len(groups) != 0 {
		t.Fatalf("child revived after another sync: %+v, %v", groups, err)
	}
}
