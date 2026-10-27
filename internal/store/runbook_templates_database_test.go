package store_test

import (
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestRunbookTemplateCRUDOwnerIsolationAndSnapshotDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctxA := execBatchDatabaseContext("owner-a")
	ctxB := execBatchDatabaseContext("owner-b")

	snippet, err := database.SnippetCreate(ctxA, "restart", "systemctl restart web", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	created, err := database.RunbookTemplateCreate(ctxA, store.RunbookTemplateInput{
		Name: "  Restart  ", Description: "d",
		Steps: []store.RunbookTemplateStep{
			{ID: "s1", SnippetID: snippet.ID, Name: "from snippet"},
			{ID: "s2", Name: "inline", Command: "uptime"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "Restart" || created.Version != 1 || created.CreatedAt == 0 || created.UpdatedAt == 0 ||
		created.Steps[0].Command != "systemctl restart web" || created.Steps[0].SnippetID != snippet.ID {
		t.Fatalf("created = %+v", created)
	}

	// Editing the snippet later must not change the saved snapshot.
	if err := database.SnippetUpdate(ctxA, snippet.ID, "restart", "reboot"); err != nil {
		t.Fatal(err)
	}
	loaded, err := database.RunbookTemplateGet(ctxA, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Steps[0].Command != "systemctl restart web" || len(loaded.Steps) != 2 {
		t.Fatalf("loaded = %+v", loaded)
	}

	// Other owners neither see nor mutate it.
	list, err := database.RunbookTemplateList(ctxB)
	if err != nil || len(list) != 0 {
		t.Fatalf("owner-b list = %+v, %v", list, err)
	}
	var appErr *ipc.Error
	if _, err := database.RunbookTemplateGet(ctxB, created.ID); !errors.As(err, &appErr) || appErr.Code != ipc.CodeNotFound {
		t.Fatalf("cross-owner get = %v", err)
	}
	if err := database.RunbookTemplateDelete(ctxB, created.ID); !errors.As(err, &appErr) || appErr.Code != ipc.CodeNotFound {
		t.Fatalf("cross-owner delete = %v", err)
	}

	updated, err := database.RunbookTemplateUpdate(ctxA, created.ID, store.RunbookTemplateInput{
		Name: "Restart 2", Steps: []store.RunbookTemplateStep{{ID: "s1", Name: "x", Command: "true"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Name != "Restart 2" || updated.CreatedAt != created.CreatedAt || updated.UpdatedAt < created.UpdatedAt || len(updated.Steps) != 1 {
		t.Fatalf("updated = %+v", updated)
	}

	if _, err := database.RunbookTemplateCreate(ctxA, store.RunbookTemplateInput{Name: "bad"}); !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam {
		t.Fatalf("empty steps error = %v", err)
	}
	if _, err := database.RunbookTemplateCreate(ctxA, store.RunbookTemplateInput{
		Name: "dup", Steps: []store.RunbookTemplateStep{{ID: "a", Command: "x"}, {ID: "a", Command: "y"}},
	}); !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam {
		t.Fatalf("duplicate step error = %v", err)
	}

	if _, err := database.RunbookTemplateCreate(ctxB, store.RunbookTemplateInput{
		Name: "b", Steps: []store.RunbookTemplateStep{{ID: "a", Command: "x"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.RunbookTemplateDeleteOwner(t.Context(), "owner-a"); err != nil {
		t.Fatal(err)
	}
	if list, _ := database.RunbookTemplateList(ctxA); len(list) != 0 {
		t.Fatalf("owner-a templates after owner delete = %+v", list)
	}
	if list, _ := database.RunbookTemplateList(ctxB); len(list) != 1 {
		t.Fatalf("owner-b templates after owner-a delete = %+v", list)
	}
}
