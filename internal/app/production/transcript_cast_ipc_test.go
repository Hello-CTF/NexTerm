package production

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func castLines(t *testing.T, path string) [][]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Fatalf("cast must end with newline: %q", raw)
	}
	var lines [][]any
	for index, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if index == 0 {
			var header map[string]any
			if err := json.Unmarshal([]byte(line), &header); err != nil {
				t.Fatal(err)
			}
			lines = append(lines, []any{header})
			continue
		}
		var event []any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, event)
	}
	return lines
}

func TestTranscriptExportCastWritesOutputAndResizeOnly(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := transcriptDispatcher(t, database, session.NewManager(session.Config{}))
	id := ids.New()
	if err := database.TranscriptStart(ctx, store.TranscriptRow{
		ID: id, SessionID: "s", AssetID: "asset-1", AssetName: "web-01", AssetKind: "ssh", StartedAt: 1_700_000_000_000,
	}); err != nil {
		t.Fatal(err)
	}
	chunks := []store.TranscriptChunkRow{
		{Seq: 0, TabID: "t", TS: 1_700_000_000_100, Kind: store.TranscriptChunkKindResize, Data: []byte(`{"cols":132,"rows":43}`)},
		{Seq: 1, TabID: "t", TS: 1_700_000_000_200, Kind: store.TranscriptChunkKindOutput, Data: []byte("login: ")},
		{Seq: 2, TabID: "t", TS: 1_700_000_000_300, Kind: store.TranscriptChunkKindInput, Data: []byte("hunter2\r")},
		{Seq: 3, TabID: "t", TS: 1_700_000_000_400, Kind: store.TranscriptChunkKindResize, Data: []byte(`{"cols":100,"rows":30}`)},
		{Seq: 4, TabID: "t", TS: 1_700_000_001_500, Kind: store.TranscriptChunkKindOutput, Data: []byte("done\r\n")},
	}
	if err := database.TranscriptAppendChunks(ctx, id, chunks); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "out.cast")
	response := dispatcher.Dispatch(ctx, ipc.Request{Command: "transcript_export_cast",
		Args: json.RawMessage(`{"id":"` + id + `","path":` + castJSONString(t, target) + `}`)}, ipc.Environment{})
	var result transcriptExportCastResult
	requireStoreTestResponse(t, response, &result)
	if result.Path != target || result.Events != 3 || result.TimingEstimated || result.Bytes <= 0 {
		t.Fatalf("result = %+v", result)
	}
	if info, err := os.Stat(target); err != nil || info.Size() != result.Bytes {
		t.Fatalf("stat = %v %v", info, err)
	}
	lines := castLines(t, target)
	header := lines[0][0].(map[string]any)
	if header["width"].(float64) != 132 || header["height"].(float64) != 43 || header["version"].(float64) != 2 ||
		header["timestamp"].(float64) != 1_700_000_000 || header["title"] != "web-01" {
		t.Fatalf("header = %v", header)
	}
	raw, _ := os.ReadFile(target)
	if strings.Contains(string(raw), "hunter2") {
		t.Fatal("keyboard input must not be exported")
	}
	if len(lines) != 4 || lines[1][1] != "o" || lines[1][2] != "login: " || lines[2][1] != "r" || lines[2][2] != "100x30" ||
		lines[3][2] != "done\r\n" || lines[3][0].(float64) != 1.5 {
		t.Fatalf("events = %v", lines[1:])
	}
}

func TestTranscriptExportCastPagesThroughLargeTranscripts(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := transcriptDispatcher(t, database, session.NewManager(session.Config{}))
	id := ids.New()
	if err := database.TranscriptStart(ctx, store.TranscriptRow{ID: id, SessionID: "s", AssetID: "a", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("x", 200_000)
	var rows []store.TranscriptChunkRow
	for seq := int64(0); seq < 30; seq++ {
		rows = append(rows, store.TranscriptChunkRow{Seq: seq, TabID: "t", TS: 1000 + seq, Data: []byte(payload)})
	}
	if err := database.TranscriptAppendChunks(ctx, id, rows); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "big.cast")
	response := dispatchStoreTest(dispatcher, "transcript_export_cast", `{"id":"`+id+`","path":`+castJSONString(t, target)+`}`)
	var result transcriptExportCastResult
	requireStoreTestResponse(t, response, &result)
	if result.Events != 30 {
		t.Fatalf("events = %d", result.Events)
	}
}

func TestTranscriptExportCastRejectsOtherOwnerAndOmittedContent(t *testing.T) {
	owner := ipc.WithUserID(context.Background(), "owner-a")
	other := ipc.WithUserID(context.Background(), "owner-b")
	database, err := store.OpenInMemory(owner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := transcriptDispatcher(t, database, session.NewManager(session.Config{}))
	id := ids.New()
	if err := database.TranscriptStart(owner, store.TranscriptRow{ID: id, SessionID: "s", AssetID: "a", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	if err := database.TranscriptAppendChunks(owner, id, []store.TranscriptChunkRow{{Seq: 0, TabID: "t", TS: 1001, Data: []byte("x")}}); err != nil {
		t.Fatal(err)
	}
	omittedID := ids.New()
	ended := int64(3000)
	if err := database.TranscriptInsertSynced(owner, store.TranscriptRow{
		ID: omittedID, SessionID: "s2", AssetID: "a", StartedAt: 2000, EndedAt: &ended, ContentOmitted: true,
	}, nil); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	export := func(ctx context.Context, transcriptID, name string) ipc.Response {
		args := `{"id":"` + transcriptID + `","path":` + castJSONString(t, filepath.Join(dir, name)) + `}`
		return dispatcher.Dispatch(ctx, ipc.Request{Command: "transcript_export_cast", Args: json.RawMessage(args)}, ipc.Environment{})
	}
	if response := export(other, id, "other.cast"); response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
		t.Fatalf("other owner must get not-found: %+v", response)
	}
	if _, err := os.Stat(filepath.Join(dir, "other.cast")); err == nil {
		t.Fatal("no file may be written for a foreign transcript")
	}
	if response := export(owner, omittedID, "omitted.cast"); response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("omitted content must be a bad param: %+v", response)
	}
	if response := export(owner, id, "ok.cast"); !response.OK {
		t.Fatalf("owner export failed: %+v", response)
	}
}

func castJSONString(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
