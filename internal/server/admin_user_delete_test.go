package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// writeOwnedBlob places a staged or persisted entry owned by ownerID, as Stage does, and returns its directory.
func writeOwnedBlob(t *testing.T, root, ownerID string) string {
	t.Helper()
	dir := filepath.Join(root, ids.New())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta, err := json.Marshal(blobMeta{OwnerID: ownerID})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, blobMetaName), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.bin"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAdminUserDeletePurgesAccountAndKeepsAdminAudit(t *testing.T) {
	fixture := newAuthAuditFixture(t)
	admin, _ := fixture.initSuperadmin(t, "operator-1", "operator-password1")
	adminID := admin.user["id"].(string)
	fixture.createUser(t, "bob", "bob-password1")
	bob := fixture.login(t, "bob", "bob-password1")
	bobID := bob.user["id"].(string)

	purged := make(chan string, 4)
	fixture.server.accountPurge = func(_ context.Context, ownerID string) error {
		purged <- ownerID
		return nil
	}
	if err := fixture.database.GORM().Create(&store.SnippetRow{ID: ids.New(), Name: "bob", Body: "uptime", OwnerID: bobID}).Error; err != nil {
		t.Fatal(err)
	}
	blobs := fixture.server.blobStore
	if blobs == nil {
		t.Fatal("blob store not mounted")
	}
	bobStaged := writeOwnedBlob(t, blobs.stageRoot(), bobID)
	bobKept := writeOwnedBlob(t, blobs.keepRoot(), bobID)
	adminStaged := writeOwnedBlob(t, blobs.stageRoot(), adminID)
	ownerless := writeOwnedBlob(t, blobs.keepRoot(), "")

	if call := fixture.call(t, http.MethodDelete, "/admin/users/"+adminID, nil, admin, admin.csrf, nil); call.status != http.StatusBadRequest {
		t.Fatalf("self delete status=%d body=%v", call.status, call.body)
	}
	if call := fixture.call(t, http.MethodDelete, "/admin/users/"+ids.New(), nil, admin, admin.csrf, nil); call.status != http.StatusNotFound {
		t.Fatalf("unknown delete status=%d body=%v", call.status, call.body)
	}
	if call := fixture.call(t, http.MethodDelete, "/admin/users/"+adminID, nil, bob, bob.csrf, nil); call.status != http.StatusForbidden {
		t.Fatalf("non-admin delete status=%d body=%v", call.status, call.body)
	}
	if countKind(fixture.rows(t), "admin_delete_user") != 0 {
		t.Fatal("a refused delete was audited")
	}

	call := fixture.call(t, http.MethodDelete, "/admin/users/"+bobID, nil, admin, admin.csrf, nil)
	if call.status != http.StatusOK || call.body["ok"] != true {
		t.Fatalf("delete status=%d body=%v", call.status, call.body)
	}
	if warnings, _ := call.body["warnings"].([]any); len(warnings) != 0 {
		t.Fatalf("clean delete reported warnings: %v", warnings)
	}
	select {
	case owner := <-purged:
		if owner != bobID {
			t.Fatalf("live state purge ran for %q, want %q", owner, bobID)
		}
	default:
		t.Fatal("live state purge did not run")
	}

	if call := fixture.call(t, http.MethodGet, "/auth/me", nil, bob, "", nil); call.status != http.StatusUnauthorized {
		t.Fatalf("deleted user's session status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "bob", "password": "bob-password1"}, nil, "", nil); call.status == http.StatusOK {
		t.Fatal("deleted user can still sign in")
	}
	list := fixture.call(t, http.MethodGet, "/admin/users", nil, admin, "", nil)
	if users, _ := list.body["users"].([]any); len(users) != 1 {
		t.Fatalf("users after delete = %v", list.body)
	}

	expectAudit(t, fixture.only(t, "admin_delete_user"), adminID, map[string]any{"target_user_id": bobID})
	for _, row := range fixture.rows(t) {
		if row.owner == bobID {
			t.Fatalf("audit row owned by the deleted user survived: %+v", row)
		}
	}
	var snippets int64
	if err := fixture.database.GORM().Model(&store.SnippetRow{}).Where("owner_id = ?", bobID).Count(&snippets).Error; err != nil || snippets != 0 {
		t.Fatalf("deleted user's snippets = %d, %v", snippets, err)
	}

	for _, dir := range []string{bobStaged, bobKept} {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("deleted user's blob %s remains: %v", dir, err)
		}
	}
	for _, dir := range []string{adminStaged, ownerless} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("other blob %s was removed: %v", dir, err)
		}
	}

	if call := fixture.call(t, http.MethodDelete, "/admin/users/"+bobID, nil, admin, admin.csrf, nil); call.status != http.StatusNotFound {
		t.Fatalf("repeated delete status=%d", call.status)
	}
	if got := countKind(fixture.rows(t), "admin_delete_user"); got != 1 {
		t.Fatalf("admin_delete_user rows = %d, want 1", got)
	}
}

func TestAdminUserDeleteReportsIncompleteLiveCleanup(t *testing.T) {
	fixture := newAuthAuditFixture(t)
	admin, _ := fixture.initSuperadmin(t, "operator-1", "operator-password1")
	fixture.createUser(t, "carol", "carol-password1")
	carol := fixture.login(t, "carol", "carol-password1")
	fixture.server.accountPurge = func(context.Context, string) error {
		return errors.New("session busy")
	}
	call := fixture.call(t, http.MethodDelete, "/admin/users/"+carol.user["id"].(string), nil, admin, admin.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("delete status=%d body=%v", call.status, call.body)
	}
	if warnings, _ := call.body["warnings"].([]any); len(warnings) != 1 {
		t.Fatalf("warnings = %v, want the unfinished live cleanup", call.body["warnings"])
	}
	if _, err := fixture.accounts.GetUser(context.Background(), carol.user["id"].(string)); err == nil {
		t.Fatal("account survived a delete whose live cleanup failed")
	}
}

func TestBlobStorePurgeOwnerIgnoresEmptyOwner(t *testing.T) {
	blobs := NewBlobStore(t.TempDir(), testLogger())
	ownerless := writeOwnedBlob(t, blobs.stageRoot(), "")
	if removed, err := blobs.PurgeOwner(""); err != nil || removed != 0 {
		t.Fatalf("empty owner purge removed=%d err=%v", removed, err)
	}
	if _, err := os.Stat(ownerless); err != nil {
		t.Fatalf("ownerless entry removed: %v", err)
	}
}

func TestAdminUserDeleteRunsAccountCleanupAroundTheDisable(t *testing.T) {
	fixture := newAuthAuditFixture(t)
	admin, _ := fixture.initSuperadmin(t, "operator-1", "operator-password1")
	fixture.createUser(t, "dana", "dana-password1")
	dana, err := fixture.accounts.GetUserByUsername(context.Background(), "dana")
	if err != nil {
		t.Fatal(err)
	}
	stateOf := func(id string) string {
		var states []string
		if err := fixture.database.GORM().Model(&store.AppUser{}).Where("id = ?", id).Pluck("state", &states).Error; err != nil || len(states) != 1 {
			return "missing"
		}
		return states[0]
	}
	events := make(chan string, 16)
	fixture.server.accountCleanupPrepare = func(_ context.Context, ownerID, cleanupID string) (func(...string), func(), error) {
		events <- "prepare:" + stateOf(ownerID)
		return func(...string) { events <- "commit:" + stateOf(ownerID) }, func() { events <- "rollback" }, nil
	}
	fixture.server.accountPurge = func(_ context.Context, ownerID string) error {
		events <- "purge:" + stateOf(ownerID)
		return nil
	}
	drain := func() []string {
		var got []string
		for {
			select {
			case event := <-events:
				got = append(got, event)
			default:
				return got
			}
		}
	}

	if call := fixture.call(t, http.MethodDelete, "/admin/users/"+ids.New(), nil, admin, admin.csrf, nil); call.status != http.StatusNotFound {
		t.Fatalf("unknown delete status=%d", call.status)
	}
	if got := drain(); len(got) != 2 || got[0] != "prepare:missing" || got[1] != "rollback" {
		t.Fatalf("unknown delete cleanup events = %v, want prepare then rollback", got)
	}
	if call := fixture.call(t, http.MethodDelete, "/admin/users/"+dana.ID, nil, admin, admin.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("delete status=%d body=%v", call.status, call.body)
	}
	want := []string{"prepare:active", "commit:disabled", "purge:disabled"}
	if got := drain(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("delete cleanup events = %v, want %v", got, want)
	}
	if stateOf(dana.ID) != "missing" {
		t.Fatal("account row survived the delete")
	}
}

func TestAdminUserDeleteFailedPurgeLeavesAccountDisabledAndAudited(t *testing.T) {
	fixture := newAuthAuditFixture(t)
	admin, _ := fixture.initSuperadmin(t, "operator-1", "operator-password1")
	adminID := admin.user["id"].(string)
	fixture.createUser(t, "erin", "erin-password1")
	erin, err := fixture.accounts.GetUserByUsername(context.Background(), "erin")
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.GORM().Migrator().DropTable(&store.SnippetRow{}); err != nil {
		t.Fatal(err)
	}
	call := fixture.call(t, http.MethodDelete, "/admin/users/"+erin.ID, nil, admin, admin.csrf, nil)
	if call.status < 500 {
		t.Fatalf("delete with a failing purge status=%d body=%v", call.status, call.body)
	}
	user, err := fixture.accounts.GetUser(context.Background(), erin.ID)
	if err != nil || user.State != "disabled" {
		t.Fatalf("account after a failed purge = %+v, %v; want it kept and disabled", user, err)
	}
	if call := fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "erin", "password": "erin-password1"}, nil, "", nil); call.status == http.StatusOK {
		t.Fatal("a half-deleted account can still sign in")
	}
	expectAudit(t, fixture.only(t, "admin_delete_user_incomplete"), adminID, map[string]any{"target_user_id": erin.ID})
	if countKind(fixture.rows(t), "admin_delete_user") != 0 {
		t.Fatal("a failed delete was audited as completed")
	}
}
