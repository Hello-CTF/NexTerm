package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func insertLegacyTemplate(t *testing.T, database *store.Store, owner, id, name string, createdAt, updatedAt int64, steps ...store.RunbookTemplateStep) {
	t.Helper()
	encoded, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	row := store.RunbookTemplateRow{
		OwnerID: owner, ID: id, Name: name, Description: "desc " + name,
		Steps: string(encoded), Version: 3, CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
	if err := database.GORM().Create(&row).Error; err != nil {
		t.Fatal(err)
	}
}

func templateCount(t *testing.T, database *store.Store) int64 {
	t.Helper()
	var count int64
	if err := database.GORM().Model(&store.RunbookTemplateRow{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

func TestLegacyRunbookSnippetIDIsStableAndValid(t *testing.T) {
	id := ids.New()
	first := store.LegacyRunbookSnippetID(id)
	if !ids.Valid(first) {
		t.Fatalf("derived id %q is not valid", first)
	}
	if first != store.LegacyRunbookSnippetID(id) {
		t.Fatal("derived id is not stable")
	}
	if first == id || first == store.LegacyRunbookSnippetID(ids.New()) {
		t.Fatal("derived id must depend on the template id")
	}
}

func TestRunbookTemplatesMigrateToMultiStepSnippetsDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctxA := execBatchDatabaseContext("owner-a")
	ctxB := execBatchDatabaseContext("owner-b")
	ctx := context.Background()

	existing, err := database.SnippetCreate(ctxA, "existing", "uptime", nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	tplA, tplB := ids.New(), ids.New()
	insertLegacyTemplate(t, database, "owner-a", tplA, "restart", 1111, 2222,
		store.RunbookTemplateStep{ID: "s1", SnippetID: existing.ID, Name: "stop", Description: "stop it", Command: "systemctl stop web"},
		store.RunbookTemplateStep{ID: "s2", Name: "start", Command: "systemctl start web", Interactive: true},
	)
	insertLegacyTemplate(t, database, "owner-b", tplB, "other", 3333, 4444,
		store.RunbookTemplateStep{ID: "x", Name: "x", Command: "echo b"},
	)

	if err := database.MigrateRunbookTemplatesForTest(ctx); err != nil {
		t.Fatal(err)
	}

	if templateCount(t, database) != 0 {
		t.Fatal("templates must be removed after migration")
	}
	snippetsA, err := database.SnippetList(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	if len(snippetsA) != 2 {
		t.Fatalf("owner-a snippets = %d, want 2", len(snippetsA))
	}
	var migrated store.SnippetRow
	for _, row := range snippetsA {
		if row.ID == store.LegacyRunbookSnippetID(tplA) {
			migrated = row
		}
	}
	if migrated.ID == "" {
		t.Fatal("migrated snippet with the derived id is missing")
	}
	if migrated.Name != "restart" || migrated.Description != "desc restart" || migrated.CreatedAt != 1111 || migrated.UpdatedAt != 2222 || migrated.GroupID != nil {
		t.Fatalf("migrated = %+v", migrated)
	}
	if migrated.Sort <= existing.Sort {
		t.Fatalf("migrated sort %d must follow existing root sort %d", migrated.Sort, existing.Sort)
	}
	if migrated.Body != "systemctl stop web\nsystemctl start web" {
		t.Fatalf("body = %q", migrated.Body)
	}
	steps, err := store.DecodeSnippetSteps(migrated.Steps)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.SnippetStep{
		{ID: "s1", Name: "stop", Description: "stop it", Command: "systemctl stop web"},
		{ID: "s2", Name: "start", Command: "systemctl start web", Interactive: true},
	}
	if len(steps) != 2 || steps[0] != want[0] || steps[1] != want[1] {
		t.Fatalf("steps = %+v", steps)
	}

	snippetsB, err := database.SnippetList(ctxB)
	if err != nil || len(snippetsB) != 1 || snippetsB[0].ID != store.LegacyRunbookSnippetID(tplB) || snippetsB[0].Sort != 0 {
		t.Fatalf("owner-b snippets = %+v, %v", snippetsB, err)
	}
	var tombstones int64
	if err := database.GORM().Table("sync_tombstone").Where("kind = ?", "runbook_template").Count(&tombstones).Error; err != nil || tombstones != 0 {
		t.Fatalf("runbook_template tombstones = %d, %v", tombstones, err)
	}

	// A second run is a no-op, and a template re-created later (same id) is
	// absorbed by the existing snippet instead of duplicating it.
	insertLegacyTemplate(t, database, "owner-a", tplA, "restart", 1111, 2222,
		store.RunbookTemplateStep{ID: "s1", Name: "stop", Command: "systemctl stop web"})
	for range 2 {
		if err := database.MigrateRunbookTemplatesForTest(ctx); err != nil {
			t.Fatal(err)
		}
	}
	again, err := database.SnippetList(ctxA)
	if err != nil || len(again) != 2 || templateCount(t, database) != 0 {
		t.Fatalf("after rerun: %d snippets, %d templates, %v", len(again), templateCount(t, database), err)
	}
}

func TestRunbookTemplateMigrationAvoidsCrossOwnerIDCollisionDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctxA := execBatchDatabaseContext("owner-a")
	ctxB := execBatchDatabaseContext("owner-b")
	tpl := ids.New()
	// owner-a already holds a snippet under the id owner-b's template derives to.
	if err := database.GORM().Create(&store.SnippetRow{
		ID: store.LegacyRunbookSnippetID(tpl), Name: "foreign", Body: "echo a", OwnerID: "owner-a", CreatedAt: 1, UpdatedAt: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	insertLegacyTemplate(t, database, "owner-b", tpl, "mine", 10, 20,
		store.RunbookTemplateStep{ID: "s", Name: "s", Command: "echo b"})
	if err := database.MigrateRunbookTemplatesForTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	listB, err := database.SnippetList(ctxB)
	if err != nil || len(listB) != 1 || listB[0].Name != "mine" || !ids.Valid(listB[0].ID) || listB[0].ID == store.LegacyRunbookSnippetID(tpl) {
		t.Fatalf("owner-b = %+v, %v", listB, err)
	}
	listA, err := database.SnippetList(ctxA)
	if err != nil || len(listA) != 1 || listA[0].Name != "foreign" {
		t.Fatalf("owner-a = %+v, %v", listA, err)
	}
}

func TestSnippetStepsContractDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := execBatchDatabaseContext("owner-a")
	steps := []store.SnippetStep{
		{ID: "a", Name: "one", Command: "echo 1", Confirm: true},
		{ID: "b", Name: "two", Command: "echo 2"},
	}
	created, err := database.SnippetCreateContent(ctx, "multi", store.SnippetContent{Description: "d", Body: "ignored", Steps: steps}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if created.Body != "echo 1\necho 2" || created.Description != "d" {
		t.Fatalf("created = %+v", created)
	}
	got, err := store.DecodeSnippetSteps(created.Steps)
	if err != nil || len(got) != 2 || got[0] != steps[0] || got[1] != steps[1] {
		t.Fatalf("steps = %+v, %v", got, err)
	}
	// An update without steps turns the snippet back into a single-step one.
	if err := database.SnippetUpdateFields(ctx, created.ID, store.SnippetPatch{Name: "multi", Body: "echo only"}); err != nil {
		t.Fatal(err)
	}
	plain, err := database.SnippetGet(ctx, created.ID)
	if err != nil || plain.Steps != "" || plain.Body != "echo only" || plain.Description != "" {
		t.Fatalf("plain = %+v, %v", plain, err)
	}
	for name, bad := range map[string][]store.SnippetStep{
		"empty command": {{ID: "a", Command: "  "}},
		"duplicate id":  {{ID: "a", Command: "x"}, {ID: "a", Command: "y"}},
		"padded id":     {{ID: " a", Command: "x"}},
		"long id":       {{ID: strings.Repeat("i", 97), Command: "x"}},
		"long name":     {{ID: "a", Name: strings.Repeat("n", 513), Command: "x"}},
		"too many":      make([]store.SnippetStep, 65),
	} {
		if err := store.ValidateSnippetSteps(bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := database.SnippetCreateContent(ctx, "bad", store.SnippetContent{Description: strings.Repeat("d", 4097)}, nil, 0); err == nil {
		t.Error("oversized description accepted")
	}
}
