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

	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
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

func transcriptIdentity(sessionID string) session.TranscriptInfo {
	return session.TranscriptInfo{
		SessionID: sessionID, AssetID: "asset-1", AssetName: "web-01", AssetKind: "ssh",
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

func TestTranscriptWriterPersistsOrderedChunksAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	database, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	writer := startTestWriter(t, database, transcriptWriterConfig{})
	writer.SessionStarted(ctx, transcriptIdentity("session-1"))
	for index := 0; index < 50; index++ {
		writer.SessionOutput(ctx, "", "session-1", "tab-1", []byte(fmt.Sprintf("chunk-%03d\r\n", index)))
	}
	writer.SessionEnded(ctx, "session-1")
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	rows, err := reopened.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one transcript after restart, got %+v", rows)
	}
	row := rows[0]
	if row.SessionID != "session-1" || row.AssetName != "web-01" || row.AssetKind != "ssh" {
		t.Fatalf("identity not persisted: %+v", row)
	}
	if row.EndedAt == nil || row.Chunks != 50 {
		t.Fatalf("end state not persisted: %+v", row)
	}
	chunks := readTranscriptAll(t, reopened, row.ID)
	if len(chunks) != 50 {
		t.Fatalf("expected 50 chunks, got %d", len(chunks))
	}
	for index, chunk := range chunks {
		if chunk.Seq != int64(index) {
			t.Fatalf("chunk %d has seq %d", index, chunk.Seq)
		}
		want := fmt.Sprintf("chunk-%03d\r\n", index)
		if string(chunk.Data) != want {
			t.Fatalf("chunk %d = %q, want %q", index, chunk.Data, want)
		}
		if chunk.TabID != "tab-1" || chunk.TS <= 0 {
			t.Fatalf("chunk %d metadata: %+v", index, chunk)
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
	writer.SessionStarted(ctx, transcriptIdentity("session-throughput"))
	for index := 0; index < total; index++ {
		writer.SessionOutput(ctx, "", "session-throughput", "tab-1", payload)
	}
	enqueueElapsed := time.Since(start)
	writer.SessionEnded(ctx, "session-throughput")
	shutdownCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	rows, err := database.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one transcript, got %+v", rows)
	}
	row := rows[0]
	if row.Chunks != total || row.Bytes != int64(total*len(payload)) {
		t.Fatalf("counters mismatch: %+v", row)
	}
	if row.EndedAt == nil {
		t.Fatal("transcript must be ended after SessionEnded + Shutdown")
	}
	chunks := readTranscriptAll(t, database, row.ID)
	if len(chunks) != total {
		t.Fatalf("expected %d chunks, got %d", total, len(chunks))
	}
	for index, chunk := range chunks {
		if chunk.Seq != int64(index) || len(chunk.Data) != len(payload) {
			t.Fatalf("chunk %d lost ordering or content: seq=%d len=%d", index, chunk.Seq, len(chunk.Data))
		}
	}
	t.Logf("transcript throughput: %d chunks (%d MiB) enqueued in %s, fully persisted in %s",
		total, total*len(payload)>>20, enqueueElapsed, elapsed)
}

func TestTranscriptWriterBackpressureBlocksUntilDrain(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := newTranscriptWriter(transcriptWriterConfig{
		Database: database, Logger: discardTranscriptLogger(), QueueMaxBytes: 4096,
	})
	writer.SessionStarted(ctx, transcriptIdentity("session-backpressure"))
	payload := bytes.Repeat([]byte("y"), 1024)
	blocked := make(chan struct{})
	go func() {
		defer close(blocked)
		for index := 0; index < 64; index++ {
			writer.SessionOutput(ctx, "", "session-backpressure", "tab-1", payload)
		}
	}()
	select {
	case <-blocked:
		t.Fatal("enqueue must block while the queue is above its byte bound")
	case <-time.After(150 * time.Millisecond):
	}
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked:
	case <-time.After(30 * time.Second):
		t.Fatal("enqueue did not resume after the writer started draining")
	}
	writer.SessionEnded(ctx, "session-backpressure")
	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	rows, err := database.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Chunks != 64 {
		t.Fatalf("all 64 chunks must be persisted: %+v", rows)
	}
}

func TestTranscriptWriterTruncatesAtSessionCap(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := startTestWriter(t, database, transcriptWriterConfig{MaxSessionBytes: 2048})
	writer.SessionStarted(ctx, transcriptIdentity("session-cap"))
	payload := bytes.Repeat([]byte("z"), 1024)
	for index := 0; index < 16; index++ {
		writer.SessionOutput(ctx, "", "session-cap", "tab-1", payload)
	}
	writer.SessionEnded(ctx, "session-cap")
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	rows, err := database.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one transcript, got %+v", rows)
	}
	row := rows[0]
	if !row.Truncated {
		t.Fatal("transcript must be marked truncated")
	}
	chunks := readTranscriptAll(t, database, row.ID)
	markers := 0
	payloadChunks := 0
	var recorded []byte
	for _, chunk := range chunks {
		if bytes.Contains(chunk.Data, []byte("size limit reached")) {
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
	if int64(len(recorded))+int64(markers*len(transcriptTruncationMarker)) != row.Bytes {
		t.Fatalf("recorded bytes mismatch: chunks=%d counter=%d", len(recorded)+markers*len(transcriptTruncationMarker), row.Bytes)
	}
}

func TestTranscriptWriterCrashLeavesTranscriptOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	database, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	writer := startTestWriter(t, database, transcriptWriterConfig{})
	writer.SessionStarted(ctx, transcriptIdentity("session-crash"))
	writer.SessionOutput(ctx, "", "session-crash", "tab-1", []byte("before-crash\r\n"))
	waitForTranscriptChunks(t, database, mustTranscriptID(t, database), 1)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	rows, err := reopened.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected the crashed transcript to survive, got %+v", rows)
	}
	if rows[0].EndedAt != nil {
		t.Fatal("a crashed session must keep ended_at unset")
	}
	chunks := readTranscriptAll(t, reopened, rows[0].ID)
	if len(chunks) != 1 || !bytes.Contains(chunks[0].Data, []byte("before-crash")) {
		t.Fatalf("crashed transcript content: %+v", chunks)
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
	writer.SessionEnded(ctx, "unknown-session")
	writer.SessionStarted(ctx, transcriptIdentity("session-dup"))
	writer.SessionOutput(ctx, "", "session-dup", "tab-1", []byte("data\r\n"))
	writer.SessionEnded(ctx, "session-dup")
	writer.SessionEnded(ctx, "session-dup")
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	rows, err := database.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Chunks != 1 || rows[0].EndedAt == nil {
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
		if err := database.TranscriptStart(ctx, store.TranscriptRow{
			ID: id, SessionID: fmt.Sprintf("session-%d", index), AssetID: "asset-1",
			AssetName: "web-01", AssetKind: "ssh", StartedAt: now - int64(4-index)*1000,
		}); err != nil {
			t.Fatal(err)
		}
		if err := database.TranscriptEnd(ctx, id, now-int64(4-index)*1000, false); err != nil {
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
		rows, err = database.TranscriptListByAsset(ctx, "asset-1", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
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

func TestTranscriptWriterReconnectPeriodsSharingBatch(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := newTranscriptWriter(transcriptWriterConfig{Database: database, Logger: discardTranscriptLogger()})
	writer.SessionStarted(ctx, transcriptIdentity("session-shared"))
	writer.SessionOutput(ctx, "", "session-shared", "tab-1", []byte("old-period-output\r\n"))
	writer.SessionEnded(ctx, "session-shared")
	writer.SessionStarted(ctx, transcriptIdentity("session-shared"))
	writer.SessionOutput(ctx, "", "session-shared", "tab-1", []byte("new-period-output\r\n"))
	writer.SessionEnded(ctx, "session-shared")
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	rows, err := database.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected two connection-period transcripts, got %+v", rows)
	}
	var oldRow, newRow store.TranscriptRow
	for _, row := range rows {
		if row.EndedAt == nil {
			t.Fatalf("period must be ended: %+v", row)
		}
		chunks := readTranscriptAll(t, database, row.ID)
		if len(chunks) != 1 {
			t.Fatalf("period %s must have exactly its own chunk, got %+v", row.ID, chunks)
		}
		if chunks[0].Seq != 0 {
			t.Fatalf("period %s chunk seq = %d, want 0", row.ID, chunks[0].Seq)
		}
		switch string(chunks[0].Data) {
		case "old-period-output\r\n":
			oldRow = row
		case "new-period-output\r\n":
			newRow = row
		default:
			t.Fatalf("unexpected chunk content %q", chunks[0].Data)
		}
	}
	if oldRow.ID == "" || newRow.ID == "" || oldRow.ID == newRow.ID {
		t.Fatalf("periods must be isolated: old=%+v new=%+v", oldRow, newRow)
	}
	if newRow.StartedAt < oldRow.StartedAt {
		t.Fatalf("new period must start after the old one: old=%+v new=%+v", oldRow, newRow)
	}
}

func TestTranscriptWriterBackpressureEndDoesNotOvertakeOutput(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := newTranscriptWriter(transcriptWriterConfig{
		Database: database, Logger: discardTranscriptLogger(), QueueMaxBytes: 4096,
	})
	writer.SessionStarted(ctx, transcriptIdentity("session-order"))
	payload := bytes.Repeat([]byte("y"), 1024)
	for index := 0; index < 4; index++ {
		writer.SessionOutput(ctx, "", "session-order", "tab-1", payload)
	}
	tailDone := make(chan struct{})
	go func() {
		defer close(tailDone)
		writer.SessionOutput(ctx, "", "session-order", "tab-1", []byte("tail-output"))
	}()
	time.Sleep(150 * time.Millisecond)
	select {
	case <-tailDone:
		t.Fatal("tail output must wait while the queue is full")
	default:
	}
	endDone := make(chan struct{})
	go func() {
		defer close(endDone)
		writer.SessionEnded(ctx, "session-order")
	}()
	select {
	case <-endDone:
	case <-time.After(5 * time.Second):
		t.Fatal("End must not wedge behind a blocked output")
	}
	select {
	case <-tailDone:
		t.Fatal("tail output must stay blocked until the writer drains")
	default:
	}
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tailDone:
	case <-time.After(30 * time.Second):
		t.Fatal("tail output did not drain")
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	rows, err := database.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].EndedAt == nil {
		t.Fatalf("expected one ended transcript, got %+v", rows)
	}
	chunks := readTranscriptAll(t, database, rows[0].ID)
	if len(chunks) != 5 {
		t.Fatalf("expected 5 chunks including the tail, got %d", len(chunks))
	}
	if !bytes.Contains(chunks[4].Data, []byte("tail-output")) {
		t.Fatalf("tail output must be recorded before End applies: %+v", chunks[4])
	}
}

func TestTranscriptWriterDBStallKeepsOrderAndDoesNotWedge(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := startTestWriter(t, database, transcriptWriterConfig{QueueMaxBytes: 4096})
	writer.SessionStarted(ctx, transcriptIdentity("session-stall"))
	waitForTranscriptChunks(t, database, mustTranscriptID(t, database), 0)

	conn, err := database.DB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("z"), 1024)
	for index := 0; index < 4; index++ {
		writer.SessionOutput(ctx, "", "session-stall", "tab-1", payload)
	}
	time.Sleep(200 * time.Millisecond)
	fillDone := make(chan struct{})
	go func() {
		defer close(fillDone)
		for index := 0; index < 4; index++ {
			writer.SessionOutput(ctx, "", "session-stall", "tab-1", payload)
		}
	}()
	time.Sleep(200 * time.Millisecond)
	blocked := make(chan struct{})
	go func() {
		defer close(blocked)
		writer.SessionOutput(ctx, "", "session-stall", "tab-1", []byte("stalled-tail-output"))
	}()
	time.Sleep(150 * time.Millisecond)
	select {
	case <-blocked:
		t.Fatal("the tail output must block while the database is stalled and the queue is full")
	default:
	}
	endDone := make(chan struct{})
	go func() {
		defer close(endDone)
		writer.SessionEnded(ctx, "session-stall")
	}()
	select {
	case <-endDone:
	case <-time.After(5 * time.Second):
		t.Fatal("End must not wedge during a database stall")
	}
	select {
	case <-blocked:
		t.Fatal("the tail output must stay blocked while the stall lasts")
	default:
	}
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fillDone:
	case <-time.After(30 * time.Second):
		t.Fatal("fill outputs did not drain after the database stall ended")
	}
	select {
	case <-blocked:
	case <-time.After(30 * time.Second):
		t.Fatal("the tail output did not drain after the database stall ended")
	}
	select {
	case <-endDone:
	case <-time.After(30 * time.Second):
		t.Fatal("End did not drain after the database stall ended")
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	rows, err := database.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].EndedAt == nil || rows[0].Chunks != 9 {
		t.Fatalf("all 9 chunks must survive the stall in order: %+v", rows)
	}
	chunks := readTranscriptAll(t, database, rows[0].ID)
	for index, chunk := range chunks {
		if chunk.Seq != int64(index) {
			t.Fatalf("chunk %d lost ordering across the stall: seq=%d", index, chunk.Seq)
		}
	}
	if !bytes.Contains(chunks[8].Data, []byte("stalled-tail-output")) {
		t.Fatalf("the tail output must be recorded before End: %+v", chunks[8])
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
	writer.SessionStarted(ctx, transcriptIdentity("session-cancel"))
	payload := bytes.Repeat([]byte("c"), 1024)
	for index := 0; index < 1; index++ {
		writer.SessionOutput(ctx, "", "session-cancel", "tab-1", payload)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		writer.SessionOutput(cancelled, "", "session-cancel", "tab-1", payload)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("enqueue must return when the caller context is cancelled")
	}
	endDone := make(chan struct{})
	go func() {
		defer close(endDone)
		writer.SessionEnded(cancelled, "session-cancel")
	}()
	select {
	case <-endDone:
	case <-time.After(5 * time.Second):
		t.Fatal("End must not wedge behind a full queue")
	}
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 10*time.Second)
	defer shutdownCancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	rows, err := database.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].EndedAt == nil || rows[0].Chunks != 1 {
		t.Fatalf("the cancelled output must be dropped while the rest completes: %+v", rows)
	}
}

func TestCatchUpProviderPersistsOffsets(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	inner := &fakeCatchUpInner{attachment: &fakeCatchUpAttachment{}}
	provider := &catchUpProvider{DurableProvider: inner, database: database}

	if got := provider.DurableTranscriptCatchUpBytes("tab-1"); got != 0 {
		t.Fatalf("unknown offset = %d, want 0", got)
	}
	if err := database.DurableTranscriptOffsetSet(ctx, "tab-1", 4096); err != nil {
		t.Fatal(err)
	}
	if got := provider.DurableTranscriptCatchUpBytes("tab-1"); got != 4096 {
		t.Fatalf("persisted offset = %d, want 4096", got)
	}
	provider.PersistDurableTranscriptOffset("tab-1", 8192)
	if got := provider.DurableTranscriptCatchUpBytes("tab-1"); got != 8192 {
		t.Fatalf("updated offset = %d, want 8192", got)
	}

	if _, err := provider.Create(ctx, base.DurableCreateOptions{ID: "tab-1"}); err != nil {
		t.Fatal(err)
	}
	if got := provider.DurableTranscriptCatchUpBytes("tab-1"); got != 0 {
		t.Fatalf("create must reset the offset, got %d", got)
	}

	broken := &catchUpProvider{DurableProvider: inner}
	if got := broken.DurableTranscriptCatchUpBytes("tab-1"); got != 0 {
		t.Fatal("nil database must fall back to a zero boundary")
	}
	broken.PersistDurableTranscriptOffset("tab-1", 1)
}

type fakeCatchUpInner struct {
	attachment base.DurableAttachment
}

func (f *fakeCatchUpInner) Create(context.Context, base.DurableCreateOptions) (base.DurableAttachment, error) {
	return f.attachment, nil
}

func (f *fakeCatchUpInner) Attach(context.Context, string) (base.DurableAttachment, error) {
	return f.attachment, nil
}

type fakeCatchUpAttachment struct{}

func (a *fakeCatchUpAttachment) Read([]byte) (int, error)                     { return 0, io.EOF }
func (a *fakeCatchUpAttachment) Write(data []byte) (int, error)               { return len(data), nil }
func (a *fakeCatchUpAttachment) Close() error                                 { return nil }
func (a *fakeCatchUpAttachment) ID() string                                   { return "tab-1" }
func (a *fakeCatchUpAttachment) Generation() uint64                           { return 0 }
func (a *fakeCatchUpAttachment) Resize(context.Context, uint32, uint32) error { return nil }
func (a *fakeCatchUpAttachment) Wait(context.Context) error                   { return nil }
func (a *fakeCatchUpAttachment) Kill(context.Context) error                   { return nil }
func (a *fakeCatchUpAttachment) Stderr() io.Reader                            { return nil }
func (a *fakeCatchUpAttachment) CloseWrite() error                            { return nil }

func TestTranscriptWriterCrashBeforeFlushDoesNotSuppressUnflushed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	database, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	writer := startTestWriter(t, database, transcriptWriterConfig{})
	writer.SessionStarted(ctx, transcriptIdentity("session-unflushed"))
	writer.SessionOutput(ctx, "tab-1", "session-unflushed", "tab-1", []byte("flushed-prefix\r\n"))
	waitForTranscriptChunks(t, database, mustTranscriptID(t, database), 1)
	offset, err := database.DurableTranscriptOffsetGet(ctx, "tab-1")
	if err != nil {
		t.Fatal(err)
	}
	if offset != int64(len("flushed-prefix\r\n")) {
		t.Fatalf("offset = %d, want the flushed byte count", offset)
	}

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
	writer.SessionOutput(ctx, "tab-1", "session-unflushed", "tab-1", []byte("unflushed-tail\r\n"))
	time.Sleep(200 * time.Millisecond)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
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
	offset, err = reopened.DurableTranscriptOffsetGet(ctx, "tab-1")
	if err != nil {
		t.Fatal(err)
	}
	if offset != int64(len("flushed-prefix\r\n")) {
		t.Fatalf("crash must not advance the offset past flushed output, got %d", offset)
	}
	chunks := readTranscriptAll(t, reopened, mustTranscriptID(t, reopened))
	if len(chunks) != 1 || !bytes.Contains(chunks[0].Data, []byte("flushed-prefix")) {
		t.Fatalf("only flushed output may be persisted: %+v", chunks)
	}
}
