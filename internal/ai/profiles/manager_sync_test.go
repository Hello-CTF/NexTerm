package profiles_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func seedProfile(t *testing.T, manager *profiles.Manager, name string) string {
	t.Helper()
	overview, err := manager.Save(context.Background(), profiles.Profile{Name: name, BaseURL: "https://api.example.com/v1", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range overview.Profiles {
		if profile.Name == name {
			return profile.ID
		}
	}
	t.Fatalf("seed profile %q not found: %+v", name, overview.Profiles)
	return ""
}

func profileTombstone(t *testing.T, database *store.Store, id string) (kind string, found bool) {
	t.Helper()
	var got string
	err := database.DB().QueryRowContext(context.Background(),
		"SELECT kind FROM sync_tombstone WHERE id = ?", id).Scan(&got)
	if err == nil {
		return got, true
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	t.Fatal(err)
	return "", false
}

func TestDeleteWithoutOptInWritesNoTombstone(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "u-a")
	database := openStore(t)
	if err := database.SetAIProfileSyncOptIn(ctx, "u-a", false); err != nil {
		t.Fatal(err)
	}
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	id := seedProfile(t, manager, "off")
	if _, err := manager.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, found := profileTombstone(t, database, id); found {
		t.Fatal("opt-in 关闭时删除档案不得产生墓碑")
	}
}

func TestDeleteWithOptInWritesUserTombstone(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "u-a")
	database := openStore(t)
	if err := database.SetAIProfileSyncOptIn(context.Background(), "u-a", true); err != nil {
		t.Fatal(err)
	}
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	id := seedProfile(t, manager, "on")
	if _, err := manager.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	kind, found := profileTombstone(t, database, id)
	if !found || kind != store.SyncTombstoneKindAIProfile {
		t.Fatalf("tombstone = %q %v", kind, found)
	}
}

func TestDeleteWithoutIdentityWritesNoTombstone(t *testing.T) {
	database := openStore(t)
	// Even with some user having opted in, the identity-less delete path (desktop direct / anonymous) writes no tombstone
	if err := database.SetAIProfileSyncOptIn(context.Background(), "u-a", true); err != nil {
		t.Fatal(err)
	}
	manager, err := profiles.NewManager(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	id := seedProfile(t, manager, "anon")
	if _, err := manager.Delete(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, found := profileTombstone(t, database, id); found {
		t.Fatal("无身份删除不得产生墓碑")
	}
}

func TestDeleteOptInIsolatedPerUser(t *testing.T) {
	database := openStore(t)
	if err := database.SetAIProfileSyncOptIn(context.Background(), "u-a", true); err != nil {
		t.Fatal(err)
	}
	manager, err := profiles.NewManager(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	id := seedProfile(t, manager, "isolated")
	// A on, B off: B's delete follows B's own opt-in (off), so no tombstone
	ctxB := ipc.WithUserID(context.Background(), "u-b")
	if err := database.SetAIProfileSyncOptIn(ctxB, "u-b", false); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Delete(ctxB, id); err != nil {
		t.Fatal(err)
	}
	if _, found := profileTombstone(t, database, id); found {
		t.Fatal("B 未开启 opt-in, 删除不得产生墓碑")
	}
}

// fakeSyncSettings implements profiles.Settings + opt-in reads + tombstone recording, for the record-failure path.
type fakeSyncSettings struct {
	values    map[string]string
	recordErr error
}

func (f *fakeSyncSettings) SettingGet(_ context.Context, key string) (string, bool, error) {
	value, ok := f.values[key]
	return value, ok, nil
}

func (f *fakeSyncSettings) SettingSet(_ context.Context, key, value string) error {
	f.values[key] = value
	return nil
}

func (f *fakeSyncSettings) AIProfileSyncOptIn(_ context.Context, userID string) (bool, error) {
	return f.values["sync.optin.ai_profile."+userID] == "1", nil
}

func (f *fakeSyncSettings) SyncTombstoneRecord(_ context.Context, _, _ string, _ int64) error {
	return f.recordErr
}

// atomicSyncSettings implements the atomic delete interface on top of fakeSyncSettings (same shape as the production store's AIProfilesDeleteTx).
type atomicSyncSettings struct {
	*fakeSyncSettings
	deleteErr error
	recorded  []string
}

func (f *atomicSyncSettings) AIProfilesDeleteTx(_ context.Context, stateJSON string, tombstoneID string, _ int64) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.values[profiles.SettingKey] = stateJSON
	f.recorded = append(f.recorded, tombstoneID)
	return nil
}

func TestDeletePropagatesTombstoneRecordFailure(t *testing.T) {
	settings := &fakeSyncSettings{values: map[string]string{"sync.optin.ai_profile.u-a": "1"}, recordErr: errors.New("disk full")}
	manager, err := profiles.NewManager(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	id := seedProfile(t, manager, "fail")
	ctx := ipc.WithUserID(context.Background(), "u-a")
	if _, err := manager.Delete(ctx, id); err == nil {
		t.Fatal("墓碑记录失败必须向调用方返回错误, 不得静默吞掉")
	}
}

// M165 R2: atomic delete: the first tombstone write failure rolls back as a whole (profile remains); after recovery the delete is retried and the tombstone recorded, converging.
func TestDeleteAtomicRetryAfterFailureConverges(t *testing.T) {
	settings := &atomicSyncSettings{
		fakeSyncSettings: &fakeSyncSettings{values: map[string]string{"sync.optin.ai_profile.u-a": "1"}},
		deleteErr:        errors.New("tombstone write failed"),
	}
	manager, err := profiles.NewManager(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	id := seedProfile(t, manager, "retry")
	ctx := ipc.WithUserID(context.Background(), "u-a")

	if _, err := manager.Delete(ctx, id); err == nil {
		t.Fatal("首次删除必须返回墓碑写失败")
	}
	if _, ok := manager.Profile(id); !ok {
		t.Fatal("原子删除失败时档案必须仍在(可重试收敛)")
	}
	if len(settings.recorded) != 0 {
		t.Fatal("失败不得留下墓碑记录")
	}

	settings.deleteErr = nil
	if _, err := manager.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Profile(id); ok {
		t.Fatal("重试后档案必须已删除")
	}
	if len(settings.recorded) != 1 || settings.recorded[0] != id {
		t.Fatalf("重试必须补记墓碑: %v", settings.recorded)
	}
}
