package store_test

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestConvOverviewDatabase(t *testing.T) {
	ctx := context.Background()
	database := dbtest.NewFixture(t).OpenStore(t)
	database.SetAIHistoryShared(false)
	gormDB := database.GORM().WithContext(ctx)
	finished := int64(9)
	conversations := []ConversationRow{
		{ID: "conv-web", Title: "Deploy Nginx", ScopeJSON: `{"scope":{"sessionId":"s1","assetId":"asset-web"}}`, CreatedAt: 10, UpdatedAt: 100, OwnerID: "owner-a"},
		{ID: "conv-db", Title: "数据库巡检", ScopeJSON: `{"scope":{"assetId":"asset-db"}}`, CreatedAt: 10, UpdatedAt: 200, OwnerID: "owner-a"},
		{ID: "conv-local", Title: "untitled", ScopeJSON: `{"scope":{"sessionId":"s2"}}`, CreatedAt: 10, UpdatedAt: 300, OwnerID: "owner-a"},
		{ID: "conv-idle", Title: "empty", ScopeJSON: `{}`, CreatedAt: 10, UpdatedAt: 400, OwnerID: "owner-a"},
		{ID: "conv-failed", Title: "broken", ScopeJSON: `{"scope":{"assetId":"asset-web"}}`, CreatedAt: 10, UpdatedAt: 500, OwnerID: "owner-a"},
		{ID: "conv-foreign", Title: "Deploy Nginx elsewhere", ScopeJSON: `{"scope":{"assetId":"asset-web"}}`, CreatedAt: 10, UpdatedAt: 600, OwnerID: "owner-b"},
	}
	for index := range conversations {
		if err := gormDB.Create(&conversations[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	runs := []RunRow{
		{ID: "run-web-1", ConversationID: "conv-web", Status: store.RunStatusCompleted, Source: "chat", CreatedAt: 20, UpdatedAt: 20, FinishedAt: &finished, OwnerID: "owner-a"},
		{ID: "run-web-2", ConversationID: "conv-web", Status: store.RunStatusRunning, Source: "chat", CreatedAt: 30, UpdatedAt: 30, OwnerID: "owner-a"},
		{ID: "run-web-title", ConversationID: "conv-web", Status: store.RunStatusCompleted, Source: store.RunSourceTitle, CreatedAt: 40, UpdatedAt: 40, OwnerID: "owner-a"},
		{ID: "run-db", ConversationID: "conv-db", Status: store.RunStatusInterrupted, Source: "chat", CreatedAt: 20, UpdatedAt: 20, OwnerID: "owner-a"},
		{ID: "run-local", ConversationID: "conv-local", Status: store.RunStatusCompleted, Source: "chat", CreatedAt: 20, UpdatedAt: 20, FinishedAt: &finished, OwnerID: "owner-a"},
		{ID: "run-failed-1", ConversationID: "conv-failed", Status: store.RunStatusCompleted, Source: "chat", CreatedAt: 20, UpdatedAt: 20, FinishedAt: &finished, OwnerID: "owner-a"},
		{ID: "run-failed-2", ConversationID: "conv-failed", Status: store.RunStatusFailed, Source: "chat", CreatedAt: 30, UpdatedAt: 30, FinishedAt: &finished, OwnerID: "owner-a"},
		{ID: "run-foreign", ConversationID: "conv-foreign", Status: store.RunStatusRunning, Source: "chat", CreatedAt: 20, UpdatedAt: 20, OwnerID: "owner-b"},
	}
	for index := range runs {
		if err := gormDB.Create(&runs[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	messages := []MessageRow{
		{ID: "m1", ConversationID: "conv-local", Role: "assistant", ContentJSON: `{"role":"assistant","content":"Disk 100%_FULL on /var"}`, CreatedAt: 1, Seq: 1, OwnerID: "owner-a"},
		{ID: "m2", ConversationID: "conv-foreign", Role: "assistant", ContentJSON: `{"role":"assistant","content":"disk 100%_full"}`, CreatedAt: 1, Seq: 1, OwnerID: "owner-b"},
	}
	for index := range messages {
		if err := gormDB.Create(&messages[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	events := []RunEventRow{
		{RunID: "run-db", Seq: 1, Type: "delta", PayloadJSON: `{}`, CreatedAt: 21, OwnerID: "owner-a"},
		{RunID: "run-db", Seq: 2, Type: "confirmRequired", PayloadJSON: `{"id":"call-1","tool":"exec"}`, CreatedAt: 22, OwnerID: "owner-a"},
		{RunID: "run-db", Seq: 3, Type: "interactionResolved", PayloadJSON: `{"callId":"call-1","decision":"allow"}`, CreatedAt: 23, OwnerID: "owner-a"},
		{RunID: "run-foreign", Seq: 1, Type: "confirmRequired", PayloadJSON: `{}`, CreatedAt: 22, OwnerID: "owner-b"},
	}
	for index := range events {
		if err := gormDB.Create(&events[index]).Error; err != nil {
			t.Fatal(err)
		}
	}

	owner := ipc.WithUserID(ctx, "owner-a")
	rows, err := database.ConvOverview(owner, store.ConvOverviewFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]store.ConvOverviewRow{}
	for _, row := range rows {
		got[row.ID] = row
	}
	if len(rows) != 5 || rows[0].ID != "conv-failed" {
		t.Fatalf("overview = %+v, want the caller's 5 conversations, most recent first", rows)
	}
	for id, want := range map[string]struct {
		asset  string
		status string
		runs   int64
	}{
		"conv-web":    {asset: "asset-web", status: store.ConvStatusRunning, runs: 2},
		"conv-db":     {asset: "asset-db", status: store.ConvStatusInterrupted, runs: 1},
		"conv-local":  {asset: "", status: store.ConvStatusCompleted, runs: 1},
		"conv-idle":   {asset: "", status: store.ConvStatusIdle, runs: 0},
		"conv-failed": {asset: "asset-web", status: store.ConvStatusFailed, runs: 2},
	} {
		row := got[id]
		if row.AssetID != want.asset || row.Status != want.status || row.RunCount != want.runs {
			t.Errorf("%s = asset %q status %q runs %d, want %+v", id, row.AssetID, row.Status, row.RunCount, want)
		}
	}

	web := store.SelectConvOverview(rows, "asset-web", false, "")
	if len(web) != 2 {
		t.Fatalf("host filter = %+v", web)
	}
	if unlinked := store.SelectConvOverview(rows, "", true, ""); len(unlinked) != 2 {
		t.Fatalf("unlinked filter = %+v", unlinked)
	}
	if failed := store.SelectConvOverview(rows, "", false, store.ConvStatusFailed); len(failed) != 1 || failed[0].ID != "conv-failed" {
		t.Fatalf("status filter = %+v", failed)
	}

	for _, test := range []struct {
		query string
		want  string
	}{
		{query: "nginx", want: "conv-web"},
		{query: "100%_full", want: "conv-local"},
		{query: "巡检", want: "conv-db"},
	} {
		found, err := database.ConvOverview(owner, store.ConvOverviewFilter{Query: test.query})
		if err != nil {
			t.Fatal(err)
		}
		if len(found) != 1 || found[0].ID != test.want {
			t.Errorf("query %q = %+v, want only %s", test.query, found, test.want)
		}
	}
	if found, err := database.ConvOverview(owner, store.ConvOverviewFilter{Query: "1_0"}); err != nil || len(found) != 0 {
		t.Fatalf("LIKE wildcards must match literally: %+v, %v", found, err)
	}

	ranged, err := database.ConvOverview(owner, store.ConvOverviewFilter{UpdatedFrom: 200, UpdatedBefore: 400})
	if err != nil || len(ranged) != 2 {
		t.Fatalf("time range = %+v, %v", ranged, err)
	}
	if picked, err := database.ConvOverview(owner, store.ConvOverviewFilter{IDs: []string{"conv-db", "conv-foreign"}}); err != nil || len(picked) != 1 || picked[0].ID != "conv-db" {
		t.Fatalf("ids = %+v, %v", picked, err)
	}
	if _, err := database.ConvOverview(owner, store.ConvOverviewFilter{UpdatedFrom: 5, UpdatedBefore: 5}); err == nil {
		t.Fatal("an empty time range must be refused")
	}

	interactions, err := database.ConvInteractionEvents(owner, "conv-db")
	if err != nil {
		t.Fatal(err)
	}
	if len(interactions) != 2 || interactions[0].Type != "confirmRequired" || interactions[1].Type != "interactionResolved" {
		t.Fatalf("interactions = %+v", interactions)
	}
	if _, err := database.ConvInteractionEvents(owner, "conv-foreign"); err == nil {
		t.Fatal("a foreign conversation's interactions must not be readable")
	}
	other, err := database.ConvOverview(ipc.WithUserID(ctx, "owner-b"), store.ConvOverviewFilter{Query: "100%_full"})
	if err != nil || len(other) != 1 || other[0].ID != "conv-foreign" || other[0].Status != store.ConvStatusRunning {
		t.Fatalf("owner-b overview = %+v, %v", other, err)
	}
}

func TestConversationAssetID(t *testing.T) {
	for scope, want := range map[string]string{
		`{"scope":{"assetId":"a1","sessionId":"s"}}`: "a1",
		`{"scope":null}`: "",
		`{}`:             "",
		`not json`:       "",
	} {
		if got := store.ConversationAssetID(scope); got != want {
			t.Errorf("ConversationAssetID(%s) = %q, want %q", scope, got, want)
		}
	}
}
