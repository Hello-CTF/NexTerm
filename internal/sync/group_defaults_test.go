package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func setDeviceGroupDefaults(t *testing.T, device *testDevice, groupID string, defaults store.GroupDefaults) {
	t.Helper()
	if _, err := device.db.GroupUpdate(context.Background(), groupID, store.GroupPatch{Defaults: &defaults}); err != nil {
		t.Fatal(err)
	}
}

func TestEngineSyncsGroupConnectionDefaults(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newMasterTestDevice(t)
	deviceB := newMasterTestDevice(t)
	ctx := context.Background()
	groupID, credID, jumpID := strings.Repeat("G", 26), strings.Repeat("C", 26), strings.Repeat("J", 26)
	putDeviceCredential(t, deviceA, credID, "group-password", "s3cret", 100)
	host := "bastion.example"
	putDeviceAsset(t, deviceA, store.AssetRow{ID: jumpID, Kind: "ssh", Name: "bastion", Host: &host, OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100})
	putDeviceGroup(t, deviceA, groupID, nil, "生产环境", 100)
	setDeviceGroupDefaults(t, deviceA, groupID, store.GroupDefaults{Username: "deploy", AuthKind: "password", CredID: credID, JumpAssetID: jumpID})

	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	// The credential travels in the same round as the group, so the reference may need a second round to settle.
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	got, err := deviceB.db.GroupGet(ctx, groupID)
	if err != nil {
		t.Fatal(err)
	}
	if deref(got.DefaultUsername) != "deploy" || deref(got.DefaultAuthKind) != "password" || deref(got.DefaultCredID) != credID || deref(got.DefaultJumpAssetID) != jumpID {
		t.Fatalf("group defaults on the second device = %+v", got)
	}
	// Both sides converge: nothing is left to push.
	if report := syncDevice(t, deviceB, server, "alice", "alice-pw-123"); report.Pushed != 0 {
		t.Fatalf("defaults did not converge: %+v", report)
	}
	if report := syncDevice(t, deviceA, server, "alice", "alice-pw-123"); report.Pushed != 0 {
		t.Fatalf("origin kept pushing: %+v", report)
	}

	// Clearing a default on one device clears it on the other.
	setDeviceGroupDefaults(t, deviceB, groupID, store.GroupDefaults{Username: "deploy"})
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	got, err = deviceA.db.GroupGet(ctx, groupID)
	if err != nil || got.DefaultCredID != nil || got.DefaultAuthKind != nil || got.DefaultJumpAssetID != nil || deref(got.DefaultUsername) != "deploy" {
		t.Fatalf("cleared defaults did not sync back: %+v, %v", got, err)
	}
}

func TestEngineRestoresGroupDefaultCredentialArrivingLater(t *testing.T) {
	server := newTestSyncServer(t)
	dek := server.createUser(t, "alice", "alice-pw-123")
	user, err := server.accounts.GetUserByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	device := newMasterTestDevice(t)
	groupID, credID := strings.Repeat("G", 26), strings.Repeat("C", 26)
	missing := credID
	pushGroupDependencyObject(t, server, user.ID, dek, groupID, KindGroup, groupObject{
		ID: groupID, Name: "later", CreatedAt: 1, UpdatedAt: 100,
		DefaultUsername: strPtr("deploy"), DefaultAuthKind: strPtr("password"), DefaultCredID: &missing,
	})

	syncDevice(t, device, server, "alice", "alice-pw-123")
	got, err := device.db.GroupGet(context.Background(), groupID)
	if err != nil || got.DefaultCredID != nil || deref(got.DefaultUsername) != "deploy" {
		t.Fatalf("group before its credential arrives = %+v, %v", got, err)
	}
	// The degraded form is not pushed back as a user edit.
	if report := syncDevice(t, device, server, "alice", "alice-pw-123"); report.Pushed != 0 {
		t.Fatalf("degraded group was pushed: %+v", report)
	}
	putDeviceCredential(t, device, credID, "arrived", "s3cret", 50)
	syncDevice(t, device, server, "alice", "alice-pw-123")
	syncDevice(t, device, server, "alice", "alice-pw-123")
	got, err = device.db.GroupGet(context.Background(), groupID)
	if err != nil || deref(got.DefaultCredID) != credID {
		t.Fatalf("group default credential was not restored: %+v, %v", got, err)
	}
}

// A peer that predates group defaults drops the fields. Its echo at the same revision must not erase them;
// a genuinely newer edit from such a peer does, which is the documented limit.
func TestEngineOldPeerGroupPayloadHandling(t *testing.T) {
	server := newTestSyncServer(t)
	dek := server.createUser(t, "alice", "alice-pw-123")
	user, err := server.accounts.GetUserByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	device := newMasterTestDevice(t)
	ctx := context.Background()
	groupID := strings.Repeat("G", 26)
	putDeviceGroup(t, device, groupID, nil, "旧版对等端", 100)
	setDeviceGroupDefaults(t, device, groupID, store.GroupDefaults{Username: "deploy", AuthKind: "agent"})
	local, err := device.db.GroupGet(ctx, groupID)
	if err != nil {
		t.Fatal(err)
	}

	pushGroupDependencyObject(t, server, user.ID, dek, groupID, KindGroup, groupObject{
		ID: groupID, Name: local.Name, Sort: local.Sort, CreatedAt: local.CreatedAt, UpdatedAt: local.UpdatedAt,
	})
	syncDevice(t, device, server, "alice", "alice-pw-123")
	got, err := device.db.GroupGet(ctx, groupID)
	if err != nil || deref(got.DefaultUsername) != "deploy" || deref(got.DefaultAuthKind) != "agent" {
		t.Fatalf("an old peer's same-revision echo erased the defaults: %+v, %v", got, err)
	}

	pushGroupDependencyObject(t, server, user.ID, dek, groupID, KindGroup, groupObject{
		ID: groupID, Name: "旧版改名", Sort: local.Sort, CreatedAt: local.CreatedAt, UpdatedAt: local.UpdatedAt + 1000,
	})
	syncDevice(t, device, server, "alice", "alice-pw-123")
	got, err = device.db.GroupGet(ctx, groupID)
	if err != nil || got.Name != "旧版改名" || got.DefaultUsername != nil {
		t.Fatalf("a newer edit from an old peer should replace the group, as for any field it does not know: %+v, %v", got, err)
	}
}

func TestBundleCarriesGroupDefaultsDatabase(t *testing.T) {
	source := newDatabaseTestInstance(t, true)
	target := newDatabaseTestInstance(t, true)
	ctx := context.Background()
	credID := ids.New()
	putTestCredential(t, source, credID, "bundle-cred", "password", "s3cret")
	group, err := source.db.GroupCreate(ctx, store.GroupInput{Name: "导出组"})
	if err != nil {
		t.Fatal(err)
	}
	host := "bastion.example"
	jump, err := source.db.AssetCreate(ctx, store.AssetInput{GroupID: &group.ID, Kind: "ssh", Name: "bastion", Host: &host, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	member, err := source.db.AssetCreate(ctx, store.AssetInput{GroupID: &group.ID, Kind: "ssh", Name: "member", Host: &host, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.db.GroupUpdate(ctx, group.ID, store.GroupPatch{Defaults: &store.GroupDefaults{
		Username: "deploy", AuthKind: "password", CredID: credID, JumpAssetID: jump.ID,
	}}); err != nil {
		t.Fatal(err)
	}

	t.Run("with credentials", func(t *testing.T) {
		bundle, err := source.service.ExportBundle(ctx, ExportBundleRequest{AssetIDs: []string{jump.ID, member.ID}, WithCreds: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(bundle.Groups) != 1 || deref(bundle.Groups[0].DefaultCredID) != credID || deref(bundle.Groups[0].DefaultJumpAssetID) != jump.ID {
			t.Fatalf("bundle groups = %+v", bundle.Groups)
		}
		if _, err := target.service.ImportBundle(ctx, ImportBundleRequest{Bundle: bundle}); err != nil {
			t.Fatal(err)
		}
		got, err := target.db.GroupGet(ctx, group.ID)
		if err != nil {
			t.Fatal(err)
		}
		if deref(got.DefaultUsername) != "deploy" || deref(got.DefaultCredID) != credID || deref(got.DefaultJumpAssetID) != jump.ID {
			t.Fatalf("imported group = %+v", got)
		}
	})

	t.Run("without credentials or the jump asset", func(t *testing.T) {
		bundle, err := source.service.ExportBundle(ctx, ExportBundleRequest{AssetIDs: []string{member.ID}})
		if err != nil {
			t.Fatal(err)
		}
		if bundle.Groups[0].DefaultCredID != nil || bundle.Groups[0].DefaultJumpAssetID != nil || deref(bundle.Groups[0].DefaultUsername) != "deploy" {
			t.Fatalf("unexportable references travelled: %+v", bundle.Groups[0])
		}
		if len(bundle.Warnings) == 0 {
			t.Fatal("omitted references are not reported")
		}
	})

	t.Run("an old bundle keeps local defaults", func(t *testing.T) {
		legacy := SyncBundle{Protocol: BundleProtocolVersion, Groups: []SyncBundleGroup{{ID: group.ID, Name: "导出组", CreatedAt: 1, UpdatedAt: 5000000000000}}}
		if _, err := target.service.ImportBundle(ctx, ImportBundleRequest{Bundle: legacy}); err != nil {
			t.Fatal(err)
		}
		got, err := target.db.GroupGet(ctx, group.ID)
		if err != nil || deref(got.DefaultUsername) != "deploy" {
			t.Fatalf("a bundle without defaults erased local defaults: %+v, %v", got, err)
		}
	})

	t.Run("a dangling reference is cleared with a warning", func(t *testing.T) {
		fresh := newDatabaseTestInstance(t, true)
		missing := ids.New()
		bundle := SyncBundle{Protocol: BundleProtocolVersion, Groups: []SyncBundleGroup{{
			ID: group.ID, Name: "导出组", CreatedAt: 1, UpdatedAt: 100,
			DefaultUsername: strPtr("deploy"), DefaultAuthKind: strPtr("password"), DefaultCredID: &missing, DefaultJumpAssetID: &missing,
		}}}
		report, err := fresh.service.ImportBundle(ctx, ImportBundleRequest{Bundle: bundle})
		if err != nil {
			t.Fatal(err)
		}
		got, err := fresh.db.GroupGet(ctx, group.ID)
		if err != nil || got.DefaultCredID != nil || got.DefaultJumpAssetID != nil || deref(got.DefaultUsername) != "deploy" {
			t.Fatalf("dangling references survived: %+v, %v", got, err)
		}
		if len(report.Warnings) < 2 {
			t.Fatalf("cleared references are not reported: %v", report.Warnings)
		}
	})
}

func TestBundleKeepsEmptyAuthKindOfInheritingAssetDatabase(t *testing.T) {
	source := newDatabaseTestInstance(t, true)
	target := newDatabaseTestInstance(t, true)
	ctx := context.Background()
	group, err := source.db.GroupCreate(ctx, store.GroupInput{Name: "g", Defaults: &store.GroupDefaults{Username: "u", AuthKind: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	host := "h.example"
	inheriting, err := source.db.AssetCreate(ctx, store.AssetInput{GroupID: &group.ID, Kind: "ssh", Name: "inherits", Host: &host, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	loose, err := source.db.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "ungrouped", Host: &host, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := source.service.ExportBundle(ctx, ExportBundleRequest{AssetIDs: []string{inheriting.ID, loose.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.service.ImportBundle(ctx, ImportBundleRequest{Bundle: bundle}); err != nil {
		t.Fatal(err)
	}
	got, err := target.db.AssetGet(ctx, inheriting.ID)
	if err != nil || deref(got.AuthKind) != "" {
		t.Fatalf("an inheriting asset gained its own authKind through the bundle: %+v, %v", got.AuthKind, err)
	}
	resolved, err := target.db.ResolveAsset(ctx, got)
	if err != nil || deref(resolved.AuthKind) != "agent" || deref(resolved.Username) != "u" {
		t.Fatalf("imported asset no longer inherits: %+v, %v", resolved.AssetRow, err)
	}
	// An ungrouped asset keeps the long-standing password default.
	other, err := target.db.AssetGet(ctx, loose.ID)
	if err != nil || deref(other.AuthKind) != "password" {
		t.Fatalf("ungrouped asset authKind = %v, %v", other.AuthKind, err)
	}
}
