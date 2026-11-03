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

func snippetIDs(t *testing.T, database *store.Store, ctx context.Context) []string {
	t.Helper()
	rows, err := database.SnippetList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row.ID
	}
	return out
}

// The ID a template converts to is decided once per owner and then reused: a collision with another owner
// moves the owner to a fresh ID, remembered so the migration and the sync conversion agree.
func TestRunbookTemplateSnippetIDMappingSurvivesCrossOwnerCollisionDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctxA := execBatchDatabaseContext("owner-a")
	ctxB := execBatchDatabaseContext("owner-b")
	ctx := context.Background()
	tpl := ids.New()
	derived := store.LegacyRunbookSnippetID(tpl)
	if err := database.GORM().Create(&store.SnippetRow{ID: derived, Name: "foreign", Body: "echo a", OwnerID: "owner-a", CreatedAt: 1, UpdatedAt: 1}).Error; err != nil {
		t.Fatal(err)
	}
	insertLegacyTemplate(t, database, "owner-b", tpl, "mine", 10, 20, store.RunbookTemplateStep{ID: "s", Name: "s", Command: "echo b"})
	if err := database.MigrateRunbookTemplatesForTest(ctx); err != nil {
		t.Fatal(err)
	}
	migrated := snippetIDs(t, database, ctxB)
	if len(migrated) != 1 || migrated[0] == derived {
		t.Fatalf("owner-b snippets = %v", migrated)
	}
	// The sync conversion of the same template resolves to the migrated snippet, not to a second one.
	resolved, err := database.ResolveRunbookTemplateSnippetID(ctxB, tpl)
	if err != nil || resolved != migrated[0] {
		t.Fatalf("resolved = %q (%v), want %q", resolved, err, migrated[0])
	}
	if looked, found, err := database.LookupRunbookTemplateSnippetID(ctxB, tpl); err != nil || !found || looked != migrated[0] {
		t.Fatalf("lookup = %q %v %v", looked, found, err)
	}
	// Owner-a keeps the derived ID; owner-b's mapping does not leak.
	if resolvedA, err := database.ResolveRunbookTemplateSnippetID(ctxA, tpl); err != nil || resolvedA != derived {
		t.Fatalf("owner-a resolved = %q (%v)", resolvedA, err)
	}
	// A template of another owner that nobody collides with converts to the derived ID, without a mapping.
	free := ids.New()
	if got, err := database.ResolveRunbookTemplateSnippetID(ctxB, free); err != nil || got != store.LegacyRunbookSnippetID(free) {
		t.Fatalf("free template resolved = %q (%v)", got, err)
	}
	// Lookup never allocates: a collided template without a mapping has no snippet.
	other := ids.New()
	if err := database.GORM().Create(&store.SnippetRow{ID: store.LegacyRunbookSnippetID(other), Name: "foreign", Body: "x", OwnerID: "owner-a", CreatedAt: 1, UpdatedAt: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if _, found, err := database.LookupRunbookTemplateSnippetID(ctxB, other); err != nil || found {
		t.Fatalf("lookup of a collided unmapped template: found=%v err=%v", found, err)
	}
	// Re-running the migration and a re-created template keep a single snippet.
	insertLegacyTemplate(t, database, "owner-b", tpl, "mine", 10, 20, store.RunbookTemplateStep{ID: "s", Name: "s", Command: "echo b"})
	if err := database.MigrateRunbookTemplatesForTest(ctx); err != nil {
		t.Fatal(err)
	}
	if again := snippetIDs(t, database, ctxB); len(again) != 1 || again[0] != migrated[0] {
		t.Fatalf("after rerun owner-b snippets = %v", again)
	}
}

// An insert that loses the ID to another owner between the check and the write moves to a fresh ID; an ID
// the owner already holds counts as migrated.
func TestRunbookTemplateSnippetInsertCollisionFallsBackToNewIDDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctxB := execBatchDatabaseContext("owner-b")
	ctx := context.Background()
	tpl := ids.New()
	derived := store.LegacyRunbookSnippetID(tpl)
	if err := database.GORM().Create(&store.SnippetRow{ID: derived, Name: "foreign", Body: "echo a", OwnerID: "owner-a", CreatedAt: 1, UpdatedAt: 1}).Error; err != nil {
		t.Fatal(err)
	}
	row := store.SnippetRow{ID: derived, OwnerID: "owner-b", Name: "mine", Body: "echo b", CreatedAt: 2, UpdatedAt: 3}
	placed, err := database.PlaceRunbookTemplateSnippetForTest(ctx, "owner-b", tpl, row)
	if err != nil || placed == derived || !ids.Valid(placed) {
		t.Fatalf("placed = %q (%v)", placed, err)
	}
	if got, err := database.ResolveRunbookTemplateSnippetID(ctxB, tpl); err != nil || got != placed {
		t.Fatalf("mapping = %q (%v), want %q", got, err, placed)
	}
	// Placing again lands on the same snippet instead of creating another.
	row.ID = placed
	again, err := database.PlaceRunbookTemplateSnippetForTest(ctx, "owner-b", tpl, row)
	if err != nil || again != placed || len(snippetIDs(t, database, ctxB)) != 1 {
		t.Fatalf("second placement = %q (%v), snippets %v", again, err, snippetIDs(t, database, ctxB))
	}
}

// Templates of one owner migrate in name order, which fixes the sort the snippets receive.
func TestRunbookTemplateMigrationOrdersByOwnerNameThenIDDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := execBatchDatabaseContext("owner-a")
	step := store.RunbookTemplateStep{ID: "s", Name: "s", Command: "echo"}
	idZ, idA := "0Z"+ids.New()[2:], "0A"+ids.New()[2:]
	insertLegacyTemplate(t, database, "owner-a", idZ, "alpha", 1, 1, step)
	insertLegacyTemplate(t, database, "owner-a", idA, "bravo", 1, 1, step)
	if err := database.MigrateRunbookTemplatesForTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := database.SnippetList(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("snippets = %+v %v", rows, err)
	}
	byName := map[string]int64{}
	for _, row := range rows {
		byName[row.Name] = row.Sort
	}
	if byName["alpha"] >= byName["bravo"] {
		t.Fatalf("sorts by name = %v, want alpha before bravo regardless of id order", byName)
	}
}

func TestSnippetMigrationStatusCountsOwnersLeftoverTemplatesDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctxA := execBatchDatabaseContext("owner-a")
	ctxB := execBatchDatabaseContext("owner-b")
	step := store.RunbookTemplateStep{ID: "s", Name: "s", Command: "echo"}
	insertLegacyTemplate(t, database, "owner-a", ids.New(), "ok", 1, 1, step)
	// An empty template cannot become a snippet and stays behind.
	insertLegacyTemplate(t, database, "owner-a", ids.New(), "empty", 1, 1)
	insertLegacyTemplate(t, database, "owner-b", ids.New(), "other", 1, 1, step)
	if err := database.MigrateRunbookTemplatesForTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count, err := database.RunbookTemplateUnmigratedCount(ctxA); err != nil || count != 1 {
		t.Fatalf("owner-a leftovers = %d (%v)", count, err)
	}
	if count, err := database.RunbookTemplateUnmigratedCount(ctxB); err != nil || count != 0 {
		t.Fatalf("owner-b leftovers = %d (%v)", count, err)
	}
}
