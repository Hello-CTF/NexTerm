package production

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestTranscriptStarListAndPurgeCommands(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "web-01"})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := transcriptDispatcher(t, database, session.NewManager(session.Config{}))
	id := seedTranscript(t, database, asset.ID, 1000, "output\r\n")
	if err := database.TranscriptEnd(ctx, id, 2000, false); err != nil {
		t.Fatal(err)
	}

	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "transcript_star", `{"id":"`+id+`","starred":true}`), new(any))
	var starred []transcriptSummaryDTO
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "transcript_list", `{"assetId":"`+asset.ID+`","starredOnly":true}`), &starred)
	if len(starred) != 1 || !starred[0].Starred {
		t.Fatalf("starred list = %+v", starred)
	}

	var usage transcriptUsageDTO
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "transcript_usage", `{"endedBefore":3000}`), &usage)
	if usage.Total != 1 || usage.Starred != 1 || usage.Purgeable != 0 {
		t.Fatalf("usage = %+v", usage)
	}
	var purged transcriptPurgeResult
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "transcript_purge", `{"endedBefore":3000}`), &purged)
	if purged.Deleted != 0 {
		t.Fatalf("a starred transcript must not be purged: %+v", purged)
	}
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "transcript_star", `{"id":"`+id+`","starred":false}`), new(any))
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "transcript_purge", `{"endedBefore":3000}`), &purged)
	if purged.Deleted != 1 {
		t.Fatalf("the unstarred transcript must be purged: %+v", purged)
	}
	if response := dispatchStoreTest(dispatcher, "transcript_purge", `{"endedBefore":0}`); response.OK {
		t.Fatal("a purge without a cutoff must be rejected")
	}
}

func TestTranscriptRetentionCommandsAreDesktopOnly(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := transcriptDispatcher(t, database, session.NewManager(session.Config{}))

	var got transcriptRetentionDTO
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "transcript_retention_get", `{}`), &got)
	if !got.Enabled || !got.Editable || got.MaxAgeMS <= 0 || got.MaxCount <= 0 {
		t.Fatalf("desktop defaults = %+v", got)
	}
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "transcript_retention_set",
		`{"enabled":false,"maxAgeMs":86400000,"maxCount":10,"unlimitedCount":false}`), new(any))
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "transcript_retention_get", `{}`), &got)
	if got.Enabled || got.MaxAgeMS != 86400000 || got.MaxCount != 10 {
		t.Fatalf("stored setting = %+v", got)
	}

	// On the multi-user server the policy is server-wide: a user can read it but not change it.
	user := ipc.WithUserID(ctx, "user-a")
	response := dispatcher.Dispatch(user, ipc.Request{Command: "transcript_retention_set",
		Args: json.RawMessage(`{"enabled":true,"maxAgeMs":1,"maxCount":1}`)}, ipc.Environment{})
	if response.OK {
		t.Fatal("a server user must not change the server-wide cleanup policy")
	}
	response = dispatcher.Dispatch(user, ipc.Request{Command: "transcript_retention_get", Args: json.RawMessage(`{}`)}, ipc.Environment{})
	requireStoreTestResponse(t, response, &got)
	if got.Editable || got.Enabled {
		t.Fatalf("server view = %+v; want the unchanged policy, not editable", got)
	}
}
