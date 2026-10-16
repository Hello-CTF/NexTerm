package store

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func startTranscript(t *testing.T, db *Store, assetID string, startedAt int64) string {
	t.Helper()
	id := ids.New()
	if err := db.TranscriptStart(context.Background(), TranscriptRow{
		ID: id, SessionID: ids.New(), AssetID: assetID, AssetName: "web-01", AssetKind: "ssh", StartedAt: startedAt,
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func appendTranscriptChunks(t *testing.T, db *Store, id string, chunks ...TranscriptChunkRow) {
	t.Helper()
	if err := db.TranscriptAppendChunks(context.Background(), id, chunks); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptLifecycleAndPagination(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)

	appendTranscriptChunks(t, db, id,
		TranscriptChunkRow{Seq: 0, TabID: "tab-a", TS: 1001, Data: []byte("hello ")},
		TranscriptChunkRow{Seq: 1, TabID: "tab-a", TS: 1002, Data: []byte("world")},
	)

	row, err := db.TranscriptGet(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Bytes != 11 || row.Chunks != 2 || row.EndedAt != nil || row.Truncated {
		t.Fatalf("unexpected transcript row: %+v", row)
	}

	page, err := db.TranscriptChunks(ctx, id, 0, 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].Seq != 0 || string(page[0].Data) != "hello " || page[1].Seq != 1 {
		t.Fatalf("unexpected first page: %+v", page)
	}
	page, err = db.TranscriptChunks(ctx, id, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].Seq != 0 {
		t.Fatalf("byte budget must stop the page after the first chunk: %+v", page)
	}
	page, err = db.TranscriptChunks(ctx, id, 1, 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].Seq != 1 {
		t.Fatalf("unexpected second page: %+v", page)
	}
	page, err = db.TranscriptChunks(ctx, id, 2, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 0 {
		t.Fatalf("expected empty page after last seq, got %+v", page)
	}

	if err := db.TranscriptEnd(ctx, id, 2000, false); err != nil {
		t.Fatal(err)
	}
	row, err = db.TranscriptGet(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.EndedAt == nil || *row.EndedAt != 2000 {
		t.Fatalf("expected ended_at=2000, got %+v", row.EndedAt)
	}
	if err := db.TranscriptEnd(ctx, id, 3000, true); err != nil {
		t.Fatal(err)
	}
	row, err = db.TranscriptGet(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if *row.EndedAt != 2000 || row.Truncated {
		t.Fatalf("second end must be a no-op: %+v", row)
	}
}

func TestTranscriptListByAssetChronologicalAndIsolated(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	old := startTranscript(t, db, "asset-1", 1000)
	recent := startTranscript(t, db, "asset-1", 3000)
	other := startTranscript(t, db, "asset-2", 2000)

	rows, err := db.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != recent || rows[1].ID != old {
		t.Fatalf("expected [recent old], got %+v", rows)
	}
	rows, err = db.TranscriptListByAsset(ctx, "asset-2", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != other {
		t.Fatalf("expected [other], got %+v", rows)
	}
}

func TestTranscriptSearchMatchesAcrossChunks(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendTranscriptChunks(t, db, id,
		TranscriptChunkRow{Seq: 0, TabID: "tab", TS: 1001, Data: []byte("error: disk full\nerror: retrying\n")},
		TranscriptChunkRow{Seq: 1, TabID: "tab", TS: 1002, Data: []byte("done\n")},
	)

	matches, err := db.TranscriptSearch(ctx, id, []byte("error"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].Seq != 0 || matches[1].Seq != 0 {
		t.Fatalf("expected two matches in chunk 0, got %+v", matches)
	}
	if !bytes.Contains(matches[0].Preview, []byte("error: disk full")) {
		t.Fatalf("unexpected preview %q", matches[0].Preview)
	}
	matches, err = db.TranscriptSearch(ctx, id, []byte("done"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Seq != 1 {
		t.Fatalf("expected one match in chunk 1, got %+v", matches)
	}
	matches, err = db.TranscriptSearch(ctx, id, []byte("missing"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no matches, got %+v", matches)
	}
	_, err = db.TranscriptSearch(ctx, id, nil, 0, 0)
	requireCode(t, err, ipc.CodeBadParam)
}

func TestTranscriptDeleteCascadesChunks(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendTranscriptChunks(t, db, id, TranscriptChunkRow{Seq: 0, TabID: "tab", TS: 1001, Data: []byte("data")})

	if err := db.TranscriptDelete(ctx, id); err != nil {
		t.Fatal(err)
	}
	requireTableCount(t, db, "transcript", 0)
	requireTableCount(t, db, "transcript_chunk", 0)
	requireCode(t, db.TranscriptDelete(ctx, id), ipc.CodeNotFound)
	_, err := db.TranscriptGet(ctx, id)
	requireCode(t, err, ipc.CodeNotFound)
}

func TestTranscriptRetentionAgeAndCount(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	now := time.Now().UnixMilli()
	oldEnded := startTranscript(t, db, "asset-1", now-72*time.Hour.Milliseconds())
	if err := db.TranscriptEnd(ctx, oldEnded, now-48*time.Hour.Milliseconds(), false); err != nil {
		t.Fatal(err)
	}
	recentEnded := startTranscript(t, db, "asset-1", now-time.Hour.Milliseconds())
	if err := db.TranscriptEnd(ctx, recentEnded, now, false); err != nil {
		t.Fatal(err)
	}
	active := startTranscript(t, db, "asset-1", now-72*time.Hour.Milliseconds())

	result, err := db.EnforceTranscriptRetention(ctx, TranscriptRetentionPolicy{MaxAge: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 {
		t.Fatalf("expected age retention to delete 1, got %+v", result)
	}
	if _, err := db.TranscriptGet(ctx, oldEnded); err == nil {
		t.Fatal("old ended transcript must be deleted")
	}
	if _, err := db.TranscriptGet(ctx, recentEnded); err != nil {
		t.Fatal("recent ended transcript must be kept")
	}
	if _, err := db.TranscriptGet(ctx, active); err != nil {
		t.Fatal("active transcript must be kept by age retention")
	}

	for index := 0; index < 3; index++ {
		id := startTranscript(t, db, "asset-1", now+int64(index))
		if err := db.TranscriptEnd(ctx, id, now+int64(index), false); err != nil {
			t.Fatal(err)
		}
	}
	result, err = db.EnforceTranscriptRetention(ctx, TranscriptRetentionPolicy{MaxCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 2 {
		t.Fatalf("expected count retention to delete 2, got %+v", result)
	}
	rows, err := db.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected active + 2 newest ended, got %d", len(rows))
	}
	if _, err := db.EnforceTranscriptRetention(ctx, TranscriptRetentionPolicy{MaxAge: -1}); err == nil {
		t.Fatal("negative max age must be rejected")
	}
	if _, err := db.EnforceTranscriptRetention(ctx, TranscriptRetentionPolicy{MaxCount: -1}); err == nil {
		t.Fatal("negative max count must be rejected")
	}
}

func TestTranscriptDatabaseFilePermissions(t *testing.T) {
	if !ownerOnlyPermissionsSupported() {
		t.Skip("owner-only permissions are not supported on this platform")
	}
	db, path := fileStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendTranscriptChunks(t, db, id, TranscriptChunkRow{Seq: 0, TabID: "tab", TS: 1001, Data: []byte("secret output")})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("%s permission %o, want 600", name, perm)
		}
	}
}

func TestTranscriptSearchAcrossChunkBoundaries(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendTranscriptChunks(t, db, id,
		TranscriptChunkRow{Seq: 0, TabID: "tab", TS: 1001, Data: []byte("hel")},
		TranscriptChunkRow{Seq: 1, TabID: "tab", TS: 1002, Data: []byte("lo world\r\n")},
	)

	matches, err := db.TranscriptSearch(ctx, id, []byte("hello"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Seq != 0 {
		t.Fatalf("query spanning a chunk boundary must match once at seq 0: %+v", matches)
	}
	if !bytes.Contains(matches[0].Preview, []byte("hello world")) {
		t.Fatalf("unexpected preview %q", matches[0].Preview)
	}
	matches, err = db.TranscriptSearch(ctx, id, []byte("world"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Seq != 1 {
		t.Fatalf("query inside the second chunk must match at seq 1: %+v", matches)
	}
}

func TestTranscriptSearchAcrossChunkUTF8AndANSI(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	nihao := []byte("你好")
	appendTranscriptChunks(t, db, id,
		TranscriptChunkRow{Seq: 0, TabID: "tab", TS: 1001, Data: append([]byte("say "), nihao[0])},
		TranscriptChunkRow{Seq: 1, TabID: "tab", TS: 1002, Data: append([]byte{nihao[1], nihao[2]}, []byte("好\r\nerr\x1b[3")...)},
		TranscriptChunkRow{Seq: 2, TabID: "tab", TS: 1003, Data: []byte("1mor\x1b[0m done\r\n")},
	)

	matches, err := db.TranscriptSearch(ctx, id, []byte("你好"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Seq != 1 {
		t.Fatalf("utf-8 rune split across chunks must match once it becomes visible: %+v", matches)
	}
	matches, err = db.TranscriptSearch(ctx, id, []byte("error"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Seq != 1 {
		t.Fatalf("ansi sequence split inside a word must still match the visible text: %+v", matches)
	}
	if !bytes.Contains(matches[0].Preview, []byte("error done")) {
		t.Fatalf("unexpected preview %q", matches[0].Preview)
	}
	if bytes.Contains(matches[0].Preview, []byte{0x1b}) {
		t.Fatalf("preview must be visible text without escape bytes: %q", matches[0].Preview)
	}
	matches, err = db.TranscriptSearch(ctx, id, []byte("\x1b[31m"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("escape sequences must not be searchable as visible text: %+v", matches)
	}
}

func TestTranscriptSearchBudgetsAndOrder(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	chunks := make([]TranscriptChunkRow, 0, 8)
	for index := 0; index < 8; index++ {
		chunks = append(chunks, TranscriptChunkRow{
			Seq: int64(index), TabID: "tab", TS: 1000 + int64(index),
			Data: []byte("needle "),
		})
	}
	appendTranscriptChunks(t, db, id, chunks...)

	matches, err := db.TranscriptSearch(ctx, id, []byte("needle"), 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 3 {
		t.Fatalf("maxMatches must bound the result: %+v", matches)
	}
	for index, match := range matches {
		if match.Seq != int64(index) {
			t.Fatalf("matches must stay chronological: %+v", matches)
		}
	}
	matches, err = db.TranscriptSearch(ctx, id, []byte("needle"), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("scan budget must bound the scan: %+v", matches)
	}
	if _, err := db.TranscriptSearch(ctx, id, bytes.Repeat([]byte("q"), 300), 0, 0); err == nil {
		t.Fatal("over-long queries must be rejected")
	}
}

func TestTranscriptHostsIncludeDeletedAssets(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	live, err := db.AssetCreate(ctx, AssetInput{Kind: "ssh", Name: "live-host"})
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := db.AssetCreate(ctx, AssetInput{Kind: "ssh", Name: "deleted-host"})
	if err != nil {
		t.Fatal(err)
	}
	gone := startTranscript(t, db, deleted.ID, 1000)
	if err := db.TranscriptEnd(ctx, gone, 2000, false); err != nil {
		t.Fatal(err)
	}
	kept := startTranscript(t, db, live.ID, 3000)
	if err := db.TranscriptEnd(ctx, kept, 4000, false); err != nil {
		t.Fatal(err)
	}
	orphan := startTranscript(t, db, "01J0NEXTERMMISSINGHOST000001", 2000)
	if err := db.TranscriptEnd(ctx, orphan, 2500, false); err != nil {
		t.Fatal(err)
	}
	deviceAssetID := "device/01JDEVICE00000000000000001"
	device := startTranscript(t, db, deviceAssetID, 1500)
	if err := db.TranscriptEnd(ctx, device, 1800, false); err != nil {
		t.Fatal(err)
	}
	if err := db.AssetDelete(ctx, deleted.ID); err != nil {
		t.Fatal(err)
	}

	hosts, err := db.TranscriptHosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 4 {
		t.Fatalf("expected 4 transcript hosts, got %+v", hosts)
	}
	byID := make(map[string]TranscriptHostRow, len(hosts))
	for _, host := range hosts {
		byID[host.AssetID] = host
	}
	if host := byID[live.ID]; host.AssetDeleted || host.AssetName != "web-01" || host.Transcripts != 1 || host.AssetKind != "ssh" {
		t.Fatalf("live host row wrong: %+v", host)
	}
	if host := byID[deleted.ID]; !host.AssetDeleted || host.Transcripts != 1 {
		t.Fatalf("soft-deleted host must stay listed as deleted: %+v", host)
	}
	if host := byID["01J0NEXTERMMISSINGHOST000001"]; !host.AssetDeleted || host.AssetName != "web-01" {
		t.Fatalf("hard-missing host must be listed from the snapshot: %+v", host)
	}
	if host, ok := byID[deviceAssetID]; !ok || host.AssetDeleted || host.Transcripts != 1 {
		t.Fatalf("device pseudo asset must not be marked deleted: %+v", host)
	}
	if hosts[0].AssetID != live.ID {
		t.Fatalf("hosts must be ordered by last activity: %+v", hosts)
	}
}

func TestTranscriptSearchCharsetEscapeInsideWord(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendTranscriptChunks(t, db, id,
		TranscriptChunkRow{Seq: 0, TabID: "tab", TS: 1001, Data: []byte("hel\x1b(B")},
		TranscriptChunkRow{Seq: 1, TabID: "tab", TS: 1002, Data: []byte("lo visible\r\n")},
	)

	matches, err := db.TranscriptSearch(ctx, id, []byte("hello"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Seq != 0 {
		t.Fatalf("charset sequence inside a word must not break the visible match: %+v", matches)
	}
	if bytes.Contains(matches[0].Preview, []byte("(B")) {
		t.Fatalf("charset sequence payload must not leak into previews: %q", matches[0].Preview)
	}

	single := startTranscript(t, db, "asset-1", 2000)
	appendTranscriptChunks(t, db, single,
		TranscriptChunkRow{Seq: 0, TabID: "tab", TS: 2001, Data: []byte("plain\x1b(0text\x1b(B\r\n")},
	)
	matches, err = db.TranscriptSearch(ctx, single, []byte("plaintext"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("single-chunk charset sequences must be consumed whole: %+v", matches)
	}
	matches, err = db.TranscriptSearch(ctx, single, []byte("(0"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("charset designation must not be searchable: %+v", matches)
	}
}

func TestDurableTranscriptOffsetRoundtrip(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	offset, err := db.DurableTranscriptOffsetGet(ctx, "tab-missing")
	if err != nil || offset != 0 {
		t.Fatalf("missing offset = %d err=%v", offset, err)
	}
	if err := db.DurableTranscriptOffsetSet(ctx, "tab-1", 4096); err != nil {
		t.Fatal(err)
	}
	if err := db.DurableTranscriptOffsetSet(ctx, "tab-1", 8192); err != nil {
		t.Fatal(err)
	}
	offset, err = db.DurableTranscriptOffsetGet(ctx, "tab-1")
	if err != nil || offset != 8192 {
		t.Fatalf("offset = %d err=%v", offset, err)
	}
	if err := db.DurableTranscriptOffsetDelete(ctx, "tab-1"); err != nil {
		t.Fatal(err)
	}
	offset, err = db.DurableTranscriptOffsetGet(ctx, "tab-1")
	if err != nil || offset != 0 {
		t.Fatalf("offset after delete = %d err=%v", offset, err)
	}
}

func TestDurableTabTitleRoundtrip(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	title, err := db.DurableTabTitleGet(ctx, "tab-missing")
	if err != nil || title != "" {
		t.Fatalf("missing title = %q err=%v", title, err)
	}
	if err := db.DurableTabTitleSet(ctx, "tab-1", "构建机"); err != nil {
		t.Fatal(err)
	}
	if err := db.DurableTabTitleSet(ctx, "tab-1", "部署终端"); err != nil {
		t.Fatal(err)
	}
	title, err = db.DurableTabTitleGet(ctx, "tab-1")
	if err != nil || title != "部署终端" {
		t.Fatalf("title = %q err=%v", title, err)
	}
	if err := db.DurableTabTitleDelete(ctx, "tab-1"); err != nil {
		t.Fatal(err)
	}
	title, err = db.DurableTabTitleGet(ctx, "tab-1")
	if err != nil || title != "" {
		t.Fatalf("title after delete = %q err=%v", title, err)
	}
}

func TestDurableTranscriptOffsetAppendAccumulates(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	if err := db.TranscriptAppendChunks(ctx, id,
		[]TranscriptChunkRow{{Seq: 1, TabID: "tab-1", TS: 1001, Data: []byte("abcd")}},
		map[string]int64{"durable-1": 4}); err != nil {
		t.Fatal(err)
	}
	if offset, err := db.DurableTranscriptOffsetGet(ctx, "durable-1"); err != nil || offset != 4 {
		t.Fatalf("offset after insert = %d err=%v", offset, err)
	}
	if err := db.TranscriptAppendChunks(ctx, id,
		[]TranscriptChunkRow{{Seq: 2, TabID: "tab-1", TS: 1002, Data: []byte("efgh")}},
		map[string]int64{"durable-1": 4, "durable-2": 3}); err != nil {
		t.Fatal(err)
	}
	if offset, err := db.DurableTranscriptOffsetGet(ctx, "durable-1"); err != nil || offset != 8 {
		t.Fatalf("offset after conflict accumulate = %d err=%v", offset, err)
	}
	if offset, err := db.DurableTranscriptOffsetGet(ctx, "durable-2"); err != nil || offset != 3 {
		t.Fatalf("offset for second durable = %d err=%v", offset, err)
	}
}

func TestTranscriptSearchControlStrings(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendTranscriptChunks(t, db, id,
		TranscriptChunkRow{Seq: 0, TabID: "tab", TS: 1001, Data: []byte("hel\x1bP1;2|not-visible-payload\x1b\\lo visible\r\n")},
		TranscriptChunkRow{Seq: 1, TabID: "tab", TS: 1002, Data: []byte("apc\x1b_secret-apc\x1b\\done\r\n")},
		TranscriptChunkRow{Seq: 2, TabID: "tab", TS: 1003, Data: []byte("sos\x1bXsecret-sos\x1b\\done\r\n")},
		TranscriptChunkRow{Seq: 3, TabID: "tab", TS: 1004, Data: []byte("pm\x1b^secret-pm\x1b\\done\r\n")},
		TranscriptChunkRow{Seq: 4, TabID: "tab", TS: 1005, Data: []byte("c1\x90secret-c1\x9cdone\r\n")},
	)

	for query, wantSeq := range map[string]int64{
		"hello visible": 0,
		"apcdone":       1,
		"sosdone":       2,
		"pmdone":        3,
		"c1done":        4,
	} {
		matches, err := db.TranscriptSearch(ctx, id, []byte(query), 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 1 || matches[0].Seq != wantSeq {
			t.Fatalf("query %q: matches=%+v, want one at seq %d", query, matches, wantSeq)
		}
	}
	for _, query := range []string{
		"not-visible-payload", "secret-apc", "secret-sos", "secret-pm", "secret-c1", "1;2",
	} {
		matches, err := db.TranscriptSearch(ctx, id, []byte(query), 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 0 {
			t.Fatalf("control-string payload %q must not be searchable: %+v", query, matches)
		}
	}
}

func TestTranscriptSearchControlStringAcrossChunks(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	appendTranscriptChunks(t, db, id,
		TranscriptChunkRow{Seq: 0, TabID: "tab", TS: 1001, Data: []byte("hel\x1bP1;2|pay")},
		TranscriptChunkRow{Seq: 1, TabID: "tab", TS: 1002, Data: []byte("load\x1b\\lo visible\r\n")},
	)
	matches, err := db.TranscriptSearch(ctx, id, []byte("hello"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Seq != 0 {
		t.Fatalf("DCS split across chunks must still match the visible word: %+v", matches)
	}
	matches, err = db.TranscriptSearch(ctx, id, []byte("payload"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("split DCS payload must not be searchable: %+v", matches)
	}
}

func TestVisibleBytesC1GoldenParity(t *testing.T) {
	for name, raw := range map[string][]byte{
		"raw C1 DCS":    append(append(append([]byte("hel"), 0x90), []byte("secret-c1")...), append([]byte{0x9c}, []byte("lo visible")...)...),
		"utf8 C1 DCS":   []byte("hel\xc2\x90secret-c1\xc2\x9clo visible"),
		"raw C1 APC":    append(append(append([]byte("hel"), 0x9f), []byte("secret-c1")...), append([]byte{0x9c}, []byte("lo visible")...)...),
		"utf8 C1 APC":   []byte("hel\xc2\x9fsecret-c1\xc2\x9clo visible"),
		"raw C1 SOS":    append(append(append([]byte("hel"), 0x98), []byte("secret-c1")...), append([]byte{0x9c}, []byte("lo visible")...)...),
		"utf8 C1 SOS":   []byte("hel\xc2\x98secret-c1\xc2\x9clo visible"),
		"raw C1 PM":     append(append(append([]byte("hel"), 0x9e), []byte("secret-c1")...), append([]byte{0x9c}, []byte("lo visible")...)...),
		"utf8 C1 PM":    []byte("hel\xc2\x9esecret-c1\xc2\x9clo visible"),
		"seven bit DCS": []byte("hel\x1bPsecret-c1\x1b\\lo visible"),
		"seven bit APC": []byte("hel\x1b_secret-c1\x1b\\lo visible"),
		"seven bit SOS": []byte("hel\x1bXsecret-c1\x1b\\lo visible"),
		"seven bit PM":  []byte("hel\x1b^secret-c1\x1b\\lo visible"),
		"charset ESC(B": []byte("hel\x1b(Blo visible"),
	} {
		visible, tail := visibleBytes(raw)
		if string(visible) != "hello visible" || len(tail) != 0 {
			t.Fatalf("%s: visible=%q tail=%q, want %q", name, visible, tail, "hello visible")
		}
		matcher := newVisibleMatcher([]byte("hello"), 10)
		matcher.consume(0, 1, raw)
		if len(matcher.matches) != 1 {
			t.Fatalf("%s: word across control string must match, matches=%d", name, len(matcher.matches))
		}
		payload := newVisibleMatcher([]byte("secret-c1"), 10)
		payload.consume(0, 1, raw)
		if len(payload.matches) != 0 {
			t.Fatalf("%s: payload leaked into searchable text: %+v", name, payload.matches)
		}
	}
}

func TestVisibleBytesC1GoldenParityAcrossChunks(t *testing.T) {
	for name, chunks := range map[string][][]byte{
		"raw C1 split":  {[]byte("hel\x90pay"), []byte("load\x9clo visible")},
		"utf8 C1 split": {[]byte("hel\xc2\x90pay"), []byte("load\xc2\x9clo visible")},
	} {
		matcher := newVisibleMatcher([]byte("hello"), 10)
		for index, chunk := range chunks {
			matcher.consume(int64(index), int64(index+1), chunk)
		}
		if len(matcher.matches) != 1 {
			t.Fatalf("%s: split control string must still match: %+v", name, matcher.matches)
		}
		payload := newVisibleMatcher([]byte("payload"), 10)
		for index, chunk := range chunks {
			payload.consume(int64(index), int64(index+1), chunk)
		}
		if len(payload.matches) != 0 {
			t.Fatalf("%s: split payload leaked: %+v", name, payload.matches)
		}
	}
}

func TestTranscriptChunkKindRoundtripAndSearchOnlyOutput(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)

	appendTranscriptChunks(t, db, id,
		TranscriptChunkRow{Seq: 0, TabID: "tab", TS: 1001, Kind: TranscriptChunkKindOutput, Data: []byte("visible ")},
		TranscriptChunkRow{Seq: 1, TabID: "tab", TS: 1002, Kind: TranscriptChunkKindInput, Data: []byte("secret-input ")},
		TranscriptChunkRow{Seq: 2, TabID: "tab", TS: 1003, Kind: TranscriptChunkKindResize, Data: []byte(`{"cols":80,"rows":24}`)},
		TranscriptChunkRow{Seq: 3, TabID: "tab", TS: 1004, Data: []byte("tail")},
	)

	chunks, err := db.TranscriptChunks(ctx, id, 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 4 {
		t.Fatalf("expected 4 chunks, got %+v", chunks)
	}
	wantKinds := []int{TranscriptChunkKindOutput, TranscriptChunkKindInput, TranscriptChunkKindResize, TranscriptChunkKindOutput}
	for index, chunk := range chunks {
		if chunk.Kind != wantKinds[index] {
			t.Fatalf("chunk %d kind = %d, want %d", index, chunk.Kind, wantKinds[index])
		}
	}

	matches, err := db.TranscriptSearch(ctx, id, []byte("secret-input"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("search must not scan input chunks: %+v", matches)
	}
	matches, err = db.TranscriptSearch(ctx, id, []byte("cols"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("search must not scan resize chunks: %+v", matches)
	}
	matches, err = db.TranscriptSearch(ctx, id, []byte("visible"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Seq != 0 {
		t.Fatalf("output chunks must stay searchable: %+v", matches)
	}
}

func TestTranscriptSyncedContentPreservesKind(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	ended := ids.NowMS() - 1000
	row := TranscriptRow{
		ID: ids.New(), SessionID: ids.New(), AssetID: "asset-9", AssetName: "db-01", AssetKind: "ssh",
		StartedAt: ended - 500, EndedAt: &ended, ContentOmitted: true,
	}
	content := []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-1", TS: ended - 400, Kind: TranscriptChunkKindOutput, Data: []byte("out")},
		{Seq: 1, TabID: "tab-1", TS: ended - 300, Kind: TranscriptChunkKindInput, Data: []byte("in")},
		{Seq: 2, TabID: "tab-1", TS: ended - 200, Kind: TranscriptChunkKindResize, Data: []byte(`{"cols":80,"rows":24}`)},
	}
	if err := db.TranscriptInsertSynced(ctx, row, content); err != nil {
		t.Fatal(err)
	}
	chunks, err := db.TranscriptChunks(ctx, row.ID, 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 {
		t.Fatalf("expected 3 synced chunks, got %+v", chunks)
	}
	for index, want := range []int{TranscriptChunkKindOutput, TranscriptChunkKindInput, TranscriptChunkKindResize} {
		if chunks[index].Kind != want {
			t.Fatalf("synced chunk %d kind = %d, want %d", index, chunks[index].Kind, want)
		}
	}

	replacement := []TranscriptChunkRow{
		{Seq: 0, TabID: "tab-1", TS: ended - 400, Kind: TranscriptChunkKindInput, Data: []byte("in2")},
		{Seq: 1, TabID: "tab-1", TS: ended - 300, Kind: TranscriptChunkKindOutput, Data: []byte("out2")},
	}
	if err := db.TranscriptReplaceContent(ctx, row.ID, 5, 2, false, replacement); err != nil {
		t.Fatal(err)
	}
	chunks, err = db.TranscriptChunks(ctx, row.ID, 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 || chunks[0].Kind != TranscriptChunkKindInput || chunks[1].Kind != TranscriptChunkKindOutput {
		t.Fatalf("replaced content lost kinds: %+v", chunks)
	}
}

func TestTranscriptStorageUsesSolidCompressedBlocks(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	payload := bytes.Repeat([]byte("terminal-output-你好\r\n"), 32)
	original := make([]TranscriptChunkRow, 0, 600)
	var rawBytes int64
	for index := 0; index < 600; index++ {
		kind := TranscriptChunkKindOutput
		data := payload
		if index%101 == 0 {
			kind = TranscriptChunkKindResize
			data = []byte(`{"cols":120,"rows":40}`)
		} else if index%97 == 0 {
			kind = TranscriptChunkKindInput
			data = []byte("secret-input\r")
		}
		tabID := "tab-a"
		if index%2 == 1 {
			tabID = "tab-b"
		}
		original = append(original, TranscriptChunkRow{
			Seq: int64(index), TabID: tabID, TS: int64(1001 + index), Kind: kind, Data: data,
		})
		rawBytes += int64(len(data))
	}
	appendTranscriptChunks(t, db, id, original...)

	row, err := db.TranscriptGet(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Bytes != rawBytes || row.Chunks != int64(len(original)) {
		t.Fatalf("logical counters changed by compression: %+v", row)
	}
	if row.StoredBytes <= 0 || row.StoredBytes >= row.Bytes {
		t.Fatalf("compressed storage must be smaller: raw=%d stored=%d", row.Bytes, row.StoredBytes)
	}

	stored := []TranscriptChunkRow{}
	if err := db.db.WithContext(ctx).Where("transcript_id = ?", id).Order("seq").Find(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if len(stored) != 3 {
		t.Fatalf("600 events must form three solid blocks, got %d rows", len(stored))
	}
	var storedBytes int64
	for _, block := range stored {
		storedBytes += int64(len(block.Data))
		if block.Kind != transcriptStoredBlockKind {
			t.Fatalf("stored row kind = %d, want compressed block marker", block.Kind)
		}
		if len(block.Data) < 4 || !bytes.Equal(block.Data[:4], []byte{0x28, 0xb5, 0x2f, 0xfd}) {
			t.Fatalf("stored block is not a zstd frame: %x", block.Data[:4])
		}
	}
	if storedBytes != row.StoredBytes {
		t.Fatalf("stored byte counter = %d, actual compressed payload = %d", row.StoredBytes, storedBytes)
	}

	decoded, err := db.TranscriptChunks(ctx, id, 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(original) {
		t.Fatalf("decoded %d events, want %d", len(decoded), len(original))
	}
	for index := range original {
		if decoded[index].Seq != original[index].Seq || decoded[index].TabID != original[index].TabID ||
			decoded[index].TS != original[index].TS || decoded[index].Kind != original[index].Kind ||
			!bytes.Equal(decoded[index].Data, original[index].Data) {
			t.Fatalf("event %d changed during compression round trip: got=%+v want=%+v", index, decoded[index], original[index])
		}
	}

	page, err := db.TranscriptChunks(ctx, id, 100, 1)
	if err != nil || len(page) != 1 || page[0].Seq != 100 {
		t.Fatalf("read inside a compressed block: chunks=%+v err=%v", page, err)
	}
	remainder, err := db.TranscriptChunks(ctx, id, 101, 1<<20)
	if err != nil || len(remainder) != 499 || remainder[0].Seq != 101 || remainder[len(remainder)-1].Seq != 599 {
		t.Fatalf("resume after an in-block page failed: count=%d err=%v", len(remainder), err)
	}
}

func TestTranscriptRejectsLegacyRawChunks(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	legacy := TranscriptChunkRow{TranscriptID: id, Seq: 0, TabID: "tab", TS: 1001, Kind: TranscriptChunkKindOutput, Data: []byte("legacy output")}
	if err := db.db.WithContext(ctx).Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := db.TranscriptChunks(ctx, id, 0, 1024); err == nil {
		t.Fatal("legacy raw chunks must not be read as compressed blocks")
	} else {
		requireCode(t, err, ipc.CodeUnsupported)
	}
	if _, err := db.TranscriptSearch(ctx, id, []byte("legacy"), 0, 0); err == nil {
		t.Fatal("legacy raw chunks must not be searched as compressed blocks")
	} else {
		requireCode(t, err, ipc.CodeUnsupported)
	}
}

func TestTranscriptSearchAcrossCompressedBlockBoundaries(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	chunks := make([]TranscriptChunkRow, 513)
	for index := range chunks {
		chunks[index] = TranscriptChunkRow{Seq: int64(index), TabID: "tab", TS: int64(1001 + index), Data: []byte("filler\r\n")}
	}
	chunks[255].Data = []byte("hel")
	chunks[256].Data = []byte("lo visible\r\n")
	nihao := []byte("你好")
	chunks[511].Data = append([]byte("say "), nihao[0])
	chunks[512].Data = append([]byte{nihao[1], nihao[2]}, []byte("好\r\n")...)
	appendTranscriptChunks(t, db, id, chunks...)

	matches, err := db.TranscriptSearch(ctx, id, []byte("hello"), 0, 0)
	if err != nil || len(matches) != 1 || matches[0].Seq != 255 {
		t.Fatalf("ASCII match across compressed blocks: matches=%+v err=%v", matches, err)
	}
	matches, err = db.TranscriptSearch(ctx, id, []byte("你好"), 0, 0)
	if err != nil || len(matches) != 1 || matches[0].Seq != 512 {
		t.Fatalf("UTF-8 match across compressed blocks: matches=%+v err=%v", matches, err)
	}
}

func TestTranscriptSearchPaginatesStoredBlocks(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	id := startTranscript(t, db, "asset-1", 1000)
	for index := 0; index < 600; index++ {
		kind := TranscriptChunkKindInput
		data := []byte("not output")
		if index == 599 {
			kind = TranscriptChunkKindOutput
			data = []byte("needle after many blocks")
		}
		appendTranscriptChunks(t, db, id, TranscriptChunkRow{
			Seq: int64(index), TabID: "tab", TS: int64(1001 + index), Kind: kind, Data: data,
		})
	}
	matches, err := db.TranscriptSearch(ctx, id, []byte("needle"), 0, 0)
	if err != nil || len(matches) != 1 || matches[0].Seq != 599 {
		t.Fatalf("search must continue across stored-block pages: matches=%+v err=%v", matches, err)
	}
}
