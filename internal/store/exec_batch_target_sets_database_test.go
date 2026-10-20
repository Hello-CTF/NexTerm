package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func requireExecTargetSetBadParam(t *testing.T, err error) {
	t.Helper()
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam {
		t.Fatalf("error = %v, want bad param", err)
	}
}

func requireExecTargetSetMissing(t *testing.T, err error) {
	t.Helper()
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeNotFound {
		t.Fatalf("error = %v, want not found", err)
	}
}

func requireExecTargetSetItemCount(t *testing.T, database *store.Store, owner string, want int64) {
	t.Helper()
	var count int64
	query := database.GORM().Model(&store.ExecTargetSetItemRow{})
	if owner != "" {
		query = query.Where("owner_id = ?", owner)
	}
	if err := query.Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("exec target set item count owner %q = %d, want %d", owner, count, want)
	}
}

func TestExecTargetSetCRUDAndPickerDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctxA := execBatchDatabaseContext("owner-a")

	created, err := database.ExecTargetSetCreate(ctxA, "  web fleet  ", []string{"sess-b", "sess-a", "sess-c"})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Name != "web fleet" || created.CreatedAt == 0 || created.UpdatedAt == 0 {
		t.Fatalf("created = %+v", created)
	}
	if len(created.SessionIDs) != 3 || created.SessionIDs[0] != "sess-b" || created.SessionIDs[1] != "sess-a" || created.SessionIDs[2] != "sess-c" {
		t.Fatalf("created session order = %+v", created.SessionIDs)
	}

	loaded, err := database.ExecTargetSetGet(ctxA, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != created.Name || loaded.CreatedAt != created.CreatedAt || loaded.UpdatedAt != created.UpdatedAt ||
		strings.Join(loaded.SessionIDs, ",") != "sess-b,sess-a,sess-c" {
		t.Fatalf("loaded = %+v", loaded)
	}

	if err := database.ExecTargetSetUpdate(ctxA, created.ID, "db fleet", []string{"sess-d"}); err != nil {
		t.Fatal(err)
	}
	updated, err := database.ExecTargetSetGet(ctxA, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "db fleet" || len(updated.SessionIDs) != 1 || updated.SessionIDs[0] != "sess-d" {
		t.Fatalf("updated = %+v", updated)
	}
	if updated.CreatedAt != created.CreatedAt {
		t.Fatalf("update rewrote created_at: %+v", updated)
	}

	for _, name := range []string{"zeta", "alpha"} {
		if _, err := database.ExecTargetSetCreate(ctxA, name, []string{"sess-1"}); err != nil {
			t.Fatal(err)
		}
	}
	sets, err := database.ExecTargetSetList(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 3 || sets[0].Name != "alpha" || sets[1].Name != "db fleet" || sets[2].Name != "zeta" {
		t.Fatalf("picker list = %+v", sets)
	}
	if sets[1].ID != created.ID || len(sets[1].SessionIDs) != 1 || sets[1].SessionIDs[0] != "sess-d" {
		t.Fatalf("picker entry = %+v", sets[1])
	}

	if err := database.ExecTargetSetDelete(ctxA, created.ID); err != nil {
		t.Fatal(err)
	}
	requireExecTargetSetMissing(t, func() error {
		_, err := database.ExecTargetSetGet(ctxA, created.ID)
		return err
	}())
	requireExecTargetSetItemCount(t, database, "owner-a", 2)
	var tombstones int64
	if err := database.GORM().Model(&store.SyncTombstone{}).Where("owner_id = ?", "owner-a").Count(&tombstones).Error; err != nil {
		t.Fatal(err)
	}
	if tombstones != 0 {
		t.Fatalf("local-only target set delete wrote %d tombstones", tombstones)
	}
}

func TestExecTargetSetOwnerIsolationDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctxA := execBatchDatabaseContext("owner-a")
	ctxB := execBatchDatabaseContext("owner-b")
	ownerless := context.Background()

	setA, err := database.ExecTargetSetCreate(ctxA, "fleet", []string{"shared-session"})
	if err != nil {
		t.Fatal(err)
	}
	setB, err := database.ExecTargetSetCreate(ctxB, "fleet", []string{"shared-session"})
	if err != nil {
		t.Fatal(err)
	}
	setLocal, err := database.ExecTargetSetCreate(ownerless, "fleet", []string{"shared-session"})
	if err != nil {
		t.Fatal(err)
	}
	if setA.ID == setB.ID || setA.ID == setLocal.ID {
		t.Fatalf("expected independent generated ids: %q %q %q", setA.ID, setB.ID, setLocal.ID)
	}

	listA, err := database.ExecTargetSetList(ctxA)
	if err != nil || len(listA) != 1 || listA[0].ID != setA.ID {
		t.Fatalf("owner A list = %+v, %v", listA, err)
	}
	listB, err := database.ExecTargetSetList(ctxB)
	if err != nil || len(listB) != 1 || listB[0].ID != setB.ID {
		t.Fatalf("owner B list = %+v, %v", listB, err)
	}
	listLocal, err := database.ExecTargetSetList(ownerless)
	if err != nil || len(listLocal) != 1 || listLocal[0].ID != setLocal.ID {
		t.Fatalf("ownerless list = %+v, %v", listLocal, err)
	}

	requireExecTargetSetMissing(t, func() error {
		_, err := database.ExecTargetSetGet(ctxB, setA.ID)
		return err
	}())
	requireExecTargetSetMissing(t, func() error {
		_, err := database.ExecTargetSetGet(ownerless, setA.ID)
		return err
	}())
	requireExecTargetSetMissing(t, func() error {
		return database.ExecTargetSetUpdate(ctxB, setA.ID, "hijack", []string{"other"})
	}())
	requireExecTargetSetMissing(t, func() error {
		return database.ExecTargetSetDelete(ctxB, setA.ID)
	}())

	loadedA, err := database.ExecTargetSetGet(ctxA, setA.ID)
	if err != nil || loadedA.Name != "fleet" || len(loadedA.SessionIDs) != 1 {
		t.Fatalf("cross-owner write touched owner A row: %+v, %v", loadedA, err)
	}
	requireExecTargetSetItemCount(t, database, "", 3)
}

func TestExecTargetSetValidationDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := execBatchDatabaseContext("owner-a")

	_, err := database.ExecTargetSetCreate(ctx, "", []string{"sess"})
	requireExecTargetSetBadParam(t, err)
	_, err = database.ExecTargetSetCreate(ctx, "   ", []string{"sess"})
	requireExecTargetSetBadParam(t, err)
	_, err = database.ExecTargetSetCreate(ctx, strings.Repeat("n", store.ExecTargetSetMaxNameBytes+1), []string{"sess"})
	requireExecTargetSetBadParam(t, err)
	_, err = database.ExecTargetSetCreate(ctx, "fleet", nil)
	requireExecTargetSetBadParam(t, err)
	_, err = database.ExecTargetSetCreate(ctx, "fleet", []string{"sess", "sess"})
	requireExecTargetSetBadParam(t, err)
	_, err = database.ExecTargetSetCreate(ctx, "fleet", []string{" lead"})
	requireExecTargetSetBadParam(t, err)
	_, err = database.ExecTargetSetCreate(ctx, "fleet", []string{strings.Repeat("s", 192)})
	requireExecTargetSetBadParam(t, err)
	overflow := make([]string, store.ExecTargetSetMaxTargets+1)
	for index := range overflow {
		overflow[index] = strings.Repeat("s", index+1)
	}
	_, err = database.ExecTargetSetCreate(ctx, "fleet", overflow)
	requireExecTargetSetBadParam(t, err)

	atLimit := make([]string, store.ExecTargetSetMaxTargets)
	for index := range atLimit {
		atLimit[index] = strings.Repeat("s", index+1)
	}
	created, err := database.ExecTargetSetCreate(ctx, strings.Repeat("n", store.ExecTargetSetMaxNameBytes), atLimit)
	if err != nil {
		t.Fatalf("create at limit: %v", err)
	}
	if len(created.SessionIDs) != store.ExecTargetSetMaxTargets {
		t.Fatalf("created at limit keeps %d sessions", len(created.SessionIDs))
	}

	_, err = database.ExecTargetSetGet(ctx, "")
	requireExecTargetSetBadParam(t, err)
	_, err = database.ExecTargetSetGet(ctx, " lead")
	requireExecTargetSetBadParam(t, err)
	requireExecTargetSetMissing(t, func() error {
		_, err := database.ExecTargetSetGet(ctx, "missing")
		return err
	}())
	requireExecTargetSetMissing(t, func() error {
		return database.ExecTargetSetUpdate(ctx, "missing", "fleet", []string{"sess"})
	}())
	requireExecTargetSetMissing(t, func() error {
		return database.ExecTargetSetDelete(ctx, "missing")
	}())

	_, err = database.ExecTargetSetCreate(ctx, "fleet", []string{"sess"})
	if err != nil {
		t.Fatalf("duplicate names across sets must be allowed: %v", err)
	}
}
