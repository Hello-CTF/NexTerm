package production

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// Fixed start times (UTC): 2023-11-14 22:13:20, one day later, and ten days later.
const (
	archiveDay0 = int64(1_700_000_000_000)
	archiveDay1 = archiveDay0 + 24*60*60*1000
	archiveDay9 = archiveDay0 + 10*24*60*60*1000
)

type archiveSeed struct {
	id        string
	sessionID string
	assetID   string
	assetName string
	startedAt int64
	ended     bool
	starred   bool
	omitted   bool
	output    string
}

func seedArchiveTranscript(t *testing.T, ctx context.Context, database *store.Store, seed archiveSeed) {
	t.Helper()
	if seed.sessionID == "" {
		seed.sessionID = "session-" + seed.id
	}
	if seed.omitted {
		ended := seed.startedAt + 1000
		if err := database.TranscriptInsertSynced(ctx, store.TranscriptRow{
			ID: seed.id, SessionID: seed.sessionID, AssetID: seed.assetID, AssetName: seed.assetName, StartedAt: seed.startedAt,
			EndedAt: &ended, ContentOmitted: true, Bytes: 99,
		}, nil); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := database.TranscriptStart(ctx, store.TranscriptRow{
		ID: seed.id, SessionID: seed.sessionID, AssetID: seed.assetID, AssetName: seed.assetName, AssetKind: "ssh", StartedAt: seed.startedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.TranscriptAppendChunks(ctx, seed.id, []store.TranscriptChunkRow{
		{Seq: 0, TabID: "t", TS: seed.startedAt + 10, Kind: store.TranscriptChunkKindResize, Data: []byte(`{"cols":100,"rows":30}`)},
		{Seq: 1, TabID: "t", TS: seed.startedAt + 20, Kind: store.TranscriptChunkKindOutput, Data: []byte(seed.output)},
		{Seq: 2, TabID: "t", TS: seed.startedAt + 30, Kind: store.TranscriptChunkKindInput, Data: []byte("hunter2\r")},
	}); err != nil {
		t.Fatal(err)
	}
	if seed.ended {
		if err := database.TranscriptEnd(ctx, seed.id, seed.startedAt+1000, false); err != nil {
			t.Fatal(err)
		}
	}
	if seed.starred {
		if err := database.TranscriptSetStarred(ctx, seed.id, true); err != nil {
			t.Fatal(err)
		}
	}
}

type archiveFixture struct {
	ctx        context.Context
	database   *store.Store
	dispatcher *ipc.Dispatcher
	activeID   string
}

// newArchiveFixture seeds owner-a's transcripts: two on web-01 (one starred), one on db-01 ten days later, one whose
// content was not synced, and one still recording in a live session; owner-b has one transcript of its own.
func newArchiveFixture(t *testing.T) archiveFixture {
	t.Helper()
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	sessions := session.NewManager(session.Config{})
	t.Cleanup(func() { _ = sessions.Close() })
	live, err := sessions.RegisterRemoteSession(ctx, session.RemoteSessionOptions{OwnerID: "owner-a", Title: "live"})
	if err != nil {
		t.Fatal(err)
	}
	seeds := []archiveSeed{
		{id: "01JARCHIVE0000000000WEB001", assetID: "01JARCHIVEASSET000000WEB01", assetName: "web-01", startedAt: archiveDay0, ended: true, output: "first\r\n"},
		{id: "01JARCHIVE0000000000WEB002", assetID: "01JARCHIVEASSET000000WEB01", assetName: "web-01", startedAt: archiveDay1, ended: true, starred: true, output: "second\r\n"},
		{id: "01JARCHIVE00000000000DB001", assetID: "01JARCHIVEASSET0000000DB01", assetName: "db-01", startedAt: archiveDay9, ended: true, output: "third\r\n"},
		{id: "01JARCHIVE0000000000OMIT01", assetID: "01JARCHIVEASSET000000WEB01", assetName: "web-01", startedAt: archiveDay0 + 5000, omitted: true},
		{id: "01JARCHIVE0000000000LIVE01", sessionID: live.SessionID, assetID: "01JARCHIVEASSET000000WEB01", assetName: "web-01", startedAt: archiveDay1 + 5000, output: "live\r\n"},
	}
	for _, seed := range seeds {
		seedArchiveTranscript(t, ctx, database, seed)
	}
	seedArchiveTranscript(t, ipc.WithUserID(context.Background(), "owner-b"), database, archiveSeed{
		id: "01JARCHIVE0000000000OTHER1", assetID: "01JARCHIVEASSET000000WEB01", assetName: "web-01", startedAt: archiveDay0, ended: true, output: "foreign\r\n",
	})
	return archiveFixture{ctx: ctx, database: database, dispatcher: transcriptDispatcher(t, database, sessions), activeID: "01JARCHIVE0000000000LIVE01"}
}

func (f archiveFixture) export(t *testing.T, ctx context.Context, request map[string]any) (transcriptExportArchiveResult, ipc.Response) {
	t.Helper()
	if _, ok := request["timeZone"]; !ok {
		request["timeZone"] = "UTC"
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response := f.dispatcher.Dispatch(ctx, ipc.Request{Command: "transcript_export_archive", Args: raw}, ipc.Environment{})
	var result transcriptExportArchiveResult
	if response.OK {
		if err := json.Unmarshal(response.Data, &result); err != nil {
			t.Fatal(err)
		}
	}
	return result, response
}

func readZipEntries(t *testing.T, path string) (names []string, contents map[string]string) {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	contents = map[string]string{}
	for _, file := range reader.File {
		handle, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(handle)
		_ = handle.Close()
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, file.Name)
		contents[file.Name] = string(data)
	}
	return names, contents
}

func readTarGzEntries(t *testing.T, path string) (names []string, contents map[string]string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(compressed)
	contents = map[string]string{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if header.Size != int64(len(data)) {
			t.Fatalf("%s: header size %d, data %d", header.Name, header.Size, len(data))
		}
		names = append(names, header.Name)
		contents[header.Name] = string(data)
	}
	return names, contents
}

func decodeArchiveManifest(t *testing.T, raw string) transcriptArchiveManifest {
	t.Helper()
	var manifest transcriptArchiveManifest
	if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
		t.Fatalf("manifest %q: %v", raw, err)
	}
	return manifest
}

func TestTranscriptExportArchiveZipWritesEveryHostAndReportsSkips(t *testing.T) {
	fixture := newArchiveFixture(t)
	target := filepath.Join(t.TempDir(), "history.zip")
	result, response := fixture.export(t, fixture.ctx, map[string]any{"path": target, "format": "zip", "content": "both"})
	if !response.OK {
		t.Fatalf("export failed: %+v", response)
	}
	if result.Path != target || result.Matched != 5 || result.Exported != 3 || result.Files != 6 ||
		result.Skipped.ContentOmitted != 1 || result.Skipped.Active != 1 || result.EstimatedBytes <= 0 {
		t.Fatalf("result = %+v", result)
	}
	if info, err := os.Stat(target); err != nil || info.Size() != result.Bytes {
		t.Fatalf("archive size: %v %v, result %d", info, err, result.Bytes)
	}
	names, contents := readZipEntries(t, target)
	want := []string{
		"manifest.json",
		"web-01/web-01-2023-11-14-221320-WEB001.cast", "web-01/web-01-2023-11-14-221320-WEB001.txt",
		"web-01/web-01-2023-11-15-221320-WEB002.cast", "web-01/web-01-2023-11-15-221320-WEB002.txt",
		"db-01/db-01-2023-11-24-221320-0DB001.cast", "db-01/db-01-2023-11-24-221320-0DB001.txt",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("entries = %v, want %v", names, want)
	}
	cast := contents["web-01/web-01-2023-11-14-221320-WEB001.cast"]
	if !strings.Contains(cast, `"width":100`) || !strings.Contains(cast, "first") {
		t.Fatalf("cast = %q", cast)
	}
	if text := contents["db-01/db-01-2023-11-24-221320-0DB001.txt"]; !strings.Contains(text, "third") {
		t.Fatalf("text = %q", text)
	}
	for name, content := range contents {
		if strings.Contains(content, "hunter2") || strings.Contains(content, "foreign") || strings.Contains(content, "live") {
			t.Fatalf("%s carries input, a foreign or an active transcript: %q", name, content)
		}
	}
	manifest := decodeArchiveManifest(t, contents["manifest.json"])
	if len(manifest.Transcripts) != 3 || manifest.Transcripts[0].ID != "01JARCHIVE0000000000WEB001" ||
		manifest.Transcripts[0].Host != "web-01" || manifest.Transcripts[0].StartedAt != archiveDay0 ||
		manifest.Transcripts[0].EndedAt == nil || *manifest.Transcripts[0].EndedAt != archiveDay0+1000 ||
		!slices.Equal(manifest.Transcripts[0].Files, want[1:3]) || !manifest.Transcripts[1].Starred {
		t.Fatalf("manifest transcripts = %+v", manifest.Transcripts)
	}
	reasons := map[string]string{}
	for _, skipped := range manifest.Skipped {
		reasons[skipped.ID] = skipped.Reason
	}
	if len(reasons) != 2 || reasons["01JARCHIVE0000000000OMIT01"] != "contentOmitted" || reasons[fixture.activeID] != "active" {
		t.Fatalf("manifest skipped = %+v", manifest.Skipped)
	}
}

func TestTranscriptExportArchiveTarGzFiltersOneHostAndRange(t *testing.T) {
	fixture := newArchiveFixture(t)
	target := filepath.Join(t.TempDir(), "history.tar.gz")
	offset := 8 * 60
	result, response := fixture.export(t, fixture.ctx, map[string]any{
		"path": target, "format": "tar.gz", "content": "text", "timestamps": true,
		"assetId": "01JARCHIVEASSET000000WEB01", "startedFrom": archiveDay1, "startedBefore": archiveDay9,
		"timeZone": "", "utcOffsetMinutes": offset,
	})
	if !response.OK {
		t.Fatalf("export failed: %+v", response)
	}
	if result.Matched != 2 || result.Exported != 1 || result.Skipped.Active != 1 || result.Skipped.ContentOmitted != 0 || result.Files != 1 {
		t.Fatalf("result = %+v", result)
	}
	names, contents := readTarGzEntries(t, target)
	// The entry name uses the viewer's zone (UTC+8 here): 22:13:20 UTC is 06:13:20 the next day.
	want := []string{"manifest.json", "web-01/web-01-2023-11-16-061320-WEB002.txt"}
	if !slices.Equal(names, want) {
		t.Fatalf("entries = %v, want %v", names, want)
	}
	if text := contents[want[1]]; !strings.Contains(text, "second") || !strings.Contains(text, "06:13:20") {
		t.Fatalf("text with timestamps = %q", text)
	}
	manifest := decodeArchiveManifest(t, contents["manifest.json"])
	if manifest.Filter.AssetID != "01JARCHIVEASSET000000WEB01" || manifest.Filter.StartedFrom != archiveDay1 || !manifest.Timestamps ||
		len(manifest.Transcripts) != 1 || len(manifest.Skipped) != 1 {
		t.Fatalf("manifest = %+v", manifest)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil || len(entries) != 1 {
		t.Fatalf("spool files must be removed: %v %v", entries, err)
	}
}

func TestTranscriptExportArchiveDryRunCountsByFilter(t *testing.T) {
	fixture := newArchiveFixture(t)
	cases := []struct {
		name                       string
		request                    map[string]any
		matched, exported, omitted int64
		active                     int64
	}{
		{name: "all", request: map[string]any{}, matched: 5, exported: 3, omitted: 1, active: 1},
		{name: "starred", request: map[string]any{"starredOnly": true}, matched: 1, exported: 1},
		{name: "other host", request: map[string]any{"assetId": "01JARCHIVEASSET0000000DB01"}, matched: 1, exported: 1},
		{name: "first day", request: map[string]any{"startedFrom": archiveDay0, "startedBefore": archiveDay1}, matched: 2, exported: 1, omitted: 1},
		{name: "from day nine", request: map[string]any{"startedFrom": archiveDay9}, matched: 1, exported: 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			test.request["dryRun"] = true
			result, response := fixture.export(t, fixture.ctx, test.request)
			if !response.OK {
				t.Fatalf("dry run failed: %+v", response)
			}
			if result.Matched != test.matched || result.Exported != test.exported ||
				result.Skipped.ContentOmitted != test.omitted || result.Skipped.Active != test.active || result.Bytes != 0 || result.Path != "" {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestTranscriptExportArchiveOnlyExportsTheCallersTranscripts(t *testing.T) {
	fixture := newArchiveFixture(t)
	other := ipc.WithUserID(context.Background(), "owner-b")
	target := filepath.Join(t.TempDir(), "other.zip")
	result, response := fixture.export(t, other, map[string]any{"path": target})
	if !response.OK || result.Matched != 1 || result.Exported != 1 {
		t.Fatalf("owner-b export = %+v %+v", result, response)
	}
	names, contents := readZipEntries(t, target)
	if len(names) != 2 || !strings.Contains(contents[names[1]], "foreign") {
		t.Fatalf("owner-b entries = %v", names)
	}
	manifest := decodeArchiveManifest(t, contents["manifest.json"])
	if len(manifest.Transcripts) != 1 || manifest.Transcripts[0].ID != "01JARCHIVE0000000000OTHER1" {
		t.Fatalf("owner-b manifest = %+v", manifest)
	}
	stranger := ipc.WithUserID(context.Background(), "owner-c")
	empty := filepath.Join(t.TempDir(), "empty.zip")
	if _, response := fixture.export(t, stranger, map[string]any{"path": empty}); response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("an export with nothing to write must be rejected: %+v", response)
	}
	if _, err := os.Stat(empty); err == nil {
		t.Fatal("no archive may be written when nothing matches")
	}
}

func TestTranscriptExportArchiveRejectsBadInput(t *testing.T) {
	fixture := newArchiveFixture(t)
	for name, request := range map[string]map[string]any{
		"xz":             {"dryRun": true, "format": "tar.xz"},
		"content":        {"dryRun": true, "content": "pdf"},
		"reversed range": {"dryRun": true, "startedFrom": archiveDay1, "startedBefore": archiveDay0},
		"asset id":       {"dryRun": true, "assetId": "../x"},
	} {
		if _, response := fixture.export(t, fixture.ctx, request); response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
			t.Fatalf("%s must be a bad param: %+v", name, response)
		}
	}
}

func TestTranscriptExportArchiveStopsWhenCancelled(t *testing.T) {
	fixture := newArchiveFixture(t)
	ctx, cancel := context.WithCancel(fixture.ctx)
	cancel()
	dir := t.TempDir()
	if _, response := fixture.export(t, ctx, map[string]any{"path": filepath.Join(dir, "cancelled.zip")}); response.OK {
		t.Fatalf("a cancelled export must fail: %+v", response)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("a cancelled export must leave no file: %v %v", entries, err)
	}
}

func TestTranscriptExportFileNameIsUniquePerTranscript(t *testing.T) {
	zone := time.FixedZone("UTC+8", 8*60*60)
	row := store.TranscriptRow{ID: "01JTRANSCRIPTSEED000000001", AssetName: `a/b:c`, StartedAt: archiveDay0}
	if got := transcriptExportFileName(row, zone, "cast"); got != "a_b_c-2023-11-15-061320-000001.cast" {
		t.Fatalf("name = %q", got)
	}
	if got := transcriptExportFileName(store.TranscriptRow{ID: "abc", StartedAt: archiveDay0}, time.UTC, "txt"); got != "transcript-2023-11-14-221320-abc.txt" {
		t.Fatalf("name without host = %q", got)
	}
	for _, host := range []string{"..", ".", "", " "} {
		if dir := transcriptArchiveDir(host); dir != "_" {
			t.Fatalf("dir for %q = %q", host, dir)
		}
	}
}
