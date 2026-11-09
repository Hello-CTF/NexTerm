package production

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestTranscriptExportTextRendersOutputOnly(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := transcriptDispatcher(t, database, session.NewManager(session.Config{}))
	id := ids.New()
	start := time.Date(2026, 11, 9, 1, 2, 3, 0, time.UTC).UnixMilli()
	if err := database.TranscriptStart(ctx, store.TranscriptRow{
		ID: id, SessionID: "s", AssetID: "asset-1", AssetName: "web-01", AssetKind: "ssh", StartedAt: start,
	}); err != nil {
		t.Fatal(err)
	}
	chunks := []store.TranscriptChunkRow{
		{Seq: 0, TabID: "t", TS: start, Kind: store.TranscriptChunkKindResize, Data: []byte(`{"cols":4,"rows":24}`)},
		{Seq: 1, TabID: "t", TS: start, Kind: store.TranscriptChunkKindOutput, Data: []byte("\x1b]0;web-01\x07password: ")},
		{Seq: 2, TabID: "t", TS: start + 500, Kind: store.TranscriptChunkKindInput, Data: []byte("hunter2\r")},
		{Seq: 3, TabID: "t", TS: start + 1000, Kind: store.TranscriptChunkKindOutput, Data: []byte("\r\n\x1b[32mabcdefgh\x1b[0m\rXY\r\n")},
		{Seq: 4, TabID: "t", TS: start + 2000, Kind: store.TranscriptChunkKindOutput, Data: []byte("\x1b[?1049hvim screen\x1b[?1049l$ ")},
	}
	if err := database.TranscriptAppendChunks(ctx, id, chunks); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "out.txt")
	response := dispatcher.Dispatch(ctx, ipc.Request{Command: "transcript_export_text",
		Args: json.RawMessage(`{"id":"` + id + `","path":` + castJSONString(t, target) + `}`)}, ipc.Environment{})
	var result transcriptExportTextResult
	requireStoreTestResponse(t, response, &result)
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	// The resize to 4 columns makes the carriage return land on the second screen row of the wrapped line.
	want := "password:\nabcdXYgh\n$\n"
	if string(raw) != want {
		t.Fatalf("text = %q, want %q", raw, want)
	}
	if result.Path != target || result.Bytes != int64(len(want)) || result.Lines != 3 || result.FullScreenSegments != 1 {
		t.Fatalf("result = %+v", result)
	}
	if strings.Contains(string(raw), "hunter2") {
		t.Fatal("keyboard input must not be exported")
	}

	timed := filepath.Join(t.TempDir(), "timed.txt")
	response = dispatchStoreTest(dispatcher, "transcript_export_text",
		`{"id":"`+id+`","path":`+castJSONString(t, timed)+`,"timestamps":true,"utcOffsetMinutes":480}`)
	requireStoreTestResponse(t, response, &result)
	raw, err = os.ReadFile(timed)
	if err != nil {
		t.Fatal(err)
	}
	want = "[2026-11-09 09:02:03] password:\n[2026-11-09 09:02:04] abcdXYgh\n[2026-11-09 09:02:05] $\n"
	if string(raw) != want {
		t.Fatalf("timed text = %q, want %q", raw, want)
	}
}

func TestTranscriptExportTextPagesThroughLargeTranscripts(t *testing.T) {
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
	line := strings.Repeat("x", 999) + "\n"
	payload := strings.Repeat(line, 200)
	var rows []store.TranscriptChunkRow
	for seq := int64(0); seq < 30; seq++ {
		rows = append(rows, store.TranscriptChunkRow{Seq: seq, TabID: "t", TS: 1000 + seq, Data: []byte(payload)})
	}
	if err := database.TranscriptAppendChunks(ctx, id, rows); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "big.txt")
	response := dispatchStoreTest(dispatcher, "transcript_export_text", `{"id":"`+id+`","path":`+castJSONString(t, target)+`}`)
	var result transcriptExportTextResult
	requireStoreTestResponse(t, response, &result)
	if result.Lines != 30*200 || result.Bytes != int64(30*len(payload)) {
		t.Fatalf("result = %+v", result)
	}
}

func TestTranscriptExportTextRejectsOtherOwnerOmittedContentAndBadOffset(t *testing.T) {
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
	export := func(ctx context.Context, transcriptID, name, extra string) ipc.Response {
		args := `{"id":"` + transcriptID + `","path":` + castJSONString(t, filepath.Join(dir, name)) + extra + `}`
		return dispatcher.Dispatch(ctx, ipc.Request{Command: "transcript_export_text", Args: json.RawMessage(args)}, ipc.Environment{})
	}
	if response := export(other, id, "other.txt", ""); response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
		t.Fatalf("other owner must get not-found: %+v", response)
	}
	if _, err := os.Stat(filepath.Join(dir, "other.txt")); err == nil {
		t.Fatal("no file may be written for a foreign transcript")
	}
	if response := export(owner, omittedID, "omitted.txt", ""); response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("omitted content must be a bad param: %+v", response)
	}
	if response := export(owner, id, "offset.txt", `,"timestamps":true,"utcOffsetMinutes":100000`); response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("out-of-range offset must be a bad param: %+v", response)
	}
	if _, err := os.Stat(filepath.Join(dir, "offset.txt")); err == nil {
		t.Fatal("no file may be written for a rejected request")
	}
	if response := export(owner, id, "ok.txt", ""); !response.OK {
		t.Fatalf("owner export failed: %+v", response)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "ok.txt")); err != nil || string(raw) != "x\n" {
		t.Fatalf("owner text = %q, %v", raw, err)
	}
}
