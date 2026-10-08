package production

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func seedTranscript(t *testing.T, database *store.Store, assetID string, startedAt int64, chunks ...string) string {
	t.Helper()
	id := "01JTRANSCRIPTSEED000000001"
	if err := database.TranscriptStart(context.Background(), store.TranscriptRow{
		ID: id, SessionID: "session-seed", AssetID: assetID, AssetName: "web-01", AssetKind: "ssh", StartedAt: startedAt,
	}); err != nil {
		t.Fatal(err)
	}
	rows := make([]store.TranscriptChunkRow, 0, len(chunks))
	for index, data := range chunks {
		rows = append(rows, store.TranscriptChunkRow{
			Seq: int64(index), TabID: "tab-seed", TS: startedAt + int64(index), Data: []byte(data),
		})
	}
	if err := database.TranscriptAppendChunks(context.Background(), id, rows); err != nil {
		t.Fatal(err)
	}
	return id
}

func transcriptDispatcher(t *testing.T, database *store.Store, sessions *session.Manager) *ipc.Dispatcher {
	t.Helper()
	service := newTerminalCommandService(database, sessions, nil, nil, nil, nil, nil, nil)
	dispatcher := ipc.NewDispatcher()
	if err := service.registerTranscripts(dispatcher); err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func TestTranscriptCommandsListReadSearchDelete(t *testing.T) {
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
	id := seedTranscript(t, database, asset.ID, 1000, "hello \x1b[31mred\x1b[0m world\r\n", "second line\r\n")
	if err := database.TranscriptEnd(ctx, id, 2000, false); err != nil {
		t.Fatal(err)
	}

	response := dispatchStoreTest(dispatcher, "transcript_list", `{"assetId":"`+asset.ID+`"}`)
	var summaries []transcriptSummaryDTO
	requireStoreTestResponse(t, response, &summaries)
	if len(summaries) != 1 {
		t.Fatalf("expected one summary, got %+v", summaries)
	}
	summary := summaries[0]
	if summary.ID != id || summary.SessionID != "session-seed" || summary.AssetName != "web-01" ||
		summary.AssetKind != "ssh" || summary.AssetDeleted || summary.Active ||
		summary.StartedAt != 1000 || summary.EndedAt == nil || *summary.EndedAt != 2000 ||
		summary.Bytes != int64(len("hello \x1b[31mred\x1b[0m world\r\nsecond line\r\n")) || summary.Chunks != 2 {
		t.Fatalf("unexpected summary: %+v", summary)
	}

	response = dispatchStoreTest(dispatcher, "transcript_read", `{"id":"`+id+`","afterSeq":0,"maxBytes":1048576}`)
	var read transcriptReadResult
	requireStoreTestResponse(t, response, &read)
	if len(read.Chunks) != 2 || read.NextSeq != 2 || !read.Done || read.TotalBytes != summary.Bytes {
		t.Fatalf("unexpected read result: %+v", read)
	}
	first, err := base64.StdEncoding.DecodeString(read.Chunks[0].DataBase64)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != "hello \x1b[31mred\x1b[0m world\r\n" || read.Chunks[0].TabID != "tab-seed" {
		t.Fatalf("unexpected chunk payload: %+v", read.Chunks[0])
	}
	response = dispatchStoreTest(dispatcher, "transcript_read", `{"id":"`+id+`","afterSeq":1}`)
	requireStoreTestResponse(t, response, &read)
	if len(read.Chunks) != 1 || read.Chunks[0].Seq != 1 || !read.Done {
		t.Fatalf("unexpected second page: %+v", read)
	}

	response = dispatchStoreTest(dispatcher, "transcript_search", `{"id":"`+id+`","query":"second"}`)
	var matches []transcriptMatchDTO
	requireStoreTestResponse(t, response, &matches)
	if len(matches) != 1 || matches[0].Seq != 1 {
		t.Fatalf("unexpected matches: %+v", matches)
	}
	response = dispatchStoreTest(dispatcher, "transcript_search", `{"id":"`+id+`","query":""}`)
	if response.OK {
		t.Fatal("empty search query must fail")
	}

	response = dispatchStoreTest(dispatcher, "transcript_delete", `{"id":"`+id+`"}`)
	if !response.OK {
		t.Fatalf("delete failed: %+v", response)
	}
	response = dispatchStoreTest(dispatcher, "transcript_delete", `{"id":"`+id+`"}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
		t.Fatalf("second delete must be not-found: %+v", response)
	}
}

func TestTranscriptListMarksDeletedAssetAndUncleanEnd(t *testing.T) {
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
	crashed := seedTranscript(t, database, asset.ID, 1000, "output\r\n")
	if err := database.AssetDelete(ctx, asset.ID); err != nil {
		t.Fatal(err)
	}

	response := dispatchStoreTest(dispatcher, "transcript_list", `{"assetId":"`+asset.ID+`"}`)
	var summaries []transcriptSummaryDTO
	requireStoreTestResponse(t, response, &summaries)
	if len(summaries) != 1 {
		t.Fatalf("expected one summary, got %+v", summaries)
	}
	if !summaries[0].AssetDeleted {
		t.Fatal("deleted asset must be flagged on the summary")
	}
	if summaries[0].EndedAt != nil {
		t.Fatal("crashed transcript must have no ended_at")
	}
	if summaries[0].Active {
		t.Fatal("transcript of a session unknown to the manager must not be active")
	}
	if summaries[0].ID != crashed {
		t.Fatalf("unexpected summary: %+v", summaries[0])
	}
}

func TestTranscriptCommandsValidateInput(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := transcriptDispatcher(t, database, session.NewManager(session.Config{}))
	for _, test := range []struct {
		command string
		args    string
	}{
		{"transcript_list", `{"assetId":"not a ulid"}`},
		{"transcript_read", `{"id":"!!"}`},
		{"transcript_read", `{"id":"01JTRANSCRIPTMISSING000000"}`},
		{"transcript_search", `{"id":"01JTRANSCRIPTMISSING000000","query":"x"}`},
		{"transcript_delete", `{"id":"01JTRANSCRIPTMISSING000000"}`},
	} {
		response := dispatchStoreTest(dispatcher, test.command, test.args)
		if response.OK {
			t.Fatalf("%s %s must fail", test.command, test.args)
		}
	}
}

func TestTranscriptSummaryJSONShape(t *testing.T) {
	payload, err := json.Marshal(transcriptSummaryDTO{ID: "t1", EndedAt: nil})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "sessionId", "assetId", "assetName", "assetKind", "assetDeleted", "startedAt", "bytes", "chunks", "truncated", "active", "syncOptIn", "contentOmitted"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing json key %s in %s", key, payload)
		}
	}
	if value, ok := decoded["endedAt"]; !ok || value != nil {
		t.Fatalf("endedAt must be present and null when unset: %s", payload)
	}
}

func TestTranscriptHostsCommandIncludesDeletedAssets(t *testing.T) {
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
	if err := database.AssetDelete(ctx, asset.ID); err != nil {
		t.Fatal(err)
	}

	response := dispatchStoreTest(dispatcher, "transcript_hosts", `{}`)
	var hosts []transcriptHostDTO
	requireStoreTestResponse(t, response, &hosts)
	if len(hosts) != 1 {
		t.Fatalf("expected one host, got %+v", hosts)
	}
	host := hosts[0]
	if host.AssetID != asset.ID || host.AssetName != "web-01" || host.AssetKind != "ssh" ||
		!host.AssetDeleted || host.Transcripts != 1 || host.LastStartedAt != 1000 {
		t.Fatalf("unexpected host DTO: %+v", host)
	}
}

func TestTranscriptReadReturnsChunkKinds(t *testing.T) {
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
	id := "01JTRANSCRIPTKIND000000001"
	if err := database.TranscriptStart(ctx, store.TranscriptRow{
		ID: id, SessionID: "session-seed", AssetID: asset.ID, AssetName: "web-01", AssetKind: "ssh", StartedAt: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.TranscriptAppendChunks(ctx, id, []store.TranscriptChunkRow{
		{Seq: 0, TabID: "tab-seed", TS: 1001, Kind: store.TranscriptChunkKindResize, Data: []byte(`{"cols":80,"rows":24}`)},
		{Seq: 1, TabID: "tab-seed", TS: 1002, Kind: store.TranscriptChunkKindOutput, Data: []byte("visible ")},
		{Seq: 2, TabID: "tab-seed", TS: 1003, Kind: store.TranscriptChunkKindInput, Data: []byte("secret-input")},
	}); err != nil {
		t.Fatal(err)
	}

	response := dispatchStoreTest(dispatcher, "transcript_read", `{"id":"`+id+`","afterSeq":0,"maxBytes":1048576}`)
	var read transcriptReadResult
	requireStoreTestResponse(t, response, &read)
	if len(read.Chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %+v", read)
	}
	wantKinds := []int{store.TranscriptChunkKindResize, store.TranscriptChunkKindOutput, store.TranscriptChunkKindInput}
	for index, chunk := range read.Chunks {
		if chunk.Kind != wantKinds[index] {
			t.Fatalf("chunk %d kind = %d, want %d", index, chunk.Kind, wantKinds[index])
		}
	}

	response = dispatchStoreTest(dispatcher, "transcript_search", `{"id":"`+id+`","query":"secret-input"}`)
	var matches []transcriptMatchDTO
	requireStoreTestResponse(t, response, &matches)
	if len(matches) != 0 {
		t.Fatalf("search must not scan input chunks: %+v", matches)
	}
	response = dispatchStoreTest(dispatcher, "transcript_search", `{"id":"`+id+`","query":"visible"}`)
	requireStoreTestResponse(t, response, &matches)
	if len(matches) != 1 || matches[0].Seq != 1 {
		t.Fatalf("output must stay searchable: %+v", matches)
	}
}
