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
	service    *terminalCommandService
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
	service := newTerminalCommandService(database, sessions, nil, nil, nil, nil, nil, nil, true)
	dispatcher := ipc.NewDispatcher()
	if err := service.registerTranscripts(dispatcher); err != nil {
		t.Fatal(err)
	}
	return archiveFixture{ctx: ctx, database: database, service: service, dispatcher: dispatcher, activeID: "01JARCHIVE0000000000LIVE01"}
}

func (f archiveFixture) dispatch(t *testing.T, ctx context.Context, command string, args any) ipc.Response {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return f.dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: raw}, ipc.Environment{})
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
	if result.Path != target || result.Matched != 5 || result.Exported != 4 || result.Files != 8 ||
		result.Skipped.ContentOmitted != 1 || result.Partial != 1 || result.EstimatedBytes <= 0 {
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
		"web-01/web-01-2023-11-15-221325-LIVE01.cast", "web-01/web-01-2023-11-15-221325-LIVE01.txt",
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
	// The transcript still recording is exported up to now, like a single export.
	if text := contents["web-01/web-01-2023-11-15-221325-LIVE01.txt"]; !strings.Contains(text, "live") {
		t.Fatalf("active transcript text = %q", text)
	}
	for name, content := range contents {
		if strings.Contains(content, "hunter2") || strings.Contains(content, "foreign") {
			t.Fatalf("%s carries input or a foreign transcript: %q", name, content)
		}
	}
	manifest := decodeArchiveManifest(t, contents["manifest.json"])
	if manifest.Version != 2 || len(manifest.Transcripts) != 4 || manifest.Transcripts[0].ID != "01JARCHIVE0000000000WEB001" ||
		manifest.Transcripts[0].Host != "web-01" || manifest.Transcripts[0].StartedAt != archiveDay0 ||
		manifest.Transcripts[0].EndedAt == nil || *manifest.Transcripts[0].EndedAt != archiveDay0+1000 ||
		!slices.Equal(manifest.Transcripts[0].Files, want[1:3]) || !manifest.Transcripts[1].Starred {
		t.Fatalf("manifest transcripts = %+v", manifest.Transcripts)
	}
	for _, entry := range manifest.Transcripts {
		if entry.Partial != (entry.ID == fixture.activeID) {
			t.Fatalf("only the active transcript is partial: %+v", entry)
		}
	}
	reasons := map[string]string{}
	for _, skipped := range manifest.Skipped {
		reasons[skipped.ID] = skipped.Reason
	}
	if len(reasons) != 1 || reasons["01JARCHIVE0000000000OMIT01"] != "contentOmitted" {
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
	if result.Matched != 2 || result.Exported != 2 || result.Partial != 1 || result.Skipped.ContentOmitted != 0 || result.Files != 2 {
		t.Fatalf("result = %+v", result)
	}
	names, contents := readTarGzEntries(t, target)
	// The entry name uses the viewer's zone (UTC+8 here): 22:13:20 UTC is 06:13:20 the next day.
	want := []string{"manifest.json", "web-01/web-01-2023-11-16-061320-WEB002.txt", "web-01/web-01-2023-11-16-061325-LIVE01.txt"}
	if !slices.Equal(names, want) {
		t.Fatalf("entries = %v, want %v", names, want)
	}
	if text := contents[want[1]]; !strings.Contains(text, "second") || !strings.Contains(text, "06:13:20") {
		t.Fatalf("text with timestamps = %q", text)
	}
	manifest := decodeArchiveManifest(t, contents["manifest.json"])
	if manifest.Filter.AssetID != "01JARCHIVEASSET000000WEB01" || manifest.Filter.StartedFrom != archiveDay1 || !manifest.Timestamps ||
		len(manifest.Transcripts) != 2 || len(manifest.Skipped) != 0 || !manifest.Transcripts[1].Partial {
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
		partial                    int64
	}{
		{name: "all", request: map[string]any{}, matched: 5, exported: 4, omitted: 1, partial: 1},
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
				result.Skipped.ContentOmitted != test.omitted || result.Partial != test.partial || result.Bytes != 0 || result.Path != "" {
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

func requireArchiveErrorCode(t *testing.T, err error, code ipc.Code) *ipc.Error {
	t.Helper()
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != code {
		t.Fatalf("error = %v, want code %q", err, code)
	}
	return ipcErr
}

func requireEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("%s must be left empty: %v %v", dir, entries, err)
	}
}

func TestTranscriptExportArchiveReportsProgress(t *testing.T) {
	fixture := newArchiveFixture(t)
	// A transcript over several 1 MiB pages reports progress inside the transcript, not only between transcripts.
	big := strings.Repeat("0123456789abcdef\r\n", 64*1024)
	seedArchiveTranscript(t, fixture.ctx, fixture.database, archiveSeed{
		id: "01JARCHIVE0000000000BIG001", assetID: "01JARCHIVEASSET0000000DB01", assetName: "db-01", startedAt: archiveDay9 + 1000, ended: true, output: big,
	})
	if err := fixture.database.TranscriptAppendChunks(fixture.ctx, "01JARCHIVE0000000000BIG001", []store.TranscriptChunkRow{
		{Seq: 3, TabID: "t", TS: archiveDay9 + 1100, Kind: store.TranscriptChunkKindOutput, Data: []byte(big)},
		{Seq: 4, TabID: "t", TS: archiveDay9 + 1200, Kind: store.TranscriptChunkKindOutput, Data: []byte(big)},
	}); err != nil {
		t.Fatal(err)
	}
	var updates []transcriptArchiveProgress
	target := filepath.Join(t.TempDir(), "progress.zip")
	result, err := fixture.service.exportTranscriptArchive(fixture.ctx, transcriptExportArchiveRequest{
		Path: target, Format: transcriptArchiveZip, Content: transcriptArchiveContentBoth, TimeZone: "UTC",
	}, func(progress transcriptArchiveProgress) { updates = append(updates, progress) })
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) == 0 {
		t.Fatal("no progress reported")
	}
	var previous transcriptArchiveProgress
	inside, current := false, map[string]bool{}
	for index, update := range updates {
		if !update.Running || update.Transcripts != result.Exported || update.Bytes != result.EstimatedBytes ||
			update.DoneTranscripts < previous.DoneTranscripts || update.DoneBytes < previous.DoneBytes ||
			update.DoneTranscripts > update.Transcripts || update.DoneBytes > update.Bytes {
			t.Fatalf("update %d = %+v after %+v (result %+v)", index, update, previous, result)
		}
		if update.DoneTranscripts == previous.DoneTranscripts && update.DoneBytes > previous.DoneBytes {
			inside = true
		}
		if update.Current != "" {
			current[update.Current] = true
		}
		previous = update
	}
	if previous.DoneTranscripts != result.Exported || previous.DoneBytes != result.EstimatedBytes || previous.Current != "" {
		t.Fatalf("final progress = %+v, result %+v", previous, result)
	}
	if !inside || !current["db-01/db-01-2023-11-24-221321-BIG001"] || !current["web-01/web-01-2023-11-15-221325-LIVE01"] || len(current) != int(result.Exported) {
		t.Fatalf("progress inside transcripts %v, current names %v", inside, current)
	}
}

func TestTranscriptExportArchiveCancelMidArchiveLeavesNoFile(t *testing.T) {
	for _, format := range []string{transcriptArchiveZip, transcriptArchiveTarGz} {
		t.Run(format, func(t *testing.T) {
			fixture := newArchiveFixture(t)
			tasks := newFSTransferRegistry()
			t.Cleanup(tasks.close)
			progress := newTranscriptArchiveProgressRegistry()
			const taskID = "01JARCHIVETASK000000000001"
			polled := []transcriptArchiveProgress{}
			progress.observe = func(update transcriptArchiveProgress) {
				polled = append(polled, progress.get(fixture.ctx, taskID))
				if update.DoneTranscripts == 1 {
					if ok, err := tasks.cancel(fixture.ctx, taskID, ""); !ok || err != nil {
						t.Errorf("cancel = %v %v", ok, err)
					}
				}
			}
			dir := t.TempDir()
			_, err := fixture.service.runTranscriptArchive(fixture.ctx, tasks, progress, transcriptExportArchiveRequest{
				Path: filepath.Join(dir, "cancelled."+format), Format: format, Content: transcriptArchiveContentBoth, TimeZone: "UTC", TaskID: taskID,
			})
			requireArchiveErrorCode(t, err, ipc.Code("cancelled"))
			requireEmptyDir(t, dir)
			if len(polled) == 0 {
				t.Fatal("the running export reported no progress")
			}
			for _, update := range polled {
				if !update.Running || update.TaskID != taskID || update.DoneTranscripts > 1 {
					t.Fatalf("polled progress = %+v", update)
				}
			}
			if after := progress.get(fixture.ctx, taskID); after.Running {
				t.Fatalf("a finished export must not report running: %+v", after)
			}
		})
	}
}

func TestTranscriptExportArchiveCancelAndProgressCommands(t *testing.T) {
	fixture := newArchiveFixture(t)
	const taskID = "01JARCHIVETASK000000000002"
	response := fixture.dispatch(t, fixture.ctx, "transcript_export_archive_progress", map[string]any{"taskId": taskID})
	var idle transcriptArchiveProgress
	if !response.OK || json.Unmarshal(response.Data, &idle) != nil || idle.Running || idle.TaskID != taskID {
		t.Fatalf("progress of an unknown task = %+v %+v", response, idle)
	}
	// A cancel that arrives before the export starts still stops it.
	if response := fixture.dispatch(t, fixture.ctx, "transcript_export_archive_cancel", map[string]any{"taskId": taskID}); !response.OK || string(response.Data) != "true" {
		t.Fatalf("early cancel = %+v", response)
	}
	dir := t.TempDir()
	_, response = fixture.export(t, fixture.ctx, map[string]any{"path": filepath.Join(dir, "early.zip"), "taskId": taskID})
	if response.OK || response.Error == nil || response.Error.Code != ipc.Code("cancelled") {
		t.Fatalf("an export cancelled before it started = %+v", response)
	}
	requireEmptyDir(t, dir)
	other := ipc.WithUserID(context.Background(), "owner-b")
	if response := fixture.dispatch(t, other, "transcript_export_archive_cancel", map[string]any{"taskId": taskID}); response.OK {
		t.Fatalf("another account must not cancel the task: %+v", response)
	}
	for _, bad := range []string{"", "../x", strings.Repeat("a", 200)} {
		for _, command := range []string{"transcript_export_archive_cancel", "transcript_export_archive_progress"} {
			if response := fixture.dispatch(t, fixture.ctx, command, map[string]any{"taskId": bad}); response.OK || response.Error.Code != ipc.CodeBadParam {
				t.Fatalf("%s with task id %q = %+v", command, bad, response)
			}
		}
		if bad == "" {
			continue
		}
		if _, response := fixture.export(t, fixture.ctx, map[string]any{"path": filepath.Join(dir, "bad.zip"), "taskId": bad}); response.OK || response.Error.Code != ipc.CodeBadParam {
			t.Fatalf("export with task id %q = %+v", bad, response)
		}
	}
	// With a fresh task id the export runs to the end.
	if result, response := fixture.export(t, fixture.ctx, map[string]any{"path": filepath.Join(dir, "done.zip"), "taskId": "01JARCHIVETASK000000000003"}); !response.OK || result.Exported != 4 {
		t.Fatalf("export = %+v %+v", result, response)
	}
}

func TestTranscriptExportArchiveKeepsTextThatGrowsPastTheRecording(t *testing.T) {
	fixture := newArchiveFixture(t)
	const id, assetID = "01JARCHIVE0000000000WIDE01", "01JARCHIVEASSET00000WIDE01"
	// Each 9-byte line moves the cursor to the end of a 1024-column row and prints: about 114 text bytes per recorded byte,
	// which the removed guard (1 MiB + 32x the input) refused and which now exports in full.
	line := "\x1b[1024Gx\n"
	payload := []byte(strings.Repeat(line, 1000))
	if err := fixture.database.TranscriptStart(fixture.ctx, store.TranscriptRow{ID: id, SessionID: "wide", AssetID: assetID, AssetName: "wide", StartedAt: archiveDay0}); err != nil {
		t.Fatal(err)
	}
	chunks := []store.TranscriptChunkRow{{Seq: 0, TabID: "t", TS: archiveDay0, Kind: store.TranscriptChunkKindResize, Data: []byte(`{"cols":1024,"rows":24}`)}}
	for seq := int64(1); seq <= 5; seq++ {
		chunks = append(chunks, store.TranscriptChunkRow{Seq: seq, TabID: "t", TS: archiveDay0 + seq, Data: payload})
	}
	if err := fixture.database.TranscriptAppendChunks(fixture.ctx, id, chunks); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.TranscriptEnd(fixture.ctx, id, archiveDay0+1000, false); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "wide.zip")
	result, response := fixture.export(t, fixture.ctx, map[string]any{"path": target, "content": "text", "assetId": assetID})
	if !response.OK || result.Exported != 1 || result.Skipped.ContentOmitted != 0 {
		t.Fatalf("export = %+v %+v", result, response)
	}
	_, contents := readZipEntries(t, target)
	text := contents["wide/wide-2023-11-14-221320-WIDE01.txt"]
	input := int64(5 * len(payload))
	if int64(len(text)) <= 1<<20+32*input || strings.Count(text, "x\n") != 5000 {
		t.Fatalf("text has %d bytes and %d lines for %d recorded bytes", len(text), strings.Count(text, "x\n"), input)
	}
}

func TestTranscriptExportArchiveStopsActiveTranscriptWhereTheExportStarted(t *testing.T) {
	fixture := newArchiveFixture(t)
	target := filepath.Join(t.TempDir(), "live.zip")
	appended := false
	result, err := fixture.service.exportTranscriptArchive(fixture.ctx, transcriptExportArchiveRequest{
		Path: target, Content: transcriptArchiveContentBoth, AssetID: "01JARCHIVEASSET000000WEB01", StartedFrom: archiveDay1, TimeZone: "UTC",
	}, func(transcriptArchiveProgress) {
		if appended {
			return
		}
		appended = true
		// Output recorded after the export listed the transcript is not part of the archive.
		if err := fixture.database.TranscriptAppendChunks(fixture.ctx, fixture.activeID, []store.TranscriptChunkRow{
			{Seq: 3, TabID: "t", TS: archiveDay1 + 9000, Kind: store.TranscriptChunkKindOutput, Data: []byte("later\r\n")},
		}); err != nil {
			t.Error(err)
		}
	})
	if err != nil || result.Partial != 1 || result.Exported != 2 {
		t.Fatalf("export = %+v %v", result, err)
	}
	_, contents := readZipEntries(t, target)
	for _, name := range []string{"web-01/web-01-2023-11-15-221325-LIVE01.cast", "web-01/web-01-2023-11-15-221325-LIVE01.txt"} {
		if content := contents[name]; !strings.Contains(content, "live") || strings.Contains(content, "later") {
			t.Fatalf("%s = %q", name, content)
		}
	}
}

func TestTranscriptExportArchiveStopsAtTheStagingBudget(t *testing.T) {
	for _, format := range []string{transcriptArchiveZip, transcriptArchiveTarGz} {
		t.Run(format, func(t *testing.T) {
			fixture := newArchiveFixture(t)
			dir := t.TempDir()
			fixture.service.stagedBlobs = &fakeStagedBlobResolver{dir: dir, writeLimit: 64}
			_, err := fixture.service.exportTranscriptArchive(fixture.ctx, transcriptExportArchiveRequest{
				Path: "staged-id/history." + format, Format: format, Content: transcriptArchiveContentBoth, TimeZone: "UTC",
			}, nil)
			ipcErr := requireArchiveErrorCode(t, err, ipc.CodeIO)
			if !strings.Contains(ipcErr.Message, "暂存大小上限") {
				t.Fatalf("message = %q", ipcErr.Message)
			}
			requireEmptyDir(t, dir)
		})
	}
}
