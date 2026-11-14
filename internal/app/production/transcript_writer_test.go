package production

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func discardTranscriptLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func startTestWriter(t *testing.T, database *store.Store, config transcriptWriterConfig) *transcriptWriter {
	t.Helper()
	config.Database = database
	if config.Logger == nil {
		config.Logger = discardTranscriptLogger()
	}
	writer := newTranscriptWriter(config)
	if err := writer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = writer.Shutdown(ctx)
	})
	return writer
}

func transcriptIdentity(sessionID, tabID string) session.TranscriptInfo {
	return session.TranscriptInfo{
		SessionID: sessionID, TabID: tabID, TabTitle: "title of " + tabID,
		AssetID: "asset-1", AssetName: "web-01", AssetKind: "ssh",
	}
}

// startTestRecording opens a tab's recording at 80x24 without a starting
// screen, so the recording's only start chunk is the resize at seq 0.
func startTestRecording(ctx context.Context, writer *transcriptWriter, sessionID, tabID string) {
	writer.RecordingStarted(ctx, transcriptIdentity(sessionID, tabID), 80, 24, nil)
}

// testStartResize is the payload of the resize chunk startTestRecording records.
var testStartResize = resizePayload(80, 24)

func requireStartResize(t *testing.T, chunks []store.TranscriptChunkRow) {
	t.Helper()
	if len(chunks) == 0 || chunks[0].Seq != 0 || chunks[0].Kind != store.TranscriptChunkKindResize || !bytes.Equal(chunks[0].Data, testStartResize) {
		t.Fatalf("a recording must start with its geometry at seq 0: %+v", chunks)
	}
}

func waitForTranscriptChunks(t *testing.T, database *store.Store, id string, count int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		row, err := database.TranscriptGet(context.Background(), id)
		if err == nil && row.Chunks >= count {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	row, err := database.TranscriptGet(context.Background(), id)
	t.Fatalf("transcript %s did not reach %d chunks: row=%+v err=%v", id, count, row, err)
}

func waitForTranscriptQueueChunks(t *testing.T, writer *transcriptWriter, count int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		writer.queueMu.Lock()
		queued := writer.queuedChunks
		writer.queueMu.Unlock()
		if queued == count {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("transcript queue did not reach %d chunks", count)
}

func readTranscriptAll(t *testing.T, database *store.Store, id string) []store.TranscriptChunkRow {
	t.Helper()
	var result []store.TranscriptChunkRow
	after := int64(0)
	for {
		chunks, err := database.TranscriptChunks(context.Background(), id, after, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(chunks) == 0 {
			return result
		}
		result = append(result, chunks...)
		after = chunks[len(chunks)-1].Seq + 1
	}
}

func shutdownTestWriter(t *testing.T, writer *transcriptWriter, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := writer.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func listTestTranscripts(t *testing.T, database *store.Store) []store.TranscriptRow {
	t.Helper()
	rows, err := database.TranscriptListByAsset(context.Background(), "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestTranscriptWriterPersistsOrderedChunksAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	database, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	writer := startTestWriter(t, database, transcriptWriterConfig{})
	startTestRecording(ctx, writer, "session-1", "tab-1")
	for index := 0; index < 50; index++ {
		writer.RecordingOutput(ctx, "session-1", "tab-1", []byte(fmt.Sprintf("chunk-%03d\r\n", index)))
	}
	writer.RecordingEnded(ctx, "session-1", "tab-1")
	shutdownTestWriter(t, writer, 10*time.Second)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	rows := listTestTranscripts(t, reopened)
	if len(rows) != 1 {
		t.Fatalf("expected one transcript after restart, got %+v", rows)
	}
	row := rows[0]
	if row.SessionID != "session-1" || row.TabID != "tab-1" || row.TabTitle != "title of tab-1" || row.AssetName != "web-01" || row.AssetKind != "ssh" {
		t.Fatalf("identity not persisted: %+v", row)
	}
	if row.EndedAt == nil || row.Chunks != 51 {
		t.Fatalf("end state not persisted: %+v", row)
	}
	chunks := readTranscriptAll(t, reopened, row.ID)
	if len(chunks) != 51 {
		t.Fatalf("expected the start resize and 50 output chunks, got %d", len(chunks))
	}
	requireStartResize(t, chunks)
	for index, chunk := range chunks[1:] {
		if chunk.Seq != int64(index+1) {
			t.Fatalf("output chunk %d has seq %d", index, chunk.Seq)
		}
		want := fmt.Sprintf("chunk-%03d\r\n", index)
		if string(chunk.Data) != want || chunk.Kind != store.TranscriptChunkKindOutput {
			t.Fatalf("output chunk %d = kind %d %q, want %q", index, chunk.Kind, chunk.Data, want)
		}
		if chunk.TabID != "tab-1" || chunk.TS <= 0 {
			t.Fatalf("output chunk %d metadata: %+v", index, chunk)
		}
	}
}

func TestTranscriptWriterHighThroughputOrdering(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := startTestWriter(t, database, transcriptWriterConfig{MaxSessionBytes: 512 << 20})

	const total = 3000
	payload := bytes.Repeat([]byte("x"), 16<<10)
	start := time.Now()
	startTestRecording(ctx, writer, "session-throughput", "tab-1")
	for index := 0; index < total; index++ {
		writer.RecordingOutput(ctx, "session-throughput", "tab-1", payload)
	}
	enqueueElapsed := time.Since(start)
	writer.RecordingEnded(ctx, "session-throughput", "tab-1")
	shutdownTestWriter(t, writer, 60*time.Second)
	elapsed := time.Since(start)

	rows := listTestTranscripts(t, database)
	if len(rows) != 1 {
		t.Fatalf("expected one transcript, got %+v", rows)
	}
	row := rows[0]
	if row.Chunks != total+1 || row.Bytes != int64(total*len(payload)+len(testStartResize)) {
		t.Fatalf("counters mismatch: %+v", row)
	}
	if row.EndedAt == nil {
		t.Fatal("transcript must be ended after RecordingEnded + Shutdown")
	}
	chunks := readTranscriptAll(t, database, row.ID)
	if len(chunks) != total+1 {
		t.Fatalf("expected %d chunks, got %d", total+1, len(chunks))
	}
	requireStartResize(t, chunks)
	for index, chunk := range chunks[1:] {
		if chunk.Seq != int64(index+1) || len(chunk.Data) != len(payload) {
			t.Fatalf("output chunk %d lost ordering or content: seq=%d len=%d", index, chunk.Seq, len(chunk.Data))
		}
	}
	t.Logf("transcript throughput: %d chunks (%d MiB) enqueued in %s, fully persisted in %s",
		total, total*len(payload)>>20, enqueueElapsed, elapsed)
}

func TestTranscriptWriterBackpressureDropsWithoutBlocking(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := newTranscriptWriter(transcriptWriterConfig{
		Database: database, Logger: discardTranscriptLogger(), QueueMaxBytes: 4096,
	})
	startTestRecording(ctx, writer, "session-backpressure", "tab-1")
	payload := bytes.Repeat([]byte("y"), 1024)
	enqueued := make(chan struct{})
	go func() {
		defer close(enqueued)
		for index := 0; index < 64; index++ {
			writer.RecordingOutput(ctx, "session-backpressure", "tab-1", payload)
		}
	}()
	select {
	case <-enqueued:
	case <-time.After(5 * time.Second):
		t.Fatal("enqueue must not wait for the writer or database to drain")
	}
	writer.RecordingEnded(ctx, "session-backpressure", "tab-1")
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	shutdownTestWriter(t, writer, 30*time.Second)
	stats := writer.statsSnapshot()
	if stats.droppedChunks != 60 || stats.droppedBytes != 60*1024 {
		t.Fatalf("drop accounting = %+v", stats)
	}
	rows := listTestTranscripts(t, database)
	if len(rows) != 1 || rows[0].Chunks != 6 || !rows[0].Truncated {
		t.Fatalf("the start resize, four accepted chunks and one degradation marker must be persisted: %+v", rows)
	}
	chunks := readTranscriptAll(t, database, rows[0].ID)
	requireStartResize(t, chunks)
	if !bytes.Contains(chunks[5].Data, []byte("省略了 60 段输出")) {
		t.Fatalf("degradation marker must describe the loss: %+v", chunks[5])
	}
}

func TestTranscriptWriterTruncatesAtSessionCap(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	// The cap leaves room for the start resize and exactly two payload chunks.
	writer := startTestWriter(t, database, transcriptWriterConfig{MaxSessionBytes: int64(2048 + len(testStartResize))})
	startTestRecording(ctx, writer, "session-cap", "tab-1")
	payload := bytes.Repeat([]byte("z"), 1024)
	for index := 0; index < 16; index++ {
		writer.RecordingOutput(ctx, "session-cap", "tab-1", payload)
	}
	writer.RecordingEnded(ctx, "session-cap", "tab-1")
	shutdownTestWriter(t, writer, 10*time.Second)
	rows := listTestTranscripts(t, database)
	if len(rows) != 1 {
		t.Fatalf("expected one transcript, got %+v", rows)
	}
	row := rows[0]
	if !row.Truncated {
		t.Fatal("transcript must be marked truncated")
	}
	chunks := readTranscriptAll(t, database, row.ID)
	requireStartResize(t, chunks)
	markers := 0
	payloadChunks := 0
	var recorded []byte
	for _, chunk := range chunks[1:] {
		if bytes.Contains(chunk.Data, []byte("录制已达大小上限")) {
			markers++
			continue
		}
		payloadChunks++
		recorded = append(recorded, chunk.Data...)
	}
	if markers != 1 {
		t.Fatalf("expected exactly one truncation marker, got %d", markers)
	}
	if payloadChunks != 2 || len(recorded) != 2048 {
		t.Fatalf("expected exactly 2 payload chunks (2048 bytes) before truncation, got %d chunks %d bytes", payloadChunks, len(recorded))
	}
	if want := int64(len(testStartResize) + len(recorded) + markers*len(transcriptTruncationMarker)); want != row.Bytes {
		t.Fatalf("recorded bytes mismatch: chunks=%d counter=%d", want, row.Bytes)
	}
}

// A crash leaves the recording open with what was flushed; the next writer
// start settles it as ended at its last chunk and truncated.
func TestTranscriptWriterCrashLeavesTranscriptOpenUntilNextStart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	database, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	writer := startTestWriter(t, database, transcriptWriterConfig{})
	startTestRecording(ctx, writer, "session-crash", "tab-1")
	writer.RecordingOutput(ctx, "session-crash", "tab-1", []byte("before-crash\r\n"))
	waitForTranscriptChunks(t, database, mustTranscriptID(t, database), 2)
	// Simulate the crash by closing the store under the running writer, so
	// nothing it does afterwards (ending the recording) reaches the file. Then
	// join the writer and its retention pass before reopening: database/sql
	// Close does not wait for statements in flight, and a reopen must not
	// race the old handle.
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	shutdownTestWriter(t, writer, 10*time.Second)

	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	rows := listTestTranscripts(t, reopened)
	if len(rows) != 1 {
		t.Fatalf("expected the crashed transcript to survive, got %+v", rows)
	}
	if rows[0].EndedAt != nil {
		t.Fatal("a crashed recording must keep ended_at unset")
	}
	chunks := readTranscriptAll(t, reopened, rows[0].ID)
	if len(chunks) != 2 || !bytes.Contains(chunks[1].Data, []byte("before-crash")) {
		t.Fatalf("crashed transcript content: %+v", chunks)
	}

	next := startTestWriter(t, reopened, transcriptWriterConfig{})
	shutdownTestWriter(t, next, 10*time.Second)
	rows = listTestTranscripts(t, reopened)
	if len(rows) != 1 || rows[0].EndedAt == nil || *rows[0].EndedAt != chunks[1].TS || !rows[0].Truncated {
		t.Fatalf("the next start must end the crashed recording at its last chunk, truncated: %+v (last chunk ts %d)", rows, chunks[1].TS)
	}
}

func mustTranscriptID(t *testing.T, database *store.Store) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := database.TranscriptListByAsset(context.Background(), "asset-1", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 1 {
			return rows[0].ID
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("transcript row did not appear")
	return ""
}

func TestTranscriptWriterDuplicateEndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := startTestWriter(t, database, transcriptWriterConfig{})
	writer.RecordingEnded(ctx, "unknown-session", "unknown-tab")
	startTestRecording(ctx, writer, "session-dup", "tab-1")
	writer.RecordingOutput(ctx, "session-dup", "tab-1", []byte("data\r\n"))
	writer.RecordingEnded(ctx, "session-dup", "tab-1")
	writer.RecordingEnded(ctx, "session-dup", "tab-1")
	shutdownTestWriter(t, writer, 10*time.Second)
	rows := listTestTranscripts(t, database)
	if len(rows) != 1 || rows[0].Chunks != 2 || rows[0].EndedAt == nil {
		t.Fatalf("duplicate end corrupted state: %+v", rows)
	}
}

func TestTranscriptWriterRetentionEnforcedWithConfiguredPolicy(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Now().UnixMilli()
	for index := 0; index < 4; index++ {
		id := fmt.Sprintf("01JTRANSCRIPTOLD000000000%02d", index)
		at := now - int64(4-index)*1000
		if err := database.TranscriptStart(ctx, store.TranscriptRow{
			ID: id, SessionID: fmt.Sprintf("session-%d", index), AssetID: "asset-1",
			AssetName: "web-01", AssetKind: "ssh", StartedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
		// A chunk keeps the row from being settled as empty when the writer starts.
		if err := database.TranscriptAppendChunks(ctx, id, []store.TranscriptChunkRow{{Seq: 0, TS: at, Data: []byte("output")}}); err != nil {
			t.Fatal(err)
		}
		if err := database.TranscriptEnd(ctx, id, at, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.SettingSet(ctx, transcriptRetentionSettingKey, `{"maxCount":2}`); err != nil {
		t.Fatal(err)
	}
	writer := startTestWriter(t, database, transcriptWriterConfig{RetentionInterval: time.Hour})
	deadline := time.Now().Add(10 * time.Second)
	var rows []store.TranscriptRow
	for time.Now().Before(deadline) {
		rows = listTestTranscripts(t, database)
		if len(rows) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	shutdownTestWriter(t, writer, 10*time.Second)
	if len(rows) != 2 {
		t.Fatalf("retention on start must keep only the 2 newest, got %+v", rows)
	}
	if rows[0].EndedAt == nil || rows[1].EndedAt == nil || *rows[0].EndedAt <= *rows[1].EndedAt {
		t.Fatalf("wrong transcripts kept: %+v", rows)
	}
}

func TestTranscriptRetentionPolicyParsing(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := newTranscriptWriter(transcriptWriterConfig{Database: database, Logger: discardTranscriptLogger()})

	policy, err := writer.retentionPolicy(ctx)
	if err != nil || policy.MaxAge != transcriptDefaultRetentionMaxAge || policy.MaxCount != transcriptDefaultRetentionMaxCount {
		t.Fatalf("unset policy = %+v err=%v", policy, err)
	}
	if err := database.SettingSet(ctx, transcriptRetentionSettingKey, `{invalid`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.retentionPolicy(ctx); err == nil {
		t.Fatal("invalid JSON must surface an error")
	}
	if err := database.SettingSet(ctx, transcriptRetentionSettingKey, `{"maxAgeMs":-1}`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.retentionPolicy(ctx); err == nil {
		t.Fatal("negative values must surface an error")
	}
	if err := database.SettingSet(ctx, transcriptRetentionSettingKey, `{"maxAgeMs":60000,"maxCount":7}`); err != nil {
		t.Fatal(err)
	}
	policy, err = writer.retentionPolicy(ctx)
	if err != nil || policy.MaxAge != time.Minute || policy.MaxCount != 7 {
		t.Fatalf("configured policy = %+v err=%v", policy, err)
	}
}

// Two recordings of one tab that share a batch stay two transcripts, each
// with only its own chunks.
func TestTranscriptWriterRecordingsOfOneTabSharingBatch(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := newTranscriptWriter(transcriptWriterConfig{Database: database, Logger: discardTranscriptLogger()})
	startTestRecording(ctx, writer, "session-shared", "tab-1")
	writer.RecordingOutput(ctx, "session-shared", "tab-1", []byte("old-recording-output\r\n"))
	writer.RecordingEnded(ctx, "session-shared", "tab-1")
	startTestRecording(ctx, writer, "session-shared", "tab-1")
	writer.RecordingOutput(ctx, "session-shared", "tab-1", []byte("new-recording-output\r\n"))
	writer.RecordingEnded(ctx, "session-shared", "tab-1")
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	shutdownTestWriter(t, writer, 10*time.Second)
	rows := listTestTranscripts(t, database)
	if len(rows) != 2 {
		t.Fatalf("expected two recordings, got %+v", rows)
	}
	var oldRow, newRow store.TranscriptRow
	for _, row := range rows {
		if row.EndedAt == nil {
			t.Fatalf("recording must be ended: %+v", row)
		}
		chunks := readTranscriptAll(t, database, row.ID)
		if len(chunks) != 2 {
			t.Fatalf("recording %s must have exactly its own chunks, got %+v", row.ID, chunks)
		}
		requireStartResize(t, chunks)
		if chunks[1].Seq != 1 {
			t.Fatalf("recording %s output seq = %d, want 1", row.ID, chunks[1].Seq)
		}
		switch string(chunks[1].Data) {
		case "old-recording-output\r\n":
			oldRow = row
		case "new-recording-output\r\n":
			newRow = row
		default:
			t.Fatalf("unexpected chunk content %q", chunks[1].Data)
		}
	}
	if oldRow.ID == "" || newRow.ID == "" || oldRow.ID == newRow.ID {
		t.Fatalf("recordings must be isolated: old=%+v new=%+v", oldRow, newRow)
	}
	if newRow.StartedAt < oldRow.StartedAt {
		t.Fatalf("new recording must start after the old one: old=%+v new=%+v", oldRow, newRow)
	}
}

func TestTranscriptWriterBackpressureEndFollowsQueuedOutputAndDropMarker(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := newTranscriptWriter(transcriptWriterConfig{
		Database: database, Logger: discardTranscriptLogger(), QueueMaxBytes: 4096,
	})
	startTestRecording(ctx, writer, "session-order", "tab-1")
	payload := bytes.Repeat([]byte("y"), 1024)
	for index := 0; index < 4; index++ {
		writer.RecordingOutput(ctx, "session-order", "tab-1", payload)
	}
	tailDone := make(chan struct{})
	go func() {
		defer close(tailDone)
		writer.RecordingOutput(ctx, "session-order", "tab-1", []byte("tail-output"))
	}()
	select {
	case <-tailDone:
	case <-time.After(5 * time.Second):
		t.Fatal("tail output must be dropped rather than block on a full queue")
	}
	endDone := make(chan struct{})
	go func() {
		defer close(endDone)
		writer.RecordingEnded(ctx, "session-order", "tab-1")
	}()
	select {
	case <-endDone:
	case <-time.After(5 * time.Second):
		t.Fatal("End must not wedge behind queued output")
	}
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	shutdownTestWriter(t, writer, 30*time.Second)
	rows := listTestTranscripts(t, database)
	if len(rows) != 1 || rows[0].EndedAt == nil || !rows[0].Truncated {
		t.Fatalf("expected one ended, degraded transcript, got %+v", rows)
	}
	chunks := readTranscriptAll(t, database, rows[0].ID)
	if len(chunks) != 6 {
		t.Fatalf("expected the start resize, 4 accepted chunks and a degradation marker, got %d", len(chunks))
	}
	requireStartResize(t, chunks)
	for index := 1; index <= 4; index++ {
		if !bytes.Equal(chunks[index].Data, payload) || chunks[index].Seq != int64(index) {
			t.Fatalf("accepted output changed order at %d: %+v", index, chunks[index])
		}
	}
	if !bytes.Contains(chunks[5].Data, []byte("省略了 1 段输出")) || bytes.Contains(chunks[5].Data, []byte("tail-output")) {
		t.Fatalf("the dropped tail must be represented by its marker before End: %+v", chunks[5])
	}
}

func TestTranscriptWriterDBStallKeepsOrderAndDoesNotWedge(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := startTestWriter(t, database, transcriptWriterConfig{QueueMaxBytes: 8192})
	startTestRecording(ctx, writer, "session-stall", "tab-1")
	writer.RecordingOutput(ctx, "session-stall", "tab-1", []byte("before-stall\r\n"))
	waitForTranscriptChunks(t, database, mustTranscriptID(t, database), 2)

	conn, err := database.DB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("z"), 1024)
	writer.RecordingOutput(ctx, "session-stall", "tab-1", payload)
	waitForTranscriptQueueChunks(t, writer, 0)
	for index := 1; index < 8; index++ {
		writer.RecordingOutput(ctx, "session-stall", "tab-1", payload)
	}
	writer.RecordingOutput(ctx, "session-stall", "tab-1", []byte("stalled-tail-output"))
	endDone := make(chan struct{})
	go func() {
		defer close(endDone)
		writer.RecordingEnded(ctx, "session-stall", "tab-1")
	}()
	select {
	case <-endDone:
	case <-time.After(5 * time.Second):
		t.Fatal("End must not wedge during a database stall")
	}
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	shutdownTestWriter(t, writer, 30*time.Second)
	rows := listTestTranscripts(t, database)
	if len(rows) != 1 || rows[0].EndedAt == nil || rows[0].Chunks != 11 {
		t.Fatalf("all 11 chunks must survive the stall in order: %+v", rows)
	}
	chunks := readTranscriptAll(t, database, rows[0].ID)
	for index, chunk := range chunks {
		if chunk.Seq != int64(index) {
			t.Fatalf("chunk %d lost ordering across the stall: seq=%d", index, chunk.Seq)
		}
	}
	if !bytes.Contains(chunks[10].Data, []byte("stalled-tail-output")) {
		t.Fatalf("the tail output must be recorded before End: %+v", chunks[10])
	}
}

func TestTranscriptWriterEnqueueHonorsCancelledContext(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := newTranscriptWriter(transcriptWriterConfig{
		Database: database, Logger: discardTranscriptLogger(), QueueMaxBytes: 1024,
	})
	startTestRecording(ctx, writer, "session-cancel", "tab-1")
	payload := bytes.Repeat([]byte("c"), 1024)
	writer.RecordingOutput(ctx, "session-cancel", "tab-1", payload)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		writer.RecordingOutput(cancelled, "session-cancel", "tab-1", payload)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("enqueue must return when the caller context is cancelled")
	}
	endDone := make(chan struct{})
	go func() {
		defer close(endDone)
		writer.RecordingEnded(cancelled, "session-cancel", "tab-1")
	}()
	select {
	case <-endDone:
	case <-time.After(5 * time.Second):
		t.Fatal("End must not wedge behind a full queue")
	}
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	shutdownTestWriter(t, writer, 10*time.Second)
	rows := listTestTranscripts(t, database)
	if len(rows) != 1 || rows[0].EndedAt == nil || rows[0].Chunks != 3 || !rows[0].Truncated {
		t.Fatalf("the cancelled, full-queue output must leave a counted gap while the rest completes: %+v", rows)
	}
	if stats := writer.statsSnapshot(); stats.droppedChunks != 1 || stats.droppedBytes != 1024 {
		t.Fatalf("cancelled full-queue drop accounting = %+v", stats)
	}
}

func TestTranscriptWriterCrashBeforeFlushPersistsOnlyFlushedOutput(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	// The busy-timeout fixture bounds the modernc busy wait, which neither
	// ctx cancellation nor closing the pool can interrupt (M91 probes;
	// TestStoreSQLiteBusyWaitOutlivesCancelAndClose). Whether the tail flush
	// below wins or loses the race against Close, the contended wait now ends
	// well inside the 30s shutdown budget, so the join is deterministic while
	// production keeps the 30s default.
	database, err := store.OpenWithOptions(ctx, path, store.OpenOptions{BusyTimeout: 1500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	writer := startTestWriter(t, database, transcriptWriterConfig{})
	startTestRecording(ctx, writer, "session-unflushed", "tab-1")
	writer.RecordingOutput(ctx, "session-unflushed", "tab-1", []byte("flushed-prefix\r\n"))
	waitForTranscriptChunks(t, database, mustTranscriptID(t, database), 2)

	locker, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	lockConn, err := locker.DB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockConn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	writer.RecordingOutput(ctx, "session-unflushed", "tab-1", []byte("unflushed-tail\r\n"))
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	shutdownTestWriter(t, writer, 30*time.Second)
	if _, err := lockConn.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err := lockConn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := locker.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	chunks := readTranscriptAll(t, reopened, mustTranscriptID(t, reopened))
	if len(chunks) != 2 || !bytes.Contains(chunks[1].Data, []byte("flushed-prefix")) {
		t.Fatalf("only flushed output may be persisted: %+v", chunks)
	}
}

func TestTranscriptWriterShutdownDrainsEnqueuedChunks(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := newTranscriptWriter(transcriptWriterConfig{Database: database, Logger: discardTranscriptLogger()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = writer.Shutdown(ctx)
	})
	startTestRecording(ctx, writer, "session-drain", "tab-1")
	writer.RecordingOutput(ctx, "session-drain", "tab-1", []byte("queued-one\r\n"))
	writer.RecordingResize(ctx, "session-drain", "tab-1", 120, 40)
	writer.RecordingOutput(ctx, "session-drain", "tab-1", []byte("queued-two\r\n"))
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	shutdownTestWriter(t, writer, 10*time.Second)

	rows := listTestTranscripts(t, database)
	if len(rows) != 1 {
		t.Fatalf("expected one transcript, got %+v", rows)
	}
	row := rows[0]
	if row.Chunks != 4 || row.EndedAt == nil {
		t.Fatalf("shutdown must drain queued chunks and end the recording: %+v", row)
	}
	chunks := readTranscriptAll(t, database, row.ID)
	if len(chunks) != 4 {
		t.Fatalf("expected 4 drained chunks, got %+v", chunks)
	}
	requireStartResize(t, chunks)
	wantKinds := []int{
		store.TranscriptChunkKindResize, store.TranscriptChunkKindOutput, store.TranscriptChunkKindResize, store.TranscriptChunkKindOutput,
	}
	for index, chunk := range chunks {
		if chunk.Seq != int64(index) || chunk.Kind != wantKinds[index] {
			t.Fatalf("chunk %d = seq %d kind %d, want kind %d", index, chunk.Seq, chunk.Kind, wantKinds[index])
		}
	}
	if string(chunks[1].Data) != "queued-one\r\n" || string(chunks[3].Data) != "queued-two\r\n" {
		t.Fatalf("drained content mismatch: %q %q", chunks[1].Data, chunks[3].Data)
	}
}

func TestTranscriptWriterShutdownDropsLateItemsWithoutWedging(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := startTestWriter(t, database, transcriptWriterConfig{})
	startTestRecording(ctx, writer, "session-late", "tab-1")
	writer.RecordingOutput(ctx, "session-late", "tab-1", []byte("before-shutdown\r\n"))
	writer.RecordingEnded(ctx, "session-late", "tab-1")
	shutdownTestWriter(t, writer, 10*time.Second)

	lateDone := make(chan struct{})
	go func() {
		defer close(lateDone)
		startTestRecording(ctx, writer, "session-late", "tab-1")
		writer.RecordingOutput(ctx, "session-late", "tab-1", []byte("after-shutdown\r\n"))
		writer.RecordingEnded(ctx, "session-late", "tab-1")
	}()
	select {
	case <-lateDone:
	case <-time.After(5 * time.Second):
		t.Fatal("enqueue after shutdown must not wedge")
	}

	rows := listTestTranscripts(t, database)
	if len(rows) != 1 || rows[0].Chunks != 2 || rows[0].EndedAt == nil {
		t.Fatalf("late items must not be persisted: %+v", rows)
	}
}

func TestTranscriptWriterRecordsOutputAndResizeInOrder(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := startTestWriter(t, database, transcriptWriterConfig{})
	startTestRecording(ctx, writer, "session-kinds", "tab-1")
	writer.RecordingOutput(ctx, "session-kinds", "tab-1", []byte("output-one\r\n"))
	writer.RecordingResize(ctx, "session-kinds", "tab-1", 120, 40)
	writer.RecordingOutput(ctx, "session-kinds", "tab-1", []byte("output-two\r\n"))
	writer.RecordingEnded(ctx, "session-kinds", "tab-1")
	shutdownTestWriter(t, writer, 10*time.Second)

	rows := listTestTranscripts(t, database)
	if len(rows) != 1 {
		t.Fatalf("expected one transcript, got %+v", rows)
	}
	chunks := readTranscriptAll(t, database, rows[0].ID)
	if len(chunks) != 4 {
		t.Fatalf("expected 4 chunks, got %+v", chunks)
	}
	wantKinds := []int{
		store.TranscriptChunkKindResize, store.TranscriptChunkKindOutput,
		store.TranscriptChunkKindResize, store.TranscriptChunkKindOutput,
	}
	for index, chunk := range chunks {
		if chunk.Seq != int64(index) || chunk.Kind != wantKinds[index] {
			t.Fatalf("chunk %d = seq %d kind %d, want kind %d", index, chunk.Seq, chunk.Kind, wantKinds[index])
		}
	}
	if string(chunks[0].Data) != `{"cols":80,"rows":24}` || string(chunks[2].Data) != `{"cols":120,"rows":40}` {
		t.Fatalf("resize payload must encode the geometry: %q %q", chunks[0].Data, chunks[2].Data)
	}
}

func TestTranscriptWriterTruncationStopsResize(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := startTestWriter(t, database, transcriptWriterConfig{MaxSessionBytes: int64(2048 + len(testStartResize))})
	startTestRecording(ctx, writer, "session-cap-kinds", "tab-1")
	payload := bytes.Repeat([]byte("z"), 1024)
	writer.RecordingOutput(ctx, "session-cap-kinds", "tab-1", payload)
	writer.RecordingOutput(ctx, "session-cap-kinds", "tab-1", payload)
	writer.RecordingResize(ctx, "session-cap-kinds", "tab-1", 120, 40)
	writer.RecordingOutput(ctx, "session-cap-kinds", "tab-1", []byte("after-cap"))
	writer.RecordingEnded(ctx, "session-cap-kinds", "tab-1")
	shutdownTestWriter(t, writer, 10*time.Second)

	rows := listTestTranscripts(t, database)
	if len(rows) != 1 || !rows[0].Truncated {
		t.Fatalf("expected one truncated transcript, got %+v", rows)
	}
	chunks := readTranscriptAll(t, database, rows[0].ID)
	if len(chunks) != 4 {
		t.Fatalf("expected the start resize, 2 payload chunks and one marker, got %+v", chunks)
	}
	requireStartResize(t, chunks)
	if chunks[1].Kind != store.TranscriptChunkKindOutput || chunks[2].Kind != store.TranscriptChunkKindOutput {
		t.Fatalf("payload kinds before the cap: %+v", chunks)
	}
	marker := chunks[3]
	if marker.Kind != store.TranscriptChunkKindOutput || !bytes.Contains(marker.Data, []byte("录制已达大小上限")) {
		t.Fatalf("truncation marker must be an output chunk: %+v", marker)
	}
}

// A recording that holds only its starting screen, geometry and screen
// redraws writes nothing: not when it ends, and not when the writer shuts down
// with it still open, so a crash cannot leave it behind either.
func TestTranscriptWriterSkipsRecordingsWithoutOutput(t *testing.T) {
	ctx := context.Background()
	database := newDelayedTranscriptStore()
	database.unblock()
	writer := newTranscriptWriter(transcriptWriterConfig{Database: database, Logger: discardTranscriptLogger()})
	cleanupDelayedTranscriptWriter(t, writer, database)
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	screen := []byte("\x1b[H\x1b[2J$ ")
	writer.RecordingStarted(ctx, transcriptIdentity("session-empty", "tab-ended"), 80, 24, screen)
	writer.RecordingResize(ctx, "session-empty", "tab-ended", 100, 30)
	writer.RecordingScreen(ctx, "session-empty", "tab-ended", 100, 30, screen)
	writer.RecordingEnded(ctx, "session-empty", "tab-ended")
	writer.RecordingStarted(ctx, transcriptIdentity("session-empty", "tab-open"), 80, 24, screen)
	shutdownTestWriter(t, writer, 10*time.Second)

	chunks, ended, events := database.snapshot()
	if started := database.startedRows(); len(started) != 0 || len(chunks) != 0 || len(ended) != 0 || len(events) != 0 {
		t.Fatalf("recordings without output must not reach the store: started=%+v chunks=%+v ended=%+v events=%v", started, chunks, ended, events)
	}
}

// The row is created once output arrives, carrying the tab and the time the
// recording started, with the starting geometry and screen ahead of the output.
func TestTranscriptWriterCreatesRowWithTabOnFirstOutput(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := startTestWriter(t, database, transcriptWriterConfig{})
	screen := []byte("\x1b[H\x1b[2J$ ")
	before := time.Now().UnixMilli()
	writer.RecordingStarted(ctx, transcriptIdentity("session-lazy", "tab-lazy"), 100, 30, screen)
	afterStart := time.Now().UnixMilli()
	// The recording waits for output; nothing is written meanwhile.
	time.Sleep(20 * time.Millisecond)
	if rows := listTestTranscripts(t, database); len(rows) != 0 {
		t.Fatalf("a recording without output must not have a row yet: %+v", rows)
	}
	writer.RecordingOutput(ctx, "session-lazy", "tab-lazy", []byte("first-output\r\n"))
	id := mustTranscriptID(t, database)
	waitForTranscriptChunks(t, database, id, 3)

	row, err := database.TranscriptGet(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.SessionID != "session-lazy" || row.TabID != "tab-lazy" || row.TabTitle != "title of tab-lazy" ||
		row.AssetID != "asset-1" || row.AssetName != "web-01" || row.AssetKind != "ssh" || row.EndedAt != nil {
		t.Fatalf("lazily created row = %+v", row)
	}
	chunks := readTranscriptAll(t, database, id)
	if len(chunks) != 3 {
		t.Fatalf("expected the start resize, the screen and the output, got %+v", chunks)
	}
	if row.StartedAt < before || row.StartedAt > afterStart || row.StartedAt >= chunks[2].TS {
		t.Fatalf("started_at = %d, want the start time in [%d, %d] before the first output at %d", row.StartedAt, before, afterStart, chunks[2].TS)
	}
	if chunks[0].Kind != store.TranscriptChunkKindResize || string(chunks[0].Data) != `{"cols":100,"rows":30}` ||
		chunks[1].Kind != store.TranscriptChunkKindOutput || !bytes.Equal(chunks[1].Data, screen) ||
		string(chunks[2].Data) != "first-output\r\n" {
		t.Fatalf("recording chunks = %+v", chunks)
	}
	for index, chunk := range chunks {
		if chunk.Seq != int64(index) || chunk.TabID != "tab-lazy" {
			t.Fatalf("chunk %d = %+v", index, chunk)
		}
	}
}

// Every tab of a session is its own recording: ending one tab's recording
// ends only its transcript, and shutdown ends the one still open.
func TestTranscriptWriterTabsOfOneSessionAreSeparateRecordings(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := startTestWriter(t, database, transcriptWriterConfig{})
	startTestRecording(ctx, writer, "session-tabs", "tab-a")
	startTestRecording(ctx, writer, "session-tabs", "tab-b")
	writer.RecordingOutput(ctx, "session-tabs", "tab-a", []byte("output-a\r\n"))
	writer.RecordingOutput(ctx, "session-tabs", "tab-b", []byte("output-b\r\n"))
	writer.RecordingEnded(ctx, "session-tabs", "tab-a")

	byTab := func() map[string]store.TranscriptRow {
		result := make(map[string]store.TranscriptRow)
		for _, row := range listTestTranscripts(t, database) {
			result[row.TabID] = row
		}
		return result
	}
	deadline := time.Now().Add(10 * time.Second)
	var rows map[string]store.TranscriptRow
	for {
		rows = byTab()
		if rows["tab-a"].EndedAt != nil && rows["tab-b"].Chunks == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tab-a did not end while tab-b recorded: %+v", rows)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(rows) != 2 || rows["tab-b"].EndedAt != nil {
		t.Fatalf("ending tab-a must leave tab-b's recording open: %+v", rows)
	}
	for tabID, want := range map[string]string{"tab-a": "output-a\r\n", "tab-b": "output-b\r\n"} {
		row := rows[tabID]
		if row.SessionID != "session-tabs" {
			t.Fatalf("%s row = %+v", tabID, row)
		}
		chunks := readTranscriptAll(t, database, row.ID)
		if len(chunks) != 2 || string(chunks[1].Data) != want || chunks[1].TabID != tabID {
			t.Fatalf("%s must hold only its own chunks: %+v", tabID, chunks)
		}
		requireStartResize(t, chunks)
	}

	shutdownTestWriter(t, writer, 10*time.Second)
	if row := byTab()["tab-b"]; row.EndedAt == nil || row.Truncated {
		t.Fatalf("shutdown must end tab-b's recording: %+v", row)
	}
}

// Output that was only dropped from a full queue still happened while the tab
// was recorded: the recording is kept, truncated, with the marker.
func TestTranscriptWriterKeepsRecordingWhoseOutputWasDropped(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := newTranscriptWriter(transcriptWriterConfig{Database: database, Logger: discardTranscriptLogger(), QueueMaxBytes: 1})
	startTestRecording(ctx, writer, "session-dropped", "tab-1")
	writer.RecordingOutput(ctx, "session-dropped", "tab-1", []byte("dropped"))
	writer.RecordingEnded(ctx, "session-dropped", "tab-1")
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	shutdownTestWriter(t, writer, 10*time.Second)
	rows := listTestTranscripts(t, database)
	if len(rows) != 1 || rows[0].EndedAt == nil || !rows[0].Truncated {
		t.Fatalf("a recording with dropped output must be kept truncated: %+v", rows)
	}
	chunks := readTranscriptAll(t, database, rows[0].ID)
	if len(chunks) != 2 || !bytes.Contains(chunks[1].Data, []byte("省略了 1 段输出")) {
		t.Fatalf("the recording must carry the start resize and the drop marker: %+v", chunks)
	}
	requireStartResize(t, chunks)
}

// Losing only a redrawn screen or a resize to a full queue leaves nothing the
// session produced missing, so such a recording still writes nothing.
func TestTranscriptWriterIgnoresDroppedScreensAndResizes(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := newTranscriptWriter(transcriptWriterConfig{Database: database, Logger: discardTranscriptLogger(), QueueMaxBytes: 1})
	startTestRecording(ctx, writer, "session-screen", "tab-1")
	writer.RecordingScreen(ctx, "session-screen", "tab-1", 80, 24, []byte("\x1b[2Jscreen"))
	writer.RecordingResize(ctx, "session-screen", "tab-1", 100, 30)
	writer.RecordingEnded(ctx, "session-screen", "tab-1")
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	shutdownTestWriter(t, writer, 10*time.Second)
	if rows := listTestTranscripts(t, database); len(rows) != 0 {
		t.Fatalf("dropped screens and resizes created a recording: %+v", rows)
	}
}
