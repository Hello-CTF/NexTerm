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
		writer.SessionOutput("session-1", "tab-1", []byte(fmt.Sprintf("chunk-%03d\r\n", index)))
	}
	writer.SessionEnded("session-1")
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
		writer.SessionOutput("session-throughput", "tab-1", payload)
	}
	enqueueElapsed := time.Since(start)
	writer.SessionEnded("session-throughput")
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
			writer.SessionOutput("session-backpressure", "tab-1", payload)
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
	writer.SessionEnded("session-backpressure")
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
		writer.SessionOutput("session-cap", "tab-1", payload)
	}
	writer.SessionEnded("session-cap")
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
	writer.SessionOutput("session-crash", "tab-1", []byte("before-crash\r\n"))
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
	writer.SessionEnded("unknown-session")
	writer.SessionStarted(ctx, transcriptIdentity("session-dup"))
	writer.SessionOutput("session-dup", "tab-1", []byte("data\r\n"))
	writer.SessionEnded("session-dup")
	writer.SessionEnded("session-dup")
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
