package production

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestSearchCommandsRoundTrip(t *testing.T) {
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

	var empty commandSearchResult
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "command_search", `{"text":"nothing"}`), &empty)
	response := dispatchStoreTest(dispatcher, "command_search", `{"text":"nothing"}`)
	var raw map[string]json.RawMessage
	requireStoreTestResponse(t, response, &raw)
	if string(raw["items"]) != "[]" {
		t.Fatalf("empty items must encode as [], got %s", raw["items"])
	}

	transcriptID := seedTranscript(t, database, asset.ID, 1000, "build ok\r\n", "deploy needle done\r\n")
	if err := database.CommandLogInsert(ctx, store.CommandLogInput{SessionID: "session-seed", TabID: "tab-seed", AssetID: asset.ID, Command: "Make Build", Cwd: "/srv/app", Source: "terminal", StartedAt: 1500, FinishedAt: 1600}); err != nil {
		t.Fatal(err)
	}
	var found commandSearchResult
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "command_search", `{"text":"make build","assetId":"`+asset.ID+`","limit":10}`), &found)
	if len(found.Items) != 1 || found.HasMore || found.Items[0].Command != "Make Build" ||
		found.Items[0].TranscriptID == nil || *found.Items[0].TranscriptID != transcriptID {
		t.Fatalf("unexpected command search: %+v", found)
	}
	if found.Items[0].Cwd != "/srv/app" {
		t.Fatalf("command search must return the start directory: %+v", found.Items[0])
	}
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "command_search", `{"text":"make","cwd":"/srv/app"}`), &found)
	if len(found.Items) != 1 {
		t.Fatalf("cwd filter must keep the command from that directory: %+v", found)
	}
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "command_search", `{"text":"make","cwd":"/srv"}`), &found)
	if len(found.Items) != 0 {
		t.Fatalf("cwd filter must match the exact directory: %+v", found)
	}
	var keys map[string]json.RawMessage
	var items []map[string]json.RawMessage
	_ = json.Unmarshal(dispatchStoreTest(dispatcher, "command_search", `{}`).Data, &keys)
	_ = json.Unmarshal(keys["items"], &items)
	for _, key := range []string{"id", "sessionId", "tabId", "assetId", "command", "cwd", "source", "exitCode", "startedAt", "finishedAt", "transcriptId"} {
		if _, ok := items[0][key]; !ok {
			t.Errorf("item is missing field %q", key)
		}
	}
	if _, ok := keys["hasMore"]; !ok {
		t.Error("result is missing hasMore")
	}

	var host transcriptHostSearchResult
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "transcript_search_host", `{"assetId":"`+asset.ID+`","query":"needle"}`), &host)
	if len(host.Sessions) != 1 || host.Sessions[0].Transcript.ID != transcriptID || len(host.Sessions[0].Matches) != 1 ||
		host.ScannedSessions != 1 || host.Truncated {
		t.Fatalf("unexpected host search: %+v", host)
	}
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "transcript_search_host", `{"assetId":"`+asset.ID+`","query":"absent"}`), &raw)
	if string(raw["sessions"]) != "[]" {
		t.Fatalf("empty sessions must encode as [], got %s", raw["sessions"])
	}
	if response := dispatchStoreTest(dispatcher, "transcript_search_host", `{"assetId":"`+asset.ID+`","query":""}`); response.OK {
		t.Fatal("empty query must be rejected")
	}
	if response := dispatchStoreTest(dispatcher, "transcript_search_host", `{"assetId":"bad","query":"x"}`); response.OK {
		t.Fatal("invalid asset id must be rejected")
	}
}

func TestSearchCommandsHideOtherUsers(t *testing.T) {
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
	ctxA := ipc.WithUserID(ctx, "u-a")
	if err := database.CommandLogInsert(ctx, store.CommandLogInput{SessionID: "s", TabID: "t", AssetID: asset.ID, UserID: "u-a", Command: "secret-cmd", Source: "terminal", StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := database.TranscriptStart(ctxA, store.TranscriptRow{ID: "01JTRANSCRIPTOWNER00000001", SessionID: "s", AssetID: asset.ID, StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := database.TranscriptAppendChunks(ctxA, "01JTRANSCRIPTOWNER00000001", []store.TranscriptChunkRow{{Seq: 0, TabID: "t", TS: 1, Data: []byte("secret-out\r\n")}}); err != nil {
		t.Fatal(err)
	}
	dispatcher := transcriptDispatcher(t, database, session.NewManager(session.Config{}))
	call := func(user, command, args string) ipc.Response {
		return dispatcher.Dispatch(ipc.WithUserID(ctx, user), ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
	}
	var cmds commandSearchResult
	requireStoreTestResponse(t, call("u-b", "command_search", `{"text":"secret"}`), &cmds)
	if len(cmds.Items) != 0 {
		t.Fatalf("other user must not see commands: %+v", cmds)
	}
	requireStoreTestResponse(t, call("u-a", "command_search", `{"text":"secret"}`), &cmds)
	if len(cmds.Items) != 1 {
		t.Fatalf("owner must see its command: %+v", cmds)
	}
	var host transcriptHostSearchResult
	args := `{"assetId":"` + asset.ID + `","query":"secret-out"}`
	requireStoreTestResponse(t, call("u-b", "transcript_search_host", args), &host)
	if len(host.Sessions) != 0 {
		t.Fatalf("other user must not see output: %+v", host)
	}
	requireStoreTestResponse(t, call("u-a", "transcript_search_host", args), &host)
	if len(host.Sessions) != 1 {
		t.Fatalf("owner must see output: %+v", host)
	}
}
